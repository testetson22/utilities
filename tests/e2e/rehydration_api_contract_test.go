//go:build e2e

package e2e_test

import (
	"encoding/json"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Rehydration API Contract", Label("rehydration", "contract"), func() {
	BeforeEach(func() {
		requireThreeTierSP()
	})

	It("200 response contains required fields", func() {
		provider := threeTierProviders[0]
		policyID := createPlacementPolicy("tc36-policy", regoSelectProvider(provider.Name))
		defer deletePlacementPolicy(policyID)

		inst := createTestInstance(uniqueName("tc36"), defaultUserValues())
		defer deleteInstance(inst.UID)

		waitForInstanceRunning(inst.ResourceID, provisionTimeout)

		resp, body := rehydrateInstance(inst.UID)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		Expect(body).To(HaveKey("uid"))
		Expect(body).To(HaveKey("spec"))
		Expect(body).To(HaveKey("display_name"))
		Expect(body).To(HaveKey("api_version"))
		Expect(body).To(HaveKey("create_time"))
		Expect(body).To(HaveKey("update_time"))
		Expect(body).To(HaveKey("run_id"), "rehydrate must return run_id after control-plane#39")

		Expect(stringField(body, "uid")).NotTo(BeEmpty())
		Expect(stringField(body, "api_version")).To(Equal("v1alpha1"))
		Expect(stringField(body, "run_id")).NotTo(BeEmpty())

		spec, ok := body["spec"].(map[string]interface{})
		Expect(ok).To(BeTrue(), "spec should be a map")
		Expect(spec).To(HaveKey("catalog_item_id"))
		Expect(firstResourceID(body)).NotTo(BeEmpty(),
			"rehydrate must yield a discoverable placement resource ID")
	})

	It("404 response conforms to RFC 7807", func() {
		resp, rawBody := rehydrateInstanceRaw("nonexistent-" + uniqueName("tc37"))
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))

		var body map[string]interface{}
		Expect(json.Unmarshal(rawBody, &body)).To(Succeed())

		Expect(body).To(HaveKey("type"))
		Expect(body).To(HaveKey("title"))
		Expect(body).To(HaveKey("status"))
		Expect(body).To(HaveKey("detail"))

		status, ok := body["status"].(float64)
		if ok {
			Expect(int(status)).To(Equal(404))
		}
	})

	It("424 response conforms to RFC 7807", func() {
		provider := threeTierProviders[0]
		policyID := createPlacementPolicy("tc38-policy", regoSelectProvider(provider.Name))

		inst := createTestInstance(uniqueName("tc38"), defaultUserValues())
		defer deleteInstance(inst.UID)

		waitForInstanceRunning(inst.ResourceID, provisionTimeout)

		deletePlacementPolicy(policyID)
		deleteAllPolicies()

		resp, rawBody := rehydrateInstanceRaw(inst.UID)
		Expect(resp.StatusCode).To(Equal(http.StatusFailedDependency)) // 424

		var body map[string]interface{}
		Expect(json.Unmarshal(rawBody, &body)).To(Succeed())

		Expect(body).To(HaveKey("type"))
		Expect(body).To(HaveKey("title"))
		Expect(body).To(HaveKey("status"))
		Expect(body).To(HaveKey("detail"))
	})

	It("422/406 provider-error response conforms to RFC 7807", Label("disruptive"), func() {
		// When all providers are stopped, the placement manager returns either:
		//   422 (ErrPlacementManagerProviderError) — provider accepted but errored
		//   406 (ErrPlacementManagerPolicyRejected) — policy found no healthy provider
		// Both map to RFC 7807 bodies with identical shape; this test verifies compliance
		// for whichever code the placement manager returns.
		requirePodman()
		requireMultiProvider()

		provider := threeTierProviders[0]
		policyID := createPlacementPolicy("tc-contract-422", regoSelectProvider(provider.Name))
		defer deletePlacementPolicy(policyID)

		inst := createTestInstance(uniqueName("tc-contract-422"), defaultUserValues())
		defer deleteInstance(inst.UID)

		waitForInstanceRunning(inst.ResourceID, provisionTimeout)

		for _, p := range threeTierProviders {
			if p.ContainerName != "" {
				stopProvider(p)
				defer startProvider(p)
			}
		}
		for _, p := range threeTierProviders {
			waitForProviderHealth(p.Name, "unavailable", healthTimeout)
		}

		resp, rawBody := rehydrateInstanceRaw(inst.UID)
		Expect(resp.StatusCode).To(SatisfyAny(
			Equal(http.StatusUnprocessableEntity), // 422
			Equal(http.StatusNotAcceptable),        // 406
		), "expected provider-error (422) or policy-rejected (406) when all providers are unhealthy")

		var body map[string]interface{}
		Expect(json.Unmarshal(rawBody, &body)).To(Succeed(),
			"response body should be valid JSON")

		Expect(body).To(HaveKey("type"))
		Expect(body).To(HaveKey("title"))
		Expect(body).To(HaveKey("status"))
		Expect(body).To(HaveKey("detail"))

		status, ok := body["status"].(float64)
		if ok {
			Expect(int(status)).To(Equal(resp.StatusCode),
				"RFC 7807 status field should match HTTP status code")
		}
	})
})
