//go:build e2e

package e2e_test

import (
	"fmt"
	"net/http"
	"os"
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

		It("registers a cluster-type provider with environment-agent", func() {
			providers, err := osacEnvAgentProviders()
			Expect(err).NotTo(HaveOccurred())
			Expect(providers).NotTo(BeEmpty(), "environment-agent should have at least one registered provider")

			var found *envAgentProvider
			for i := range providers {
				if providers[i].ServiceType == "cluster" {
					found = &providers[i]
					break
				}
			}
			Expect(found).NotTo(BeNil(), "no cluster-type provider found in environment-agent /providers")
			Expect(found.Endpoint).NotTo(BeEmpty(), "cluster provider endpoint should be set")
		})

		It("registers a vm-type provider with environment-agent", func() {
			providers, err := osacEnvAgentProviders()
			Expect(err).NotTo(HaveOccurred())

			var found *envAgentProvider
			for i := range providers {
				if providers[i].ServiceType == "vm" {
					found = &providers[i]
					break
				}
			}
			Expect(found).NotTo(BeNil(), "no vm-type provider found in environment-agent /providers")
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
			if healthResp.Status == "OK" || healthResp.Status == "HEALTHY" {
				Expect(healthResp.Detail).To(BeEmpty(),
					"detail should be absent from a healthy response (DD-010)")
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

		It("rejects max_page_size=0 with 400", func() {
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters?max_page_size=0", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(SatisfyAny(
				Equal(http.StatusBadRequest),
				Equal(http.StatusOK), // some SPs treat 0 as "use default"
			))
		})

		It("rejects max_page_size > 100 with 400", func() {
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters?max_page_size=101", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
				"max_page_size > 100 should be rejected per AEP-132")
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
				Title:      invalidArgumentTitle,
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
			Expect(listResp.Clusters).To(Or(BeNil(), BeEmpty()))
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

		It("create with the same id is idempotent", func() {
			// REQ-CREATE-040: retry with same id returns existing state, not error.
			name := uniqueName("e2e-osac-cl")
			resp, err := doOsacClusterRequest(http.MethodPost,
				fmt.Sprintf("/clusters?id=%s", clusterID),
				osacClusterPayload(name))
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			// 201 or 200 — existing state returned, not 409
			Expect(resp.StatusCode).To(SatisfyAny(Equal(http.StatusCreated), Equal(http.StatusOK)),
				"duplicate create should return existing state (REQ-CREATE-040), not an error")
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
			resp, err := doOsacClusterRequest(http.MethodGet, "/clusters/"+clusterID, "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var cl osacCluster
			decodeJSON(resp, &cl)
			if cl.Status != "ACTIVE" {
				// Kubeconfig pointer is nil when the JSON field is absent.
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
			Expect(listResp.Clusters).To(ContainElement(HaveField("ID", clusterID)),
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
