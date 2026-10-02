//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoadAuthSettings(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		t.Setenv("DCM_AUTH_ENABLED", "false")
		settings, err := loadAuthSettings()
		if err != nil {
			t.Fatalf("loadAuthSettings() error = %v", err)
		}
		if settings.enabled {
			t.Fatal("expected authentication to be disabled")
		}
	})

	t.Run("enabled requires issuer and credentials", func(t *testing.T) {
		t.Setenv("DCM_AUTH_ENABLED", "true")
		t.Setenv("DCM_AUTH_ISSUER_URL", "")
		if _, err := loadAuthSettings(); err == nil {
			t.Fatal("expected missing issuer to fail")
		}
	})

	t.Run("static token does not require password credentials", func(t *testing.T) {
		t.Setenv("DCM_AUTH_ENABLED", "true")
		t.Setenv("DCM_AUTH_ISSUER_URL", "https://issuer.example/realms/test")
		t.Setenv("DCM_AUTH_TOKEN", "test-token")
		t.Setenv("DCM_AUTH_CLIENT_SECRET", "")
		t.Setenv("DCM_AUTH_USERNAME", "")
		t.Setenv("DCM_AUTH_PASSWORD", "")
		if _, err := loadAuthSettings(); err != nil {
			t.Fatalf("loadAuthSettings() error = %v", err)
		}
	})
}

func TestAuthTransportRefreshesAndInjectsBearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/realms/test/protocol/openid-connect/token":
			if err := request.ParseForm(); err != nil {
				t.Fatalf("ParseForm() error = %v", err)
			}
			if got := request.PostForm.Get("client_id"); got != "dcm-proxy" {
				t.Errorf("client_id = %q, want dcm-proxy", got)
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(writer, `{"access_token":"test-token","expires_in":300}`)
		case "/api/v1alpha1/catalog-items":
			if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Errorf("Authorization = %q, want Bearer test-token", got)
			}
			writer.WriteHeader(http.StatusOK)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	settings := authSettings{
		enabled:      true,
		issuerURL:    server.URL + "/realms/test",
		clientID:     "dcm-proxy",
		clientSecret: "secret",
		username:     "testuser",
		password:     "password",
	}
	provider := &authTokenProvider{
		settings: settings,
		client:   server.Client(),
	}
	client := &http.Client{
		Transport: &authTransport{base: server.Client().Transport, tokens: provider, origin: server.URL},
	}

	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1alpha1/catalog-items", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
}

func TestAuthTransportStripsBearerTokenOnOriginChangingRedirect(t *testing.T) {
	redirectedRequestAuthorization := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirectedRequestAuthorization <- request.Header.Get("Authorization")
		writer.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer gateway.Close()

	provider := &authTokenProvider{
		settings: authSettings{staticToken: "test-token"},
		client:   gateway.Client(),
	}
	client := &http.Client{
		Transport: &authTransport{
			base:   gateway.Client().Transport,
			tokens: provider,
			origin: gateway.URL,
		},
	}

	response, err := client.Get(gateway.URL + "/api/v1alpha1/catalog-items")
	if err != nil {
		t.Fatalf("client.Get() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := <-redirectedRequestAuthorization; got != "" {
		t.Fatalf("redirected Authorization = %q, want empty", got)
	}
}

func TestAuthTokenProviderCachesAndRefreshes(t *testing.T) {
	tokenRequests := 0
	issuer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tokenRequests++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"access_token":"token-%d","expires_in":300}`, tokenRequests)
	}))
	defer issuer.Close()

	provider := &authTokenProvider{
		settings: authSettings{
			issuerURL:    issuer.URL + "/realms/test",
			clientID:     "dcm-proxy",
			clientSecret: "secret",
			username:     "testuser",
			password:     "password",
		},
		client: issuer.Client(),
	}

	firstToken, err := provider.Token(context.Background())
	if err != nil {
		t.Fatalf("first Token() error = %v", err)
	}
	if firstToken != "token-1" {
		t.Fatalf("first token = %q, want token-1", firstToken)
	}

	cachedToken, err := provider.Token(context.Background())
	if err != nil {
		t.Fatalf("cached Token() error = %v", err)
	}
	if cachedToken != firstToken || tokenRequests != 1 {
		t.Fatalf("cached token = %q with %d requests, want %q with 1 request", cachedToken, tokenRequests, firstToken)
	}

	provider.expiresAt = time.Now().Add(10 * time.Second)
	earlyRefreshToken, err := provider.Token(context.Background())
	if err != nil {
		t.Fatalf("early-refresh Token() error = %v", err)
	}
	if earlyRefreshToken != "token-2" || tokenRequests != 2 {
		t.Fatalf("early-refresh token = %q with %d requests, want token-2 with 2 requests", earlyRefreshToken, tokenRequests)
	}

	provider.expiresAt = time.Now().Add(-time.Second)
	expiredRefreshToken, err := provider.Token(context.Background())
	if err != nil {
		t.Fatalf("expired-refresh Token() error = %v", err)
	}
	if expiredRefreshToken != "token-3" || tokenRequests != 3 {
		t.Fatalf("expired-refresh token = %q with %d requests, want token-3 with 3 requests", expiredRefreshToken, tokenRequests)
	}
}
