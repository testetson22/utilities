//go:build e2e

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

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

		// Table-driven: missing and malformed required fields — each entry
		// asserts RFC 9457 with a non-empty detail, not just a plain 400.
		DescribeTable("rejects missing required VM spec fields",
			func(path, body string) {
				resp, err := doOsacVMRequest(http.MethodPost, path, body)
				Expect(err).NotTo(HaveOccurred())
				defer resp.Body.Close()
				expectRFC9457Problem(resp, problemDetailExpectation{
					Status:     http.StatusBadRequest,
					TypeSuffix: "invalid-argument",
					Title:      osacBadRequestTitle,
				})
			},
			Entry("empty body",
				"/vms?id=tbl-vm-empty",
				``),
			Entry("spec present but empty object",
				"/vms?id=tbl-vm-empty-spec",
				`{"spec":{}}`),
			Entry("missing storage",
				"/vms?id=tbl-vm-no-storage",
				`{"spec":{"guest_os":{"type":"rhel-9"},"metadata":{"name":"e2e"},"provider_hints":{"osac":{"template_id":"t","instance_type":"s-4-16"}}}}`),
			Entry("missing guest_os",
				"/vms?id=tbl-vm-no-guestos",
				`{"spec":{"storage":{"disks":[{"name":"boot","capacity":"50GB"}]},"metadata":{"name":"e2e"},"provider_hints":{"osac":{"template_id":"t","instance_type":"s-4-16"}}}}`),
			Entry("wrong field types (disks is not array)",
				"/vms?id=tbl-vm-wrong-types",
				`{"spec":{"storage":{"disks":"not-an-array"}}}`),
		)

		It("rejects a request with no id query parameter with 400", func() {
			resp, err := doOsacVMRequest(http.MethodPost, "/vms",
				`{"spec":{"storage":{"disks":[{"name":"boot","capacity":"50GB"}]},"guest_os":{"type":"rhel-9"},"metadata":{"name":"e2e"},"provider_hints":{"osac":{"template_id":"t","instance_type":"s-4-16"}}}}`)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("rejects a body missing the boot disk with 400 or 422", func() {
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

		It("rejects a body missing provider_hints.osac.template_id with 400 or 422", func() {
			resp, err := doOsacVMRequest(http.MethodPost, "/vms?id=e2e-vm-no-template",
				`{"spec":{"storage":{"disks":[{"name":"boot","capacity":"50GB"}]},"guest_os":{"type":"rhel-9"},"metadata":{"name":"e2e"},"provider_hints":{"osac":{"instance_type":"s-4-16"}}}}`)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(SatisfyAny(
				Equal(http.StatusBadRequest),
				Equal(http.StatusUnprocessableEntity),
			))
		})

		It("rejects a body missing provider_hints.osac.instance_type with 400 or 422", func() {
			resp, err := doOsacVMRequest(http.MethodPost, "/vms?id=e2e-vm-no-itype",
				`{"spec":{"storage":{"disks":[{"name":"boot","capacity":"50GB"}]},"guest_os":{"type":"rhel-9"},"metadata":{"name":"e2e"},"provider_hints":{"osac":{"template_id":"t"}}}}`)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(SatisfyAny(
				Equal(http.StatusBadRequest),
				Equal(http.StatusUnprocessableEntity),
			))
		})

	})

	// ------------------------------------------------------------------ #
	// VM storage boundary validation — no OSAC backend required
	// REQ-VMCREATE-030/060: boot disk required, capacity must be valid.
	// ------------------------------------------------------------------ #

	Context("VM storage boundary validation", func() {

		DescribeTable("rejects invalid disk configurations",
			func(path, body string) {
				resp, err := doOsacVMRequest(http.MethodPost, path, body)
				Expect(err).NotTo(HaveOccurred())
				defer resp.Body.Close()
				Expect(resp.StatusCode).To(SatisfyAny(
					Equal(http.StatusBadRequest),
					Equal(http.StatusUnprocessableEntity),
				), "invalid disk configuration must be rejected")
			},
			Entry("empty disks array (no boot disk)",
				"/vms?id=tbl-vm-disk-empty",
				`{"spec":{"storage":{"disks":[]},"guest_os":{"type":"rhel-9"},"metadata":{"name":"e2e"},"provider_hints":{"osac":{"template_id":"t","instance_type":"s-4-16"}}}}`),
			Entry("two boot disks",
				"/vms?id=tbl-vm-disk-two-boot",
				`{"spec":{"storage":{"disks":[{"name":"boot","capacity":"50GB"},{"name":"boot","capacity":"20GB"}]},"guest_os":{"type":"rhel-9"},"metadata":{"name":"e2e"},"provider_hints":{"osac":{"template_id":"t","instance_type":"s-4-16"}}}}`),
			Entry("boot disk capacity is empty string",
				"/vms?id=tbl-vm-disk-empty-cap",
				`{"spec":{"storage":{"disks":[{"name":"boot","capacity":""}]},"guest_os":{"type":"rhel-9"},"metadata":{"name":"e2e"},"provider_hints":{"osac":{"template_id":"t","instance_type":"s-4-16"}}}}`),
			Entry("disk name is empty string",
				"/vms?id=tbl-vm-disk-empty-name",
				`{"spec":{"storage":{"disks":[{"name":"","capacity":"50GB"}]},"guest_os":{"type":"rhel-9"},"metadata":{"name":"e2e"},"provider_hints":{"osac":{"template_id":"t","instance_type":"s-4-16"}}}}`),
			Entry("disk capacity is zero (0GB)",
				"/vms?id=tbl-vm-disk-zero-cap",
				`{"spec":{"storage":{"disks":[{"name":"boot","capacity":"0GB"}]},"guest_os":{"type":"rhel-9"},"metadata":{"name":"e2e"},"provider_hints":{"osac":{"template_id":"t","instance_type":"s-4-16"}}}}`),
		)

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
				Title:      osacBadRequestTitle,
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
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502) — cannot verify VM list response shape (AEP-132)")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			// AEP-132 contract: key must be 'results', not 'vms', and the value
			// must be a non-null JSON array (json.Unmarshal into []T succeeds for
			// JSON null — check explicitly to catch that case).
			raw := readBody(resp)
			var keyed map[string]json.RawMessage
			Expect(json.Unmarshal(raw, &keyed)).To(Succeed())
			_, hasResults := keyed["results"]
			Expect(hasResults).To(BeTrue(),
				"VM list response must use 'results' key (AEP-132), not 'vms' or another key")
			Expect(string(keyed["results"])).NotTo(Equal("null"),
				"results must be a JSON array, not null")
			var arr []json.RawMessage
			Expect(json.Unmarshal(keyed["results"], &arr)).To(Succeed(),
				"results value must be a JSON array, not null or an object")
		})

	})

	// ------------------------------------------------------------------ #
	// VM query parameter validation — no OSAC backend required
	// ------------------------------------------------------------------ #

	Context("VM list query parameter validation", func() {

		It("rejects max_page_size=-1 with 400 (negative values always invalid)", func() {
			resp, err := doOsacVMRequest(http.MethodGet, "/vms?max_page_size=-1", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502) — cannot verify max_page_size=-1 rejection")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
				"max_page_size=-1 must be rejected with 400")
		})

		It("rejects a non-integer max_page_size with 400", func() {
			resp, err := doOsacVMRequest(http.MethodGet, "/vms?max_page_size=abc", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502)")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest),
				"non-integer max_page_size must be rejected with 400")
		})

		It("max_page_size=0 returns 200 treating 0 as the server default (AEP-132)", func() {
			resp, err := doOsacVMRequest(http.MethodGet, "/vms?max_page_size=0", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502) — cannot verify max_page_size=0 behaviour")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusOK),
				"max_page_size=0 must return 200 (treat as server default per AEP-132), not %d", resp.StatusCode)
		})

	})

	// ------------------------------------------------------------------ #
	// Unknown and malformed resource IDs — GET and DELETE
	// ------------------------------------------------------------------ #

	Context("VM GET and DELETE with unknown or malformed IDs", func() {

		// SP BEHAVIOR NOTE: The OSAC SP does not validate UUID format at the routing layer.
		// Path segments are forwarded to the backend as-is; the backend returns 404 for any
		// ID it does not recognise. Clients cannot distinguish "wrong ID format" from
		// "resource not found" from the response alone. A future improvement would be to
		// reject malformed IDs with 400 at the SP routing layer.

		It("GET with malformed (non-UUID) id returns 404 (SP forwards to backend)", func() {
			// Current behavior: SP does not validate UUID format; backend returns 404.
			resp, err := doOsacVMRequest(http.MethodGet, "/vms/not-a-uuid", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502)")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
				"SP currently returns 404 for malformed VM id (forwarded to backend as-is)")
		})

		It("GET with well-formed but nonexistent UUID returns 404", func() {
			resp, err := doOsacVMRequest(http.MethodGet,
				"/vms/00000000-e2e0-4000-8000-000000000002", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502) — cannot verify 404 for unknown VM")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound),
				"well-formed UUID that does not exist should return 404")
		})

		It("DELETE with malformed (non-UUID) id returns 204 (idempotency contract applied broadly)", func() {
			// Current behavior: idempotent-delete (REQ-DELETE-020) is applied regardless
			// of ID format. A stricter implementation would reject malformed IDs with 400.
			resp, err := doOsacVMRequest(http.MethodDelete, "/vms/not-a-uuid", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502)")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent),
				"SP currently returns 204 for malformed VM id on DELETE (idempotency applied broadly)")
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
			if resp.StatusCode == http.StatusBadGateway {
				Skip("OSAC backend not reachable (502) — cannot verify delete-idempotency behaviour (REQ-DELETE-020)")
			}
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent),
				"OSAC SP delete on non-existent VM must return 204, not 404 (REQ-DELETE-020)")
		})

	})

	// ------------------------------------------------------------------ #
	// CRUD lifecycle — requires real OSAC backend
	// ------------------------------------------------------------------ #

	Context("VM CRUD lifecycle", Label("cluster"), Ordered, func() {

		var (
			vmID            string
			originalName    string
			originalPayload string
		)

		BeforeAll(func() {
			requireOsacSP()
			if os.Getenv("OSAC_E2E_VM_TEMPLATE_ID") == "" {
				// VM CRUD lifecycle is currently blocked by a fulfillment-service infrastructure
				// requirement: CreateComputeInstance requires spec.ipv4_cidr/ipv6_cidr, which
				// in turn requires a VirtualNetwork, which requires a NetworkClass.
				// NetworkClass is admin-only infrastructure not creatable via the osac CLI or
				// public REST API — it must be pre-seeded at the OSAC operator level.
				// Until a NetworkClass is provisioned in the osac-test-backend, VM create
				// will always fail with "field 'spec.network_class' is required".
				Skip("OSAC_E2E_VM_TEMPLATE_ID not set — VM CRUD tests require a real OSAC backend, template, and a pre-seeded NetworkClass (admin infrastructure)")
			}
		})

		AfterAll(func() {
			deleteTestOsacVM(vmID)
		})

		It("VM list endpoint returns a valid response shape", func() {
			// Verify the list endpoint responds with 200 and a well-formed body before
			// we create our test VM. We do not assert the list is empty: the SP may
			// retain VMs from prior runs or concurrent tests.
			resp, err := doOsacVMRequest(http.MethodGet, "/vms", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var listResp osacVMListResponse
			decodeJSON(resp, &listResp)
			// Results may be nil (no VMs) or a non-nil slice; both are valid.
			// The key requirement is that the endpoint parses without error.
		})

		It("creates a VM and returns 201 with id", func() {
			originalName = uniqueName("e2e-osac-vm")
			originalPayload = osacVMPayload(originalName)
			resp, err := doOsacVMRequest(http.MethodPost,
				fmt.Sprintf("/vms?id=%s", originalName),
				originalPayload)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))

			var createResp osacCreateResponse
			decodeJSON(resp, &createResp)
			vmID = osacIDFromCreateResponse(createResp)
			Expect(vmID).NotTo(BeEmpty(), "create response should include an id or path")
		})

		It("retry with identical id and identical body returns the original resource (REQ-VMCREATE-070)", func() {
			// True idempotency: same ?id= AND same request body.
			resp, err := doOsacVMRequest(http.MethodPost,
				fmt.Sprintf("/vms?id=%s", originalName),
				originalPayload) // exact same body as the original create
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(SatisfyAny(Equal(http.StatusCreated), Equal(http.StatusOK)),
				"identical retry should return existing state (REQ-VMCREATE-070), not an error")
			var retryResp osacCreateResponse
			decodeJSON(resp, &retryResp)
			retryID := osacIDFromCreateResponse(retryResp)
			Expect(retryID).To(Equal(vmID),
				"identical retry must return the original resource id %q, not a new id %q (REQ-VMCREATE-070)", vmID, retryID)
		})

		It("retry with same id but different body returns same id (same-ID response)", func() {
			// Observed SP behavior: ?id= is the sole idempotency key. On retry the SP
			// returns 201 with the *same resource id* regardless of body differences.
			// This test asserts same-ID response only — not persistence of the original
			// body — because GET /vms/{id} does not echo `spec` on this SP version.
			differentPayload := osacVMPayload(uniqueName("e2e-osac-vm-conflict"))
			resp, err := doOsacVMRequest(http.MethodPost,
				fmt.Sprintf("/vms?id=%s", originalName),
				differentPayload)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusCreated),
				"conflicting-payload retry must return 201 (same ?id=)")
			var retryResp osacCreateResponse
			decodeJSON(resp, &retryResp)
			retryID := osacIDFromCreateResponse(retryResp)
			Expect(retryID).To(Equal(vmID),
				"conflicting-payload retry must return the same resource id %q", vmID)
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
			Expect(vm.InternalIPAddress).NotTo(BeNil(),
				"internal_ip_address must be present in GET /vms/{id} response (REQ-VMGET-030)")
			Expect(vm.ExternalIPAddress).NotTo(BeNil(),
				"external_ip_address must be present in GET /vms/{id} response (REQ-VMGET-030)")
		})

		It("GET /vms/{id} omits spec (known SP limitation — no field round-trip)", func() {
			// Documented gap: this SP version does not echo submitted spec on GET.
			// Assert the current contract so a future echo becomes a visible change.
			// When echo lands, replace with round-trip checks using osacGuestOSType()
			// (not a hard-coded guest OS) plus storage.disks / metadata.name.
			resp, err := doOsacVMRequest(http.MethodGet, "/vms/"+vmID, "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var vm osacVM
			decodeJSON(resp, &vm)
			Expect(vm.Spec).To(BeNil(),
				"GET /vms/{id} currently omits spec; if the SP adds echo, replace this "+
					"assertion with persisted-field round-trip checks (storage.disks, guest_os.type via osacGuestOSType())")
		})

		It("list results include IP fields on each entry", func() {
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
			// Do NOT clear vmID here — post-delete tests still reference it.
		})

		It("GET returns 404 after deletion", func() {
			Eventually(func() int {
				resp, err := doOsacVMRequest(http.MethodGet, "/vms/"+vmID, "")
				if err != nil {
					return 0
				}
				resp.Body.Close()
				return resp.StatusCode
			}).WithTimeout(30*time.Second).WithPolling(2*time.Second).
				Should(Equal(http.StatusNotFound),
					"GET /vms/%s should return 404 after deletion", vmID)
		})

		It("deleted VM no longer appears in the list", func() {
			resp, err := doOsacVMRequest(http.MethodGet, "/vms", "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var listResp osacVMListResponse
			decodeJSON(resp, &listResp)
			for _, vm := range listResp.Results {
				Expect(vm.ID).NotTo(Equal(vmID),
					"deleted VM %s must not appear in the list", vmID)
			}
		})

		It("second DELETE is idempotent — returns 204 again (REQ-DELETE-020)", func() {
			resp, err := doOsacVMRequest(http.MethodDelete, "/vms/"+vmID, "")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent),
				"second DELETE on already-deleted VM must return 204 (REQ-DELETE-020)")
			vmID = "" // AfterAll guard: resource confirmed gone
		})

	})

})
