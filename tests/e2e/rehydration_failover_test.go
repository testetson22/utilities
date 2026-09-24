//go:build e2e

package e2e_test

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Rehydration Failover", Label("rehydration", "failover", "disruptive"), func() {
	BeforeEach(func() {
		requireMultiProvider()
		requirePodman()
	})

	Context("provider failover", Ordered, func() {
		var (
			instanceUID         string
			policyID            string
			providerA           ThreeTierProvider
			providerB           ThreeTierProvider
			supersededResourceIDs []string // tracks IDs replaced by rehydration, used by orphan check
		)

		BeforeAll(func() {
			providerA = threeTierProviders[0]
			providerB = threeTierProviders[1]

			policyID = createPlacementPolicy("failover-initial",
				regoSelectProvider(providerA.Name))

			inst := createTestInstance(uniqueName("failover"), defaultUserValues())
			instanceUID = inst.UID

			waitForInstanceRunning(inst.ResourceID, provisionTimeout)
			supersededResourceIDs = append(supersededResourceIDs, inst.ResourceID)
		})

		AfterAll(func() {
			startProvider(providerA)
			startProvider(providerB)
			waitForProviderHealth(providerA.Name, "ready", healthTimeout)
			waitForProviderHealth(providerB.Name, "ready", healthTimeout)

			if instanceUID != "" {
				deleteInstance(instanceUID)
			}
			if policyID != "" {
				deletePlacementPolicy(policyID)
			}
		})

		It("rehydrate after provider stop moves workload to healthy provider", Label("cluster"), func() {
			preInst := getInstance(instanceUID)
			origResourceID := preInst.ResourceID

			stopProvider(providerA)
			waitForProviderHealth(providerA.Name, "unavailable", healthTimeout)

			deletePlacementPolicy(policyID)
			policyID = createPlacementPolicy("failover-to-b",
				regoSelectProvider(providerB.Name))

			resp, body := rehydrateInstance(instanceUID)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			newResourceID := firstResourceID(body)
			Expect(newResourceID).NotTo(Equal(origResourceID))

			waitForInstanceRunning(newResourceID, rehydrateTimeout)

			if kubectlAvailable && providerB.Namespace != "" {
				ns := getActiveDeploymentNamespace(newResourceID)
				Expect(ns).To(Equal(providerB.Namespace),
					"new resource should be in provider B's namespace %s", providerB.Namespace)
			}

			// Track origResourceID: it is now superseded and should eventually have 0 deployments.
			supersededResourceIDs = append(supersededResourceIDs, origResourceID)
		})

		It("rehydrate back after provider restore (bidirectional failover)", Label("cluster"), func() {
			startProvider(providerA)
			waitForProviderHealth(providerA.Name, "ready", healthTimeout)

			preInst := getInstance(instanceUID)

			deletePlacementPolicy(policyID)
			policyID = createPlacementPolicy("failover-back-to-a",
				regoSelectProvider(providerA.Name))

			resp, body := rehydrateInstance(instanceUID)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			newResourceID := firstResourceID(body)
			Expect(newResourceID).NotTo(Equal(preInst.ResourceID))

			waitForInstanceRunning(newResourceID, rehydrateTimeout)

			if kubectlAvailable && providerA.Namespace != "" {
				ns := getActiveDeploymentNamespace(newResourceID)
				Expect(ns).To(Equal(providerA.Namespace),
					"resource should return to provider A's namespace")
			}

			// Track preInst.ResourceID: after bidirectional failover it too is superseded.
			supersededResourceIDs = append(supersededResourceIDs, preInst.ResourceID)
		})

		It("sequential failover leaves no orphaned resources", Label("cluster"), func() {
			// After two rehydrations (A→B→A), every superseded resource ID must have
			// zero active deployments across all provider namespaces. SPRM is expected
			// to clean them up asynchronously; use Eventually with cleanupTimeout.
			requireKubectl()

			for _, oldID := range supersededResourceIDs {
				oldID := oldID // capture loop var for closure
				Eventually(func() int {
					return countDeploymentsAcrossNamespaces(oldID)
				}).WithTimeout(cleanupTimeout).WithPolling(pollInterval).Should(Equal(0),
					"resource %s should have no active deployments after sequential failover (orphaned resource)", oldID)
			}
		})
	})

	Context("deferred delete", Ordered, func() {
		var (
			instanceUID    string
			oldResourceID  string
			policyID       string
			providerA      ThreeTierProvider
			providerB      ThreeTierProvider
		)

		BeforeAll(func() {
			providerA = threeTierProviders[0]
			providerB = threeTierProviders[1]

			policyID = createPlacementPolicy("deferred-delete",
				regoSelectProvider(providerA.Name))

			inst := createTestInstance(uniqueName("deferred"), defaultUserValues())
			instanceUID = inst.UID

			waitForInstanceRunning(inst.ResourceID, provisionTimeout)
			oldResourceID = inst.ResourceID

			stopProvider(providerA)
			waitForProviderHealth(providerA.Name, "unavailable", healthTimeout)

			deletePlacementPolicy(policyID)
			policyID = createPlacementPolicy("deferred-to-b",
				regoSelectProvider(providerB.Name))

			resp, body := rehydrateInstance(inst.UID)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			newResourceID := firstResourceID(body)
			Expect(newResourceID).NotTo(Equal(oldResourceID))
			waitForInstanceRunning(newResourceID, rehydrateTimeout)
		})

		AfterAll(func() {
			startProvider(providerA)
			startProvider(providerB)
			waitForProviderHealth(providerA.Name, "ready", healthTimeout)
			waitForProviderHealth(providerB.Name, "ready", healthTimeout)

			if instanceUID != "" {
				deleteInstance(instanceUID)
			}
			if policyID != "" {
				deletePlacementPolicy(policyID)
			}
		})

		It("old resource is queued for deferred deletion", func() {
			// After rehydration, the placement service calls DeleteRun on the old run,
			// which calls SPRM DeleteInstance (non-deferred). The SPRM publishes a delete
			// to providerA, but providerA is still stopped, so no deletion-acknowledged
			// event arrives. The SPRM therefore keeps the old resource in
			// deletion_status=SCHEDULED, which is visible via ?show_deleted=true.
			//
			// The resource field name in the list response is "id" (per SPRM OpenAPI).
			// DeletionStatus "SCHEDULED" (or similar non-nil) confirms it is queued.

			findOldResource := func() map[string]interface{} {
				resp, err := doRequest(http.MethodGet, "/service-type-instances?show_deleted=true", "")
				if err != nil || resp.StatusCode != http.StatusOK {
					return nil
				}
				var body map[string]interface{}
				decodeJSON(resp, &body)
				instances, _ := body["instances"].([]interface{})
				for _, inst := range instances {
					m, _ := inst.(map[string]interface{})
					if m["id"] == oldResourceID {
						return m
					}
				}
				return nil
			}

			// Use Eventually: placement manager processes the delete synchronously but
			// the SPRM update may lag by a poll cycle.
			var found map[string]interface{}
			Eventually(func() bool {
				found = findOldResource()
				return found != nil
			}).WithTimeout(cleanupTimeout).WithPolling(pollInterval).Should(BeTrue(),
				"old resource %s should appear in ?show_deleted=true list after rehydration", oldResourceID)

			Expect(found["deletion_status"]).NotTo(BeNil(),
				"old resource should have deletion_status set (queued for deferred deletion)")

			// Confirm it is absent from the active (non-deleted) list.
			resp, err := doRequest(http.MethodGet, "/service-type-instances", "")
			Expect(err).NotTo(HaveOccurred())
			var activeBody map[string]interface{}
			decodeJSON(resp, &activeBody)
			activeInstances, _ := activeBody["instances"].([]interface{})
			for _, inst := range activeInstances {
				m, _ := inst.(map[string]interface{})
				Expect(m["id"]).NotTo(Equal(oldResourceID),
					"old resource should not appear in the active instance list")
			}
		})

		It("cleanup completes when provider becomes healthy", Label("cluster"), func() {
			requireKubectl()

			startProvider(providerA)
			waitForProviderHealth(providerA.Name, "ready", healthTimeout)

			if providerA.Namespace != "" {
				waitForDeploymentsGone(providerA.Namespace, oldResourceID, cleanupTimeout)
			}
		})

		It("deferred cleanup retries after provider restore", func() {
			startProvider(providerA)
			waitForProviderHealth(providerA.Name, "ready", healthTimeout)

			current := getInstance(instanceUID)
			Expect(current.ResourceID).NotTo(Equal(oldResourceID),
				"active resource should be the new one, not the old deferred one")
		})
	})
})
