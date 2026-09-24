//go:build e2e

package e2e_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
)

const (
	defaultOsacSPURL   = "http://localhost:8091/api/v1alpha1"
	defaultEnvAgentURL = "http://localhost:8090/api/v1alpha1"

	osacClusterNATSSubject = "dcm.cluster"
	osacVMNATSSubject      = "dcm.vm"
)

var (
	osacSPBaseURL   string
	envAgentBaseURL string
	osacSPReady     bool
)

// ── Request types ──────────────────────────────────────────────────────────

// osacClusterCreateRequest is the JSON body for POST /clusters.
type osacClusterCreateRequest struct {
	Spec osacClusterSpec `json:"spec"`
}

type osacClusterSpec struct {
	Version       string               `json:"version"`
	Nodes         osacClusterNodes     `json:"nodes"`
	Metadata      osacResourceMetadata `json:"metadata"`
	ProviderHints osacProviderHints    `json:"provider_hints,omitempty"`
}

type osacClusterNodes struct {
	Worker osacWorkerSpec `json:"worker"`
}

type osacWorkerSpec struct {
	Count int `json:"count"`
}

// osacVMCreateRequest is the JSON body for POST /vms.
type osacVMCreateRequest struct {
	Spec osacVMSpec `json:"spec"`
}

type osacVMSpec struct {
	Storage       osacVMStorage        `json:"storage"`
	GuestOS       osacGuestOS          `json:"guest_os"`
	Metadata      osacResourceMetadata `json:"metadata"`
	ProviderHints osacProviderHints    `json:"provider_hints,omitempty"`
}

type osacVMStorage struct {
	Disks []osacDisk `json:"disks"`
}

type osacDisk struct {
	Name     string `json:"name"`
	Capacity string `json:"capacity"`
}

type osacGuestOS struct {
	Type string `json:"type"`
}

type osacResourceMetadata struct {
	Name string `json:"name"`
}

type osacProviderHints struct {
	OSAC osacBackendHints `json:"osac,omitempty"`
}

type osacBackendHints struct {
	TemplateID   string `json:"template_id,omitempty"`
	InstanceType string `json:"instance_type,omitempty"`
}

// ── Response types ─────────────────────────────────────────────────────────

// osacCreateResponse is the body returned by POST /clusters and POST /vms.
type osacCreateResponse struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// osacHealthResponse is the body returned by GET /clusters/health and GET /vms/health.
type osacHealthResponse struct {
	Type   string  `json:"type"`
	Status string  `json:"status"`
	Path   string  `json:"path"`
	Detail string  `json:"detail,omitempty"`
	Uptime float64 `json:"uptime"`
}

// osacCluster is a single cluster resource from GET /clusters/{id} or a list entry.
// Kubeconfig is a pointer so nil distinguishes "field absent" from "field is empty string",
// enabling the kubeconfig-absent-unless-ACTIVE assertion (REQ-GET-020/030).
type osacCluster struct {
	ID         string  `json:"id"`
	Status     string  `json:"status"`
	Kubeconfig *string `json:"kubeconfig"`
}

// osacClusterListResponse is the body returned by GET /clusters.
// The OSAC SP follows AEP-132 and wraps the list in a "results" key,
// consistent with the VM list endpoint. (Verified against live SP.)
type osacClusterListResponse struct {
	Results []osacCluster `json:"results"`
}

// osacVM is a single VM resource from GET /vms/{id} or a list entry.
// IP address fields are pointers so nil distinguishes "field absent" from
// "field present but unknown (empty string)", per REQ-VMGET-030 / REQ-VMLIST-030.
type osacVM struct {
	ID                string  `json:"id"`
	Status            string  `json:"status"`
	InternalIPAddress *string `json:"internal_ip_address"`
	ExternalIPAddress *string `json:"external_ip_address"`
}

// osacVMListResponse is the body returned by GET /vms (AEP-132: results[] key).
type osacVMListResponse struct {
	Results []osacVM `json:"results"`
}

// envAgentProvider is a single entry from GET /providers on the environment-agent.
type envAgentProvider struct {
	ServiceType string `json:"service_type"`
	Endpoint    string `json:"endpoint"`
	Name        string `json:"name,omitempty"`
}

// envAgentProviderList handles both { providers: [...] } and { results: [...] } shapes.
type envAgentProviderList struct {
	Providers []envAgentProvider `json:"providers,omitempty"`
	Results   []envAgentProvider `json:"results,omitempty"`
}

// osacCloudEvent is a CloudEvents envelope for OSAC status events.
// Data is typed to osacCloudEventData for direct field access without map assertions.
type osacCloudEvent struct {
	SpecVersion     string             `json:"specversion"`
	Type            string             `json:"type"`
	Source          string             `json:"source"`
	ID              string             `json:"id"`
	DataContentType string             `json:"datacontenttype"`
	Data            osacCloudEventData `json:"data"`
}

// osacCloudEventData is the typed payload inside dcm.cluster and dcm.vm events.
// Message is a pointer so nil distinguishes "field absent" from "field is empty string".
type osacCloudEventData struct {
	ID      string  `json:"id"`
	Status  string  `json:"status"`
	Message *string `json:"message"`
}

// ── Init & guards ──────────────────────────────────────────────────────────

// initOsacSP probes both OSAC SP health endpoints and the environment-agent.
// Called from BeforeSuite; sets osacSPReady so requireOsacSP() can skip.
func initOsacSP() {
	osacSPBaseURL = strings.TrimRight(os.Getenv("DCM_OSAC_SP_URL"), "/")
	if osacSPBaseURL == "" {
		osacSPBaseURL = defaultOsacSPURL
	}

	envAgentBaseURL = strings.TrimRight(os.Getenv("DCM_ENVIRONMENT_AGENT_URL"), "/")
	if envAgentBaseURL == "" {
		envAgentBaseURL = defaultEnvAgentURL
	}

	resp, err := httpClient.Get(osacSPBaseURL + "/clusters/health")
	if err != nil {
		GinkgoWriter.Printf("OSAC SP not reachable at %s: %v — OSAC SP tests will be skipped\n",
			osacSPBaseURL, err)
		return
	}
	resp.Body.Close()
	// OSAC SP always returns HTTP 200 for health (DD-010: status lives in body, not HTTP code).
	if resp.StatusCode != http.StatusOK {
		GinkgoWriter.Printf("OSAC SP /clusters/health returned %d — OSAC SP tests will be skipped\n",
			resp.StatusCode)
		return
	}

	osacSPReady = true
	GinkgoWriter.Printf("OSAC SP ready at %s (env-agent at %s)\n", osacSPBaseURL, envAgentBaseURL)
}

// requireOsacSP skips the current test if the OSAC SP is not available.
func requireOsacSP() {
	if !osacSPReady {
		Skip("OSAC SP not available — deploy with --environment-agent --osac-service-provider (port 8091)")
	}
}

// ── HTTP helpers ───────────────────────────────────────────────────────────

// doOsacClusterRequest performs an HTTP request against the OSAC SP's /clusters path.
func doOsacClusterRequest(method, path, body string) (*http.Response, error) {
	return doOsacRequest(osacSPBaseURL+path, method, body)
}

// doOsacVMRequest performs an HTTP request against the OSAC SP's /vms path.
func doOsacVMRequest(method, path, body string) (*http.Response, error) {
	return doOsacRequest(osacSPBaseURL+path, method, body)
}

// doEnvAgentRequest performs an HTTP request against the environment-agent API.
func doEnvAgentRequest(method, path, body string) (*http.Response, error) {
	return doOsacRequest(envAgentBaseURL+path, method, body)
}

func doOsacRequest(url, method, body string) (*http.Response, error) {
	var reqBody io.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return httpClient.Do(req)
}

// ── Payload builders ───────────────────────────────────────────────────────

// osacClusterPayload returns a minimal valid cluster create body.
// Used for CRUD tests against a real OSAC backend.
func osacClusterPayload(name string) string {
	req := osacClusterCreateRequest{
		Spec: osacClusterSpec{
			Version: "1.30",
			Nodes:   osacClusterNodes{Worker: osacWorkerSpec{Count: 1}},
			Metadata: osacResourceMetadata{Name: name},
			ProviderHints: osacProviderHints{
				OSAC: osacBackendHints{
					TemplateID: os.Getenv("OSAC_E2E_CLUSTER_TEMPLATE_ID"),
				},
			},
		},
	}
	b, _ := json.Marshal(req)
	return string(b)
}

// osacVMPayload returns a minimal valid VM create body.
// Used for CRUD tests against a real OSAC backend.
func osacVMPayload(name string) string {
	req := osacVMCreateRequest{
		Spec: osacVMSpec{
			Storage: osacVMStorage{
				Disks: []osacDisk{{Name: "boot", Capacity: "50GB"}},
			},
			GuestOS:  osacGuestOS{Type: osacGuestOSType()},
			Metadata: osacResourceMetadata{Name: name},
			ProviderHints: osacProviderHints{
				OSAC: osacBackendHints{
					TemplateID:   os.Getenv("OSAC_E2E_VM_TEMPLATE_ID"),
					InstanceType: osacInstanceType(),
				},
			},
		},
	}
	b, _ := json.Marshal(req)
	return string(b)
}

func osacGuestOSType() string {
	if v := os.Getenv("OSAC_E2E_GUEST_OS_TYPE"); v != "" {
		return v
	}
	return "rhel-9"
}

func osacInstanceType() string {
	if v := os.Getenv("OSAC_E2E_INSTANCE_TYPE"); v != "" {
		return v
	}
	return "standard-4-16"
}

// ── Resource ID extraction ─────────────────────────────────────────────────

// osacIDFromCreateResponse extracts the resource ID from a decoded create response.
// OSAC SP returns { "id": "...", "path": "clusters/<id>", ... }.
// Falls back to the last path segment when id is absent.
func osacIDFromCreateResponse(r osacCreateResponse) string {
	if r.ID != "" {
		return r.ID
	}
	if r.Path != "" {
		parts := strings.Split(strings.TrimRight(r.Path, "/"), "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	return ""
}

// ── Cleanup helpers ────────────────────────────────────────────────────────

// deleteTestOsacCluster best-effort deletes a cluster via the OSAC SP API.
func deleteTestOsacCluster(id string) {
	if id == "" {
		return
	}
	resp, err := doOsacClusterRequest(http.MethodDelete, "/clusters/"+id, "")
	if err == nil && resp != nil {
		resp.Body.Close()
	}
}

// deleteTestOsacVM best-effort deletes a VM via the OSAC SP API.
func deleteTestOsacVM(id string) {
	if id == "" {
		return
	}
	resp, err := doOsacVMRequest(http.MethodDelete, "/vms/"+id, "")
	if err == nil && resp != nil {
		resp.Body.Close()
	}
}

// ── Status vocabulary ──────────────────────────────────────────────────────

// osacClusterStatusValid returns true if status is one of the 7-value cluster vocabulary.
func osacClusterStatusValid(status string) bool {
	switch status {
	case "PROVISIONING", "ACTIVE", "FAILED", "DELETING", "DELETED", "STOPPED", "STOPPING":
		return true
	}
	return false
}

// osacVMStatusValid returns true if status is one of the 8-value VM vocabulary.
func osacVMStatusValid(status string) bool {
	switch status {
	case "PROVISIONING", "RUNNING", "STOPPED", "FAILED", "DELETING", "STOPPING", "PAUSED", "DELETED":
		return true
	}
	return false
}

// ── Environment-agent helpers ──────────────────────────────────────────────

// osacEnvAgentProviders fetches the typed provider list from environment-agent.
// Handles both { "providers": [...] } and { "results": [...] } response shapes.
func osacEnvAgentProviders() ([]envAgentProvider, error) {
	resp, err := doEnvAgentRequest(http.MethodGet, "/providers", "")
	if err != nil {
		return nil, fmt.Errorf("GET /providers: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /providers status %d", resp.StatusCode)
	}
	var list envAgentProviderList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("decode /providers: %w", err)
	}
	if len(list.Providers) > 0 {
		return list.Providers, nil
	}
	if len(list.Results) > 0 {
		return list.Results, nil
	}
	return nil, fmt.Errorf("no providers found in /providers response")
}
