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

		It("detail is absent when healthy, present when degraded (DD-010)", func() {
			// DD-010: the `detail` field carries the error message when degraded
			// and must be absent (empty) when the SP is healthy.
			// The SP returns "healthy" (lowercase); accept any casing variant.
			// Both branches are real assertions — no skipping regardless of state.
			statusLower := strings.ToLower(healthResp.Status)
			if statusLower == "ok" || statusLower == "healthy" {
				Expect(healthResp.Detail).To(BeEmpty(),
					"detail must be absent from a healthy response (DD-010)")
			} else {
				Expect(healthResp.Detail).NotTo(BeEmpty(),
					"detail must be present when status is %q — degraded state must explain itself (DD-010)", healthResp.Status)
			}
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
			// must be a non-null JSON array (not null — json.Unmarshal into []T succeeds
			// for JSON null, so we check explicitly).
			raw := readBody(resp)
			var keyed map[string]json.RawMessage
			Expect(json.Unmarshal(raw, &keyed)).To(Succeed())
			_, hasResults := keyed["results"]
			Expect(hasResults).To(BeTrue(),
				"cluster list response must use 'results' key (AEP-132), not 'clusters' or another key")
			Expect(string(keyed["results"])).NotTo(Equal("null"),
				"results must be a JSON array, not null")
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

		// Table-driven: missing required nested objects — each rejected with a
		// problem+json body whose detail identifies the offending field.
		DescribeTable("rejects missing required cluster spec fields",
			func(path, body string) {
				resp, err := doOsacClusterRequest(http.MethodPost, path, body)
				Expect(err).NotTo(HaveOccurred())
				defer resp.Body.Close()
				// Generic 400 is not sufficient — assert RFC 9457 with non-empty detail.
				expectRFC9457Problem(resp, problemDetailExpectation{
					Status:     http.StatusBadRequest,
					TypeSuffix: "invalid-argument",
					Title:      osacBadRequestTitle,
				})
			},
			Entry("no spec key",
				"/clusters?id=tbl-cl-no-spec",
				`{}`),
			Entry("spec present but empty object",
				"/clusters?id=tbl-cl-empty-spec",
				`{"spec":{}}`),
			Entry("spec.nodes absent",
				"/clusters?id=tbl-cl-no-nodes",
				`{"spec":{"version":"1.30","metadata":{"name":"e2e"}}}`),
			Entry("spec.nodes.worker absent",
				"/clusters?id=tbl-cl-no-worker",
				`{"spec":{"version":"1.30","nodes":{},"metadata":{"name":"e2e"}}}`),
			Entry("spec.nodes.worker.count is zero",
				"/clusters?id=tbl-cl-zero-count",
				`{"spec":{"version":"1.30","nodes":{"worker":{"count":0}},"metadata":{"name":"e2e"}}}`),
			Entry("spec.nodes.worker.count is negative",
				"/clusters?id=tbl-cl-neg-count",
				`{"spec":{"version":"1.30","nodes":{"worker":{"count":-1}},"metadata":{"name":"e2e"}}}`),
			Entry("spec.version absent",
				"/clusters?id=tbl-cl-no-version",
				`{"spec":{"nodes":{"worker":{"count":1}},"metadata":{"name":"e2e"}}}`),
			Entry("spec.version empty string",
				"/clusters?id=tbl-cl-empty-version",
				`{"spec":{"version":"","nodes":{"worker":{"count":1}},"metadata":{"name":"e2e"}}}`),
		)

	})

	// ------------------------------------------------------------------ #
	// Query parameter validation — no OSAC backend required
	// ------------------------------------------------------------------ #

	Context("cluster list query parameter validation", func() {

		It("rejects max_page_size=-1 with 400 (negative values always invalid)", func() {
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters?max_page_size=-1", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502) — cannot verify max_page_size=-1 rejection")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
				"max_page_size=-1 must be rejected with 400")
		})

		It("rejects a non-integer max_page_size with 400", func() {
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters?max_page_size=abc", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502)")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
				"non-integer max_page_size must be rejected with 400")
		})

		It("max_page_size=0 returns 200 treating 0 as the server default (AEP-132)", func() {
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
		// Tracked under FLPATH-4459 (epic) / FLPATH-4463 (story).
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
	// Unknown and malformed resource IDs — GET and DELETE
	// Malformed IDs (not UUID format) are rejected by the SP itself (no backend needed).
	// Well-formed but nonexistent IDs reach the backend (skip on 502).
	// ------------------------------------------------------------------ #

	Context("cluster GET and DELETE with unknown or malformed IDs", func() {

		// SP BEHAVIOR NOTE: The OSAC SP does not validate UUID format at the routing layer.
		// Path segments are forwarded to the backend as-is; the backend returns 404 for any
		// ID it does not recognise, whether malformed or well-formed. Clients cannot
		// distinguish "wrong ID format" from "resource not found" from the response alone.
		// A future improvement would be to reject malformed IDs with 400 at the SP layer.

		It("GET with malformed (non-UUID) id returns 404 (SP forwards to backend)", func() {
			// Current behavior: SP does not validate UUID format; backend returns 404.
			// Ideal behavior would be 400 at the SP routing layer.
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters/not-a-uuid", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502)")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
				"SP currently returns 404 for malformed cluster id (forwarded to backend as-is)")
		})

		It("GET with well-formed but nonexistent UUID returns 404", func() {
			resp, err := doOsacClusterRequest(http.MethodGet,
				"/clusters/00000000-e2e0-4000-8000-000000000001", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502) — cannot verify 404 for unknown cluster")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
				"well-formed UUID that does not exist should return 404")
		})

		It("DELETE with malformed (non-UUID) id returns 204 (idempotency contract applied broadly)", func() {
			// Current behavior: the SP's idempotent-delete contract (REQ-DELETE-020) is
			// applied regardless of ID format — any delete is treated as "done".
			// A stricter implementation would reject malformed IDs with 400 before
			// the idempotency contract applies.
			resp, err := doOsacClusterRequest(http.MethodDelete, "/clusters/not-a-uuid", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502)")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent),
				"SP currently returns 204 for malformed cluster id on DELETE (idempotency applied broadly)")
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

		var (
			clusterID       string
			originalName    string
			originalPayload string
		)

		BeforeAll(func() {
			requireOsacSP()
			if osacTemplateID() == "" {
				Skip("OSAC_E2E_CLUSTER_TEMPLATE_ID not set — CRUD tests require a real OSAC backend and template")
			}
		})

		AfterAll(func() {
			deleteTestOsacCluster(clusterID)
		})

		It("list endpoint returns a valid response shape", func() {
			// Verify the list endpoint responds with 200 and a well-formed body before
			// we create our test cluster. We do not assert the list is empty: the SP
			// may retain clusters from prior runs or concurrent tests.
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var listResp osacClusterListResponse
			decodeJSON(resp, &listResp)
			// Results may be nil (no clusters) or a non-nil slice; both are valid.
			// The key requirement is that the endpoint parses without error.
		})

		It("creates a cluster and returns 201 with id", func() {
			originalName = uniqueName("e2e-osac-cl")
			originalPayload = osacClusterPayload(originalName)
			resp, err := doOsacClusterRequest(http.MethodPost,
				fmt.Sprintf("/clusters?id=%s", originalName),
				originalPayload)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))

			var createResp osacCreateResponse
			decodeJSON(resp, &createResp)
			clusterID = osacIDFromCreateResponse(createResp)
			Expect(clusterID).NotTo(BeEmpty(), "create response should include an id or path")
		})

		It("retry with identical id and identical body returns the original resource (REQ-CREATE-040)", func() {
			// True idempotency: same ?id= AND same request body. The SP must return
			// the existing resource, not create a new one.
			resp, err := doOsacClusterRequest(http.MethodPost,
				fmt.Sprintf("/clusters?id=%s", originalName),
				originalPayload) // exact same body as the original create
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(SatisfyAny(Equal(http.StatusCreated), Equal(http.StatusOK)),
				"identical retry should return existing state (REQ-CREATE-040), not an error")
			var retryResp osacCreateResponse
			decodeJSON(resp, &retryResp)
			retryID := osacIDFromCreateResponse(retryResp)
			Expect(retryID).To(Equal(clusterID),
				"identical retry must return the original resource id %q, not a new id %q (REQ-CREATE-040)", clusterID, retryID)
		})

		It("retry with same id but different body returns same id (same-ID response)", func() {
			// Observed SP behavior: ?id= is the sole idempotency key. On retry the SP
			// returns 201 with the *same resource id* regardless of body differences.
			// This test asserts same-ID response only — not persistence of the original
			// body — because GET /clusters/{id} does not echo `spec` on this SP version,
			// so we cannot verify first-write-wins of submitted fields via a read path.
			differentPayload := osacClusterPayload(uniqueName("e2e-osac-cl-conflict"))
			resp, err := doOsacClusterRequest(http.MethodPost,
				fmt.Sprintf("/clusters?id=%s", originalName),
				differentPayload)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusCreated),
				"conflicting-payload retry must return 201 (same ?id=)")
			var retryResp osacCreateResponse
			decodeJSON(resp, &retryResp)
			retryID := osacIDFromCreateResponse(retryResp)
			Expect(retryID).To(Equal(clusterID),
				"conflicting-payload retry must return the same resource id %q", clusterID)
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
				"status %q is not in the 8-value cluster vocabulary", cl.Status)
		})

		It("GET /clusters/{id} omits spec (known SP limitation — no field round-trip)", func() {
			// Documented gap: this SP version stores submission inputs but does not echo
			// them on GET. Assert the current contract so a future echo becomes a visible
			// behavior change rather than silent Skip of claimed round-trip coverage.
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters/"+clusterID, "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var cl osacCluster
			decodeJSON(resp, &cl)
			Expect(cl.Spec).To(BeNil(),
				"GET /clusters/{id} currently omits spec; if the SP adds echo, replace this "+
					"assertion with persisted-field round-trip checks (version, nodes.worker.count, metadata.name)")
		})

		It("kubeconfig is absent unless status is ACTIVE", func() {
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters/"+clusterID, "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var cl osacCluster
			decodeJSON(resp, &cl)
			if cl.Status == "ACTIVE" {
				Expect(cl.Kubeconfig).NotTo(BeNil(),
					"kubeconfig must be present when status is ACTIVE (REQ-GET-030)")
				Expect(*cl.Kubeconfig).NotTo(BeEmpty(),
					"kubeconfig must be non-empty when status is ACTIVE (REQ-GET-030)")
			} else {
				// Observed behavior: SP returns `"kubeconfig": ""` (present but empty)
				// rather than omitting the field entirely. Both nil and empty-string
				// satisfy the requirement that kubeconfig content is not populated
				// until the cluster reaches ACTIVE status (REQ-GET-020).
				kubeconfigAbsent := cl.Kubeconfig == nil || *cl.Kubeconfig == ""
				Expect(kubeconfigAbsent).To(BeTrue(),
					"kubeconfig must be absent or empty in the response when status is %q (REQ-GET-020)", cl.Status)
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
			// Do NOT clear clusterID here — post-delete tests still reference it.
		})

		It("GET returns 404 after deletion", func() {
			// Deletion may be asynchronous; use Eventually in case DELETING is intermediate.
			Eventually(func() int {
				resp, err := doOsacClusterRequest(http.MethodGet, "/clusters/"+clusterID, "")
				if err != nil {
					return 0
				}
				resp.Body.Close()
				return resp.StatusCode
			}).WithTimeout(30*time.Second).WithPolling(2*time.Second).
				Should(Equal(http.StatusNotFound),
					"GET /clusters/%s should return 404 after deletion", clusterID)
		})

		It("deleted cluster no longer appears in the list", func() {
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var listResp osacClusterListResponse
			decodeJSON(resp, &listResp)
			for _, cl := range listResp.Results {
				Expect(cl.ID).NotTo(Equal(clusterID),
					"deleted cluster %s must not appear in the list", clusterID)
			}
		})

		It("second DELETE is idempotent — returns 204 again (REQ-DELETE-020)", func() {
			resp, err := doOsacClusterRequest(http.MethodDelete, "/clusters/"+clusterID, "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent),
				"second DELETE on already-deleted cluster must return 204 (REQ-DELETE-020)")
			clusterID = "" // AfterAll guard: resource confirmed gone
		})

	})

})

// osacTemplateID returns the OSAC cluster template ID for CRUD tests.
func osacTemplateID() string {
	return os.Getenv("OSAC_E2E_CLUSTER_TEMPLATE_ID")
}
