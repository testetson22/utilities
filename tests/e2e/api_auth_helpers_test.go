//go:build e2e

package e2e_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultAuthClientID = "dcm-proxy"
	tokenRefreshWindow  = 30 * time.Second
)

type authSettings struct {
	enabled      bool
	issuerURL    string
	clientID     string
	clientSecret string
	username     string
	password     string
	staticToken  string
	caFile       string
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type authTokenProvider struct {
	settings authSettings
	client   *http.Client

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

var (
	gatewayBaseURL        string
	httpClient            *http.Client
	unauthenticatedClient *http.Client
	authEnabled           bool
	authTokens            *authTokenProvider
	authGatewayOrigin     string
)

func loadAuthSettings() (authSettings, error) {
	settings := authSettings{
		enabled:      strings.EqualFold(os.Getenv("DCM_AUTH_ENABLED"), "true"),
		issuerURL:    strings.TrimRight(os.Getenv("DCM_AUTH_ISSUER_URL"), "/"),
		clientID:     os.Getenv("DCM_AUTH_CLIENT_ID"),
		clientSecret: os.Getenv("DCM_AUTH_CLIENT_SECRET"),
		username:     os.Getenv("DCM_AUTH_USERNAME"),
		password:     os.Getenv("DCM_AUTH_PASSWORD"),
		staticToken:  os.Getenv("DCM_AUTH_TOKEN"),
		caFile:       os.Getenv("DCM_AUTH_CA_FILE"),
	}
	if !settings.enabled {
		return settings, nil
	}
	if settings.issuerURL == "" {
		return settings, fmt.Errorf("DCM_AUTH_ISSUER_URL is required when DCM_AUTH_ENABLED=true")
	}
	if settings.clientID == "" {
		settings.clientID = defaultAuthClientID
	}
	if settings.staticToken == "" {
		for name, value := range map[string]string{
			"DCM_AUTH_CLIENT_SECRET": settings.clientSecret,
			"DCM_AUTH_USERNAME":      settings.username,
			"DCM_AUTH_PASSWORD":      settings.password,
		} {
			if value == "" {
				return settings, fmt.Errorf("%s is required when DCM_AUTH_TOKEN is not set", name)
			}
		}
	}
	return settings, nil
}

func configureHTTPClients() error {
	settings, err := loadAuthSettings()
	if err != nil {
		return err
	}
	baseTransport, err := newHTTPTransport(settings.caFile)
	if err != nil {
		return err
	}
	gatewayURL, err := url.Parse(gatewayBaseURL)
	if err != nil || gatewayURL.Scheme == "" || gatewayURL.Host == "" {
		return fmt.Errorf("invalid DCM_GATEWAY_URL: %q", gatewayBaseURL)
	}
	authGatewayOrigin = gatewayURL.Scheme + "://" + gatewayURL.Host
	unauthenticatedClient = &http.Client{Timeout: 10 * time.Second, Transport: baseTransport}
	authEnabled = settings.enabled
	authTokens = nil
	httpClient = unauthenticatedClient
	if settings.enabled {
		authTokens = &authTokenProvider{
			settings: settings,
			client:   &http.Client{Timeout: 10 * time.Second, Transport: baseTransport},
		}
		httpClient = &http.Client{
			Timeout:   10 * time.Second,
			Transport: &authTransport{base: baseTransport, tokens: authTokens, origin: authGatewayOrigin},
		}
	}
	return nil
}

func newHTTPTransport(caFile string) (http.RoundTripper, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("unexpected default HTTP transport type %T", http.DefaultTransport)
	}
	transport := base.Clone()
	if caFile == "" {
		return transport, nil
	}
	caData, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read DCM_AUTH_CA_FILE: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(caData) {
		return nil, fmt.Errorf("DCM_AUTH_CA_FILE contains no certificates: %s", caFile)
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return transport, nil
}

type authTransport struct {
	base   http.RoundTripper
	tokens *authTokenProvider
	origin string
}

func (t *authTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	requestOrigin := request.URL.Scheme + "://" + request.URL.Host
	if requestOrigin != t.origin {
		redirectedRequest := request.Clone(request.Context())
		redirectedRequest.Header.Del("Authorization")
		return t.base.RoundTrip(redirectedRequest)
	}
	token, err := t.tokens.Token(request.Context())
	if err != nil {
		return nil, err
	}
	authenticatedRequest := request.Clone(request.Context())
	authenticatedRequest.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(authenticatedRequest)
}

func (p *authTokenProvider) Token(ctx context.Context) (string, error) {
	if p.settings.staticToken != "" {
		return p.settings.staticToken, nil
	}

	p.mu.Lock()
	if p.token != "" && time.Now().Add(tokenRefreshWindow).Before(p.expiresAt) {
		token := p.token
		p.mu.Unlock()
		return token, nil
	}
	p.mu.Unlock()

	values := url.Values{
		"grant_type":    {"password"},
		"client_id":     {p.settings.clientID},
		"client_secret": {p.settings.clientSecret},
		"username":      {p.settings.username},
		"password":      {p.settings.password},
		"scope":         {"openid"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.settings.issuerURL+"/protocol/openid-connect/token", strings.NewReader(values.Encode()))
	if err != nil {
		return "", fmt.Errorf("create auth token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := p.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("request auth token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("auth token endpoint returned HTTP %d", response.StatusCode)
	}
	var token tokenResponse
	if err := json.NewDecoder(response.Body).Decode(&token); err != nil {
		return "", fmt.Errorf("decode auth token response: %w", err)
	}
	if token.AccessToken == "" {
		return "", fmt.Errorf("auth token response did not contain access_token")
	}
	expiresIn := time.Duration(token.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = time.Minute
	}
	p.mu.Lock()
	p.token = token.AccessToken
	p.expiresAt = time.Now().Add(expiresIn)
	p.mu.Unlock()
	return token.AccessToken, nil
}

func authTokenForCLI() (string, error) {
	if !authEnabled || authTokens == nil {
		return "", nil
	}
	return authTokens.Token(context.Background())
}

func doUnauthenticatedRequest(method, path, body string) (*http.Response, error) {
	return doRequestWithClient(unauthenticatedClient, method, path, body)
}

func doRequestWithClient(client *http.Client, method, path, body string) (*http.Response, error) {
	url := gatewayBaseURL + path
	var requestBody io.Reader
	if body != "" {
		requestBody = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, url, requestBody)
	if err != nil {
		return nil, err
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return client.Do(request)
}
