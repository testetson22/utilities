//go:build e2e

package e2e_test

// RFC 9457 problem detail types for DCM service provider HTTP contract tests
// (FLPATH-4720/4721). These mirror the Error schema in each SP's
// api/v1alpha1/openapi.yaml at the DCM boundary. They are owned here to avoid
// coupling dcm-utilities to SP repos; UDLM-aligned payloads will need parallel
// types when that path ships.

const (
	problemTypeBaseURI = "https://dcm-project.github.io/problems/"

	invalidArgumentTitle = "Invalid argument"
	notFoundTitle        = "Not found"

	// containerMultiErrorDetail matches k8s-container-service-provider httperror.InvalidArgumentMultiDetail.
	containerMultiErrorDetail = "multiple validation errors, see the errors array for details"

	// osacBadRequestTitle is the RFC 9457 title that the OSAC service provider returns
	// for input-validation errors. It differs from invalidArgumentTitle ("Invalid argument")
	// used by other SPs — the OSAC SP uses the HTTP status phrase ("Bad Request") as its title.
	osacBadRequestTitle = "Bad Request"
)

// ProblemDetail is the RFC 9457 core fields shared by DCM service providers.
type ProblemDetail struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

// ErrorDetail is one validation failure inside a multi-error problem response.
type ErrorDetail struct {
	Detail  string  `json:"detail"`
	Pointer *string `json:"pointer,omitempty"`
}

// ContainerProblemDetail adds the errors[] extension used by k8s-container SP.
type ContainerProblemDetail struct {
	ProblemDetail
	Errors []ErrorDetail `json:"errors,omitempty"`
}

// problemDetailExpectation selects which RFC 9457 fields to assert exactly.
// Leave Detail empty to require only that detail is non-empty.
type problemDetailExpectation struct {
	Status     int
	TypeSuffix string
	Title      string
	Detail     string
}
