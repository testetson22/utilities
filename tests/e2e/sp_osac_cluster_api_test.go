//go:build e2e

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OSAC SP — Cluster API", Label("sp", "osac"), func() {

	BeforeEach(func() {
		requireOsacSP()
	})

	// ------------------------------------------------------------------ #
	// Registration
	// ------------------------------------------------------------------ #

	Context("registration with environment-agent", func() {

		// osacSPProviders is a helper that fetches environment-agent providers once.
		// Returns nil on error so callers can Expect.
		osacSPProviders := func() []envAgentProvider {
			providers, err := osacEnvAgentProviders()
			Expect(err).NotTo(HaveOccurred())
			return providers
		}

		It("registers exactly two OSAC providers (osac-sp-cluster + osac-sp-vm)", func() {
			// OSAC SP registers exactly one cluster-type and one vm-type provider, both
			// named with the "osac-sp" prefix. Filtering by name prefix avoids false
			// failures when other SPs (kubevirt, k8s-container, etc.) are also registered
			// in the same environment-agent.
			providers := osacSPProviders()
			var osacProviders []envAgentProvider
			for _, p := range providers {
				if strings.HasPrefix(p.Name, "osac-sp") {
					osacProviders = append(osacProviders, p)
				}
			}
			Expect(osacProviders).To(HaveLen(2),
				"OSAC SP should register exactly 2 providers (osac-sp-cluster + osac-sp-vm), "+
					"found %d OSAC providers in %d total: %+v",
				len(osacProviders), len(providers), providers)
		})

		It("registers a cluster-type provider with name osac-sp-cluster", func() {
			providers := osacSPProviders()

			var found *envAgentProvider
			for i := range providers {
				if providers[i].ServiceType == "cluster" && strings.HasPrefix(providers[i].Name, "osac-sp") {
					found = &providers[i]
					break
				}
			}
			Expect(found).NotTo(BeNil(), "no osac-sp cluster-type provider found in environment-agent /providers")
			Expect(found.Name).To(HavePrefix("osac-sp"), "cluster provider name should start with 'osac-sp'")
			Expect(found.Endpoint).NotTo(BeEmpty(), "cluster provider endpoint should be set")
		})

		It("registers a vm-type provider with name osac-sp-vm", func() {
			providers := osacSPProviders()

			var found *envAgentProvider
			for i := range providers {
				if providers[i].ServiceType == "vm" && strings.HasPrefix(providers[i].Name, "osac-sp") {
					found = &providers[i]
					break
				}
			}
			Expect(found).NotTo(BeNil(), "no osac-sp vm-type provider found in environment-agent /providers")
			Expect(found.Name).To(HavePrefix("osac-sp"), "vm provider name should start with 'osac-sp'")
			Expect(found.Endpoint).NotTo(BeEmpty(), "vm provider endpoint should be set")
		})

	})

	// ------------------------------------------------------------------ #
	// Health — /clusters/health
	// ------------------------------------------------------------------ #

	Context("/clusters/health", Ordered, func() {

		var healthResp osacHealthResponse

		BeforeAll(func() {
			requireOsacSP()
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters/health", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			// OSAC SP always returns HTTP 200 regardless of health status (DD-010).
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			decodeJSON(resp, &healthResp)
		})

		It("returns required schema fields", func() {
			Expect(healthResp.Type).NotTo(BeEmpty(), "type field must be present")
			Expect(healthResp.Status).NotTo(BeEmpty(), "status field must be present")
			Expect(healthResp.Path).NotTo(BeEmpty(), "path field must be present")
		})

		It("returns HTTP 200 regardless of health status (DD-010)", func() {
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters/health", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK),
				"health endpoint must always return 200; health status lives in the body")
		})

		It("does not include detail when status is healthy", func() {
			// DD-010: detail is absent (or empty) when the service is healthy.
			// If the status is neither "OK" nor "HEALTHY" the SP may be degraded —
			// skip rather than silently pass an empty assertion.
			if healthResp.Status != "OK" && healthResp.Status != "HEALTHY" {
				Skip(fmt.Sprintf("SP health status is %q (not OK/HEALTHY) — skipping detail-absent check; backend may be degraded", healthResp.Status))
			}
			Expect(healthResp.Detail).To(BeEmpty(),
				"detail should be absent from a healthy response (DD-010)")
		})

		It("reports increasing uptime over time", func() {
			var h1 osacHealthResponse
			resp1, err := doOsacClusterRequest(http.MethodGet, "/clusters/health", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp1.Body.Close()
			decodeJSON(resp1, &h1)

			time.Sleep(2 * time.Second)

			var h2 osacHealthResponse
			resp2, err := doOsacClusterRequest(http.MethodGet, "/clusters/health", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp2.Body.Close()
			decodeJSON(resp2, &h2)

			Expect(h2.Uptime).To(BeNumerically(">", h1.Uptime),
				"uptime should increase between successive health polls")
		})

	})

	// ------------------------------------------------------------------ #
	// OSAC SP exposes both /clusters/health and /vms/health
	// (dual service type — REQ-DUAL-010)
	// ------------------------------------------------------------------ #

	Context("dual service-type health", func() {

		It("returns 200 on both /clusters/health and /vms/health", func() {
			cResp, err := doOsacClusterRequest(http.MethodGet, "/clusters/health", "")
			Expect(err).NotTo(HaveOccurred())
			defer cResp.Body.Close()
			Expect(cResp.StatusCode).To(Equal(http.StatusOK))

			var clHealth osacHealthResponse
			decodeJSON(cResp, &clHealth)
			Expect(clHealth.Status).NotTo(BeEmpty())

			vResp, err := doOsacVMRequest(http.MethodGet, "/vms/health", "")
			Expect(err).NotTo(HaveOccurred())
			defer vResp.Body.Close()
			Expect(vResp.StatusCode).To(Equal(http.StatusOK))

			var vmHealth osacHealthResponse
			decodeJSON(vResp, &vmHealth)
			Expect(vmHealth.Status).NotTo(BeEmpty())
		})

	})

	// ------------------------------------------------------------------ #
	// Cluster list — AEP-132 response shape uses 'results' key
	// ------------------------------------------------------------------ #

	Context("cluster list response shape", func() {

		It("returns 200 and a results array (AEP-132 wrapper)", func() {
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502) — cannot verify cluster list response shape (AEP-132)")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			// AEP-132 contract: key must be 'results', not 'clusters', and the value
			// must be a JSON array (not null or an object).
			raw := readBody(resp)
			var keyed map[string]json.RawMessage
			Expect(json.Unmarshal(raw, &keyed)).To(Succeed())
			_, hasResults := keyed["results"]
			Expect(hasResults).To(BeTrue(),
				"cluster list response must use 'results' key (AEP-132), not 'clusters' or another key")
			var arr []json.RawMessage
			Expect(json.Unmarshal(keyed["results"], &arr)).To(Succeed(),
				"results value must be a JSON array, not null or an object")
		})

	})

	// ------------------------------------------------------------------ #
	// Input validation — no OSAC backend required
	// ------------------------------------------------------------------ #

	Context("cluster create input validation", func() {

		It("rejects an empty body with 400", func() {
			resp, err := doOsacClusterRequest(http.MethodPost, "/clusters?id=e2e-cluster-empty", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("rejects a request with no id query parameter with 400", func() {
			resp, err := doOsacClusterRequest(http.MethodPost, "/clusters",
				`{"spec":{"version":"1.30","nodes":{"worker":{"count":1}},"metadata":{"name":"e2e"}}}`)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("rejects wrong field types with 400", func() {
			resp, err := doOsacClusterRequest(http.MethodPost, "/clusters?id=e2e-cluster-wrong-types",
				`{"spec":{"nodes":"not-an-object"}}`)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("rejects max_page_size=-1 with 400 (negative values always invalid)", func() {
			// Negative page sizes are unambiguously invalid under AEP-132.
			// Unlike 0 (which means "use default"), -1 has no defined meaning.
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters?max_page_size=-1", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502) — cannot verify max_page_size=-1 rejection")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
				"max_page_size=-1 must be rejected with 400")
		})

		It("max_page_size=0 returns 200 treating 0 as the server default (AEP-132)", func() {
			// AEP-132 (AIP-132) states: "If page_size is 0, the API MUST use the
			// default page size." The OSAC SP forwards this to the fulfillment-service
			// backend, which returns an empty results array (no clusters provisioned)
			// with HTTP 200. Negative values (-1) and non-integers are rejected with 400.
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters?max_page_size=0", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502) — cannot verify max_page_size=0 behaviour")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusOK),
				"max_page_size=0 must return 200 (treat as server default per AEP-132), not %d", resp.StatusCode)
		})

		// KNOWN BACKEND GAP: fulfillment-service does not enforce the AEP-132 constraint
		// that max_page_size > 100 must be rejected with 400. It returns 200 instead.
		// Marked PIt (pending) so the gap appears in test output without failing CI;
		// re-enable by changing PIt → It once the backend enforces the limit.
		PIt("rejects max_page_size > 100 with 400 (AEP-132)", func() {
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters?max_page_size=101", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502) — cannot verify max_page_size>100 rejection")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
				"max_page_size > 100 must be rejected per AEP-132")
		})

	})

	// ------------------------------------------------------------------ #
	// RFC 9457 error contract
	// ------------------------------------------------------------------ #

	Context("RFC 9457 error format", Label("contract"), func() {

		It("returns problem+json on cluster create validation error", func() {
			resp, err := doOsacClusterRequest(http.MethodPost, "/clusters?id=e2e-cluster-rfc9457", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			expectRFC9457Problem(resp, problemDetailExpectation{
				Status:     http.StatusBadRequest,
				TypeSuffix: "invalid-argument",
				Title:      osacBadRequestTitle,
			})
		})

	})

	// ------------------------------------------------------------------ #
	// Delete idempotency — unique OSAC contract (204, not 404)
	// ------------------------------------------------------------------ #

	Context("delete idempotency", func() {

		It("returns 204 (not 404) when deleting a non-existent cluster", func() {
			// OSAC SP tolerates NotFound from OSAC backend and treats it as
			// success (REQ-DELETE-020/DD-080). This differs from ACM and KubeVirt
			// SPs which return 404 on non-existent delete.
			// Use a UUID-format ID so OSAC does not reject it as malformed.
			resp, err := doOsacClusterRequest(http.MethodDelete,
				"/clusters/00000000-e2e0-4000-8000-delete0cl0000", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502) — cannot verify delete-idempotency behaviour (REQ-DELETE-020)")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent),
				"OSAC SP delete on non-existent resource must return 204, not 404 (REQ-DELETE-020)")
		})

	})

	// ------------------------------------------------------------------ #
	// CRUD lifecycle — requires real OSAC backend
	// ------------------------------------------------------------------ #

	Context("cluster CRUD lifecycle", Label("cluster"), Ordered, func() {

		var clusterID string

		BeforeAll(func() {
			requireOsacSP()
			if osacTemplateID() == "" {
				Skip("OSAC_E2E_CLUSTER_TEMPLATE_ID not set — CRUD tests require a real OSAC backend and template")
			}
		})

		AfterAll(func() {
			deleteTestOsacCluster(clusterID)
		})

		It("returns an empty or non-error list when no clusters exist", func() {
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var listResp osacClusterListResponse
			decodeJSON(resp, &listResp)
			Expect(listResp.Results).To(Or(BeNil(), BeEmpty()))
		})

		It("creates a cluster and returns 201 with id", func() {
			name := uniqueName("e2e-osac-cl")
			resp, err := doOsacClusterRequest(http.MethodPost,
				fmt.Sprintf("/clusters?id=%s", name),
				osacClusterPayload(name))
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))

			var createResp osacCreateResponse
			decodeJSON(resp, &createResp)
			clusterID = osacIDFromCreateResponse(createResp)
			Expect(clusterID).NotTo(BeEmpty(), "create response should include an id or path")
		})

		It("create with the same id is idempotent and returns the original resource", func() {
			// REQ-CREATE-040: retry with same id returns existing state, not an error.
			// The response must identify the same resource (same id), not a new one.
			name := uniqueName("e2e-osac-cl")
			resp, err := doOsacClusterRequest(http.MethodPost,
				fmt.Sprintf("/clusters?id=%s", clusterID),
				osacClusterPayload(name))
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			// 201 or 200 — existing state returned, not 409
			Expect(resp.StatusCode).To(SatisfyAny(Equal(http.StatusCreated), Equal(http.StatusOK)),
				"duplicate create should return existing state (REQ-CREATE-040), not an error")
			var dupeResp osacCreateResponse
			decodeJSON(resp, &dupeResp)
			dupeID := osacIDFromCreateResponse(dupeResp)
			Expect(dupeID).To(Equal(clusterID),
				"idempotent create must return the original resource id %q, not a new id %q (REQ-CREATE-040)", clusterID, dupeID)
		})

		It("get returns the cluster with a valid status", func() {
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters/"+clusterID, "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var cl osacCluster
			decodeJSON(resp, &cl)
			Expect(cl.ID).To(Equal(clusterID))
			Expect(osacClusterStatusValid(cl.Status)).To(BeTrue(),
				"status %q is not in the 7-value cluster vocabulary", cl.Status)
		})

		It("kubeconfig is absent unless status is ACTIVE", func() {
			// REQ-GET-020/030: kubeconfig only populated when status == ACTIVE.
			// Kubeconfig is a *string so nil == field absent, non-nil == field present
			// (even if value is empty string).
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters/"+clusterID, "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var cl osacCluster
			decodeJSON(resp, &cl)
			if cl.Status == "ACTIVE" {
				// ACTIVE clusters must expose a kubeconfig (non-nil, non-empty).
				Expect(cl.Kubeconfig).NotTo(BeNil(),
					"kubeconfig must be present when status is ACTIVE (REQ-GET-030)")
				Expect(*cl.Kubeconfig).NotTo(BeEmpty(),
					"kubeconfig must be non-empty when status is ACTIVE (REQ-GET-030)")
			} else {
				// All other statuses must NOT include kubeconfig.
				Expect(cl.Kubeconfig).To(BeNil(),
					"kubeconfig must not be present in the response when status is %q (REQ-GET-020)", cl.Status)
			}
		})

		It("list includes the created cluster", func() {
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var listResp osacClusterListResponse
			decodeJSON(resp, &listResp)
			Expect(listResp.Results).To(ContainElement(HaveField("ID", clusterID)),
				"created cluster %s should appear in the list", clusterID)
		})

		It("deletes the cluster and returns 204", func() {
			resp, err := doOsacClusterRequest(http.MethodDelete, "/clusters/"+clusterID, "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
			clusterID = "" // AfterAll guard: already deleted
		})

	})

})

// osacTemplateID returns the OSAC cluster template ID for CRUD tests.
func osacTemplateID() string {
	return os.Getenv("OSAC_E2E_CLUSTER_TEMPLATE_ID")
}
