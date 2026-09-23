//go:build e2e

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OSAC SP — VM API", Label("sp", "osac"), func() {

	BeforeEach(func() {
		requireOsacSP()
	})

	// ------------------------------------------------------------------ #
	// Input validation — no OSAC backend required
	// ------------------------------------------------------------------ #

	Context("VM create input validation", func() {

		It("rejects an empty body with 400", func() {
			resp, err := doOsacVMRequest(http.MethodPost, "/vms?id=e2e-vm-empty", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("rejects a request with no id query parameter with 400", func() {
			resp, err := doOsacVMRequest(http.MethodPost, "/vms",
				`{"spec":{"storage":{"disks":[{"name":"boot","capacity":"50GB"}]},"guest_os":{"type":"rhel-9"},"metadata":{"name":"e2e"},"provider_hints":{"osac":{"template_id":"t","instance_type":"s-4-16"}}}}`)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("rejects a body missing the boot disk with 400", func() {
			// REQ-VMCREATE-030/060: exactly one disk named 'boot' is required.
			resp, err := doOsacVMRequest(http.MethodPost, "/vms?id=e2e-vm-no-boot",
				`{"spec":{"storage":{"disks":[{"name":"data","capacity":"50GB"}]},"guest_os":{"type":"rhel-9"},"metadata":{"name":"e2e"},"provider_hints":{"osac":{"template_id":"t","instance_type":"s-4-16"}}}}`)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(SatisfyAny(
				Equal(http.StatusBadRequest),
				Equal(http.StatusUnprocessableEntity),
			))
		})

		It("rejects a body missing provider_hints.osac.template_id with 400", func() {
			resp, err := doOsacVMRequest(http.MethodPost, "/vms?id=e2e-vm-no-template",
				`{"spec":{"storage":{"disks":[{"name":"boot","capacity":"50GB"}]},"guest_os":{"type":"rhel-9"},"metadata":{"name":"e2e"},"provider_hints":{"osac":{"instance_type":"s-4-16"}}}}`)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(SatisfyAny(
				Equal(http.StatusBadRequest),
				Equal(http.StatusUnprocessableEntity),
			))
		})

		It("rejects a body missing provider_hints.osac.instance_type with 400", func() {
			resp, err := doOsacVMRequest(http.MethodPost, "/vms?id=e2e-vm-no-itype",
				`{"spec":{"storage":{"disks":[{"name":"boot","capacity":"50GB"}]},"guest_os":{"type":"rhel-9"},"metadata":{"name":"e2e"},"provider_hints":{"osac":{"template_id":"t"}}}}`)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(SatisfyAny(
				Equal(http.StatusBadRequest),
				Equal(http.StatusUnprocessableEntity),
			))
		})

		It("rejects wrong field types with 400", func() {
			resp, err := doOsacVMRequest(http.MethodPost, "/vms?id=e2e-vm-wrong-types",
				`{"spec":{"storage":{"disks":"not-an-array"}}}`)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

	})

	// ------------------------------------------------------------------ #
	// RFC 9457 error contract
	// ------------------------------------------------------------------ #

	Context("RFC 9457 error format", Label("contract"), func() {

		It("returns problem+json on VM create validation error", func() {
			resp, err := doOsacVMRequest(http.MethodPost, "/vms?id=e2e-vm-rfc9457", "")
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
	// VM list — AEP-132 response shape uses 'results' key
	// ------------------------------------------------------------------ #

	Context("VM list response shape", func() {

		It("returns 200 and a results array (AEP-132 wrapper)", func() {
			resp, err := doOsacVMRequest(http.MethodGet, "/vms", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			// AEP-132 contract: key must be 'results', not 'vms'.
			// Read raw bytes and check the exact key name before decoding into the typed struct.
			raw := readBody(resp)
			var keyed map[string]json.RawMessage
			Expect(json.Unmarshal(raw, &keyed)).To(Succeed())
			_, hasResults := keyed["results"]
			Expect(hasResults).To(BeTrue(),
				"VM list response must use 'results' key (AEP-132), not 'vms' or another key")
		})

	})

	// ------------------------------------------------------------------ #
	// Delete idempotency — unique OSAC contract (204, not 404)
	// ------------------------------------------------------------------ #

	Context("delete idempotency", func() {

		It("returns 204 (not 404) when deleting a non-existent VM", func() {
			// Use a UUID-format ID so OSAC does not reject it as malformed.
			resp, err := doOsacVMRequest(http.MethodDelete,
				"/vms/00000000-e2e0-4000-8000-delete0vm0000", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent),
				"OSAC SP delete on non-existent VM must return 204, not 404 (REQ-DELETE-020)")
		})

	})

	// ------------------------------------------------------------------ #
	// CRUD lifecycle — requires real OSAC backend
	// ------------------------------------------------------------------ #

	Context("VM CRUD lifecycle", Label("cluster"), Ordered, func() {

		var vmID string

		BeforeAll(func() {
			requireOsacSP()
			if os.Getenv("OSAC_E2E_VM_TEMPLATE_ID") == "" {
				Skip("OSAC_E2E_VM_TEMPLATE_ID not set — VM CRUD tests require a real OSAC backend and template")
			}
		})

		AfterAll(func() {
			deleteTestOsacVM(vmID)
		})

		It("returns an empty results array when no VMs exist", func() {
			resp, err := doOsacVMRequest(http.MethodGet, "/vms", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var listResp osacVMListResponse
			decodeJSON(resp, &listResp)
			Expect(listResp.Results).To(Or(BeNil(), BeEmpty()))
		})

		It("creates a VM and returns 201 with id", func() {
			name := uniqueName("e2e-osac-vm")
			resp, err := doOsacVMRequest(http.MethodPost,
				fmt.Sprintf("/vms?id=%s", name),
				osacVMPayload(name))
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))

			var createResp osacCreateResponse
			decodeJSON(resp, &createResp)
			vmID = osacIDFromCreateResponse(createResp)
			Expect(vmID).NotTo(BeEmpty(), "create response should include an id or path")
		})

		It("create with the same id is idempotent", func() {
			name := uniqueName("e2e-osac-vm")
			resp, err := doOsacVMRequest(http.MethodPost,
				fmt.Sprintf("/vms?id=%s", vmID),
				osacVMPayload(name))
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(SatisfyAny(Equal(http.StatusCreated), Equal(http.StatusOK)),
				"duplicate create should return existing state (REQ-VMCREATE-070), not an error")
		})

		It("get returns the VM with a valid status and IP fields", func() {
			resp, err := doOsacVMRequest(http.MethodGet, "/vms/"+vmID, "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var vm osacVM
			decodeJSON(resp, &vm)
			Expect(vm.ID).To(Equal(vmID))
			Expect(osacVMStatusValid(vm.Status)).To(BeTrue(),
				"status %q is not in the 8-value VM vocabulary", vm.Status)
			// REQ-VMGET-030: IP fields always present, may be empty string when unknown.
			// *string fields are nil if the JSON key is absent vs. non-nil for empty string.
			Expect(vm.InternalIPAddress).NotTo(BeNil(),
				"internal_ip_address must be present in GET /vms/{id} response (REQ-VMGET-030)")
			Expect(vm.ExternalIPAddress).NotTo(BeNil(),
				"external_ip_address must be present in GET /vms/{id} response (REQ-VMGET-030)")
		})

		It("list results include IP fields on each entry", func() {
			// REQ-VMLIST-030: IP fields populated identically on list entries.
			resp, err := doOsacVMRequest(http.MethodGet, "/vms", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var listResp osacVMListResponse
			decodeJSON(resp, &listResp)
			Expect(listResp.Results).NotTo(BeEmpty())

			for _, vm := range listResp.Results {
				Expect(vm.InternalIPAddress).NotTo(BeNil(),
					"each VM list entry should have internal_ip_address (REQ-VMLIST-030)")
				Expect(vm.ExternalIPAddress).NotTo(BeNil(),
					"each VM list entry should have external_ip_address (REQ-VMLIST-030)")
			}
		})

		It("deletes the VM and returns 204", func() {
			resp, err := doOsacVMRequest(http.MethodDelete, "/vms/"+vmID, "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
			vmID = "" // AfterAll guard: already deleted
		})

	})

})
