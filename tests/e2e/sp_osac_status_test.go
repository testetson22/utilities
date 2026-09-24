//go:build e2e

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OSAC SP — NATS Status Events", Label("sp", "osac", "nats"), func() {

	var nc *nats.Conn

	BeforeEach(func() {
		requireOsacSP()

		natsAddr := os.Getenv("DCM_NATS_URL")
		if natsAddr == "" {
			natsAddr = defaultNATSURL
		}
		var err error
		nc, err = nats.Connect(natsAddr, nats.Timeout(3*time.Second))
		if err != nil {
			Skip(fmt.Sprintf("NATS not reachable at %s: %v — skipping status event tests", natsAddr, err))
		}
	})

	AfterEach(func() {
		if nc != nil {
			nc.Close()
		}
	})

	// ------------------------------------------------------------------ #
	// Cluster status events on dcm.cluster
	// Requires real OSAC backend to trigger status polling.
	// Single-publisher ordering is guaranteed by NATS; the poll loop below
	// drains messages until a matching cluster ID is found.
	// ------------------------------------------------------------------ #

	Context("cluster status events", Label("cluster"), func() {

		It("publishes a CloudEvent on dcm.cluster when a cluster is created", func() {
			templateID := osacTemplateID()
			if templateID == "" {
				Skip("OSAC_E2E_CLUSTER_TEMPLATE_ID not set — cluster status tests require a real OSAC backend")
			}

			sub, err := nc.SubscribeSync(osacClusterNATSSubject)
			Expect(err).NotTo(HaveOccurred())
			defer sub.Unsubscribe()
			Expect(nc.Flush()).To(Succeed())

			name := uniqueName("e2e-osac-nats-cl")
			resp, err := doOsacClusterRequest(http.MethodPost,
				fmt.Sprintf("/clusters?id=%s", name),
				osacClusterPayload(name))
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			var createResp osacCreateResponse
			decodeJSON(resp, &createResp)
			clusterID := osacIDFromCreateResponse(createResp)
			Expect(clusterID).NotTo(BeEmpty())
			defer deleteTestOsacCluster(clusterID)

			GinkgoWriter.Printf("Waiting for dcm.cluster CloudEvent for cluster %s\n", clusterID)

			deadline := time.Now().Add(120 * time.Second)
			var matched *osacCloudEvent
			for time.Now().Before(deadline) {
				msg, err := sub.NextMsg(5 * time.Second)
				if err != nil {
					continue // timeout or closed — keep polling
				}
				var event osacCloudEvent
				if err := json.Unmarshal(msg.Data, &event); err != nil {
					GinkgoWriter.Printf("dcm.cluster: ignoring non-JSON message (%d bytes): %v\n", len(msg.Data), err)
					continue
				}
				if event.Data.ID == clusterID {
					matched = &event
					break
				}
			}
			Expect(matched).NotTo(BeNil(),
				"no dcm.cluster CloudEvent received for cluster %s within 120s", clusterID)

			// CloudEvent envelope assertions (REQ-PUBLISH-030)
			Expect(matched.SpecVersion).To(Equal("1.0"),
				"CloudEvent specversion must be '1.0'")
			Expect(matched.Type).To(Equal("dcm.status.cluster"),
				"CloudEvent type should be dcm.status.cluster")
			// Actual source format observed: "dcm/providers/osac-sp-cluster"
			// The SP uses dcm/providers/<provider-name> — not just the bare "osac-sp" prefix.
			Expect(matched.Source).To(HavePrefix("dcm/providers/osac-sp"),
				"CloudEvent source must follow the dcm/providers/<provider-name> format; actual: %s", matched.Source)
			Expect(matched.ID).NotTo(BeEmpty(),
				"CloudEvent id must be set (unique per event)")
			Expect(matched.DataContentType).To(Equal("application/json"),
				"CloudEvent datacontenttype must be application/json")

			// Data payload assertions — typed fields, no map casting
			Expect(matched.Data.ID).To(Equal(clusterID))
			Expect(osacClusterStatusValid(matched.Data.Status)).To(BeTrue(),
				"status %q is not in the 8-value cluster vocabulary", matched.Data.Status)
			// message is a *string: non-nil means field is present (may be empty string).
			Expect(matched.Data.Message).NotTo(BeNil(),
				"data.message field should be present in the CloudEvent (may be empty string)")
		})

	})

	// ------------------------------------------------------------------ #
	// VM status events on dcm.vm
	// Requires real OSAC backend to trigger status polling.
	// Single-publisher ordering is guaranteed by NATS; the poll loop below
	// drains messages until a matching VM ID is found.
	// ------------------------------------------------------------------ #

	Context("VM status events", Label("cluster"), func() {

		It("publishes a CloudEvent on dcm.vm when a VM is created", func() {
			if os.Getenv("OSAC_E2E_VM_TEMPLATE_ID") == "" {
				Skip("OSAC_E2E_VM_TEMPLATE_ID not set — VM status tests require a real OSAC backend")
			}

			sub, err := nc.SubscribeSync(osacVMNATSSubject)
			Expect(err).NotTo(HaveOccurred())
			defer sub.Unsubscribe()
			Expect(nc.Flush()).To(Succeed())

			name := uniqueName("e2e-osac-nats-vm")
			resp, err := doOsacVMRequest(http.MethodPost,
				fmt.Sprintf("/vms?id=%s", name),
				osacVMPayload(name))
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()

			var createResp osacCreateResponse
			decodeJSON(resp, &createResp)
			vmID := osacIDFromCreateResponse(createResp)
			Expect(vmID).NotTo(BeEmpty())
			defer deleteTestOsacVM(vmID)

			GinkgoWriter.Printf("Waiting for dcm.vm CloudEvent for VM %s\n", vmID)

			deadline := time.Now().Add(120 * time.Second)
			var matched *osacCloudEvent
			for time.Now().Before(deadline) {
				msg, err := sub.NextMsg(5 * time.Second)
				if err != nil {
					continue // timeout or closed — keep polling
				}
				var event osacCloudEvent
				if err := json.Unmarshal(msg.Data, &event); err != nil {
					GinkgoWriter.Printf("dcm.vm: ignoring non-JSON message (%d bytes): %v\n", len(msg.Data), err)
					continue
				}
				if event.Data.ID == vmID {
					matched = &event
					break
				}
			}
			Expect(matched).NotTo(BeNil(),
				"no dcm.vm CloudEvent received for VM %s within 120s", vmID)

			// CloudEvent envelope assertions (REQ-PUBLISH-030)
			Expect(matched.SpecVersion).To(Equal("1.0"),
				"CloudEvent specversion must be '1.0'")
			Expect(matched.Type).To(Equal("dcm.status.vm"),
				"CloudEvent type should be dcm.status.vm")
			// Actual source format observed: "dcm/providers/osac-sp-vm"
			Expect(matched.Source).To(HavePrefix("dcm/providers/osac-sp"),
				"CloudEvent source must follow the dcm/providers/<provider-name> format; actual: %s", matched.Source)
			Expect(matched.ID).NotTo(BeEmpty(),
				"CloudEvent id must be set (unique per event)")
			Expect(matched.DataContentType).To(Equal("application/json"),
				"CloudEvent datacontenttype must be application/json")

			// Data payload assertions — VM uses 8-value status vocabulary (DD-121)
			Expect(matched.Data.ID).To(Equal(vmID))
			Expect(osacVMStatusValid(matched.Data.Status)).To(BeTrue(),
				"status %q is not in the 8-value VM vocabulary", matched.Data.Status)
			Expect(matched.Data.Message).NotTo(BeNil(),
				"data.message field should be present in the CloudEvent (may be empty string)")
		})

	})

})
