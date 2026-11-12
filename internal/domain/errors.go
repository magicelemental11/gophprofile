package domain

import "errors"

// Domain errors mapped to API responses.
var (
	ErrNotFound      = errors.New("avatar not found")
	ErrForbidden     = errors.New("forbidden")
	ErrInvalidFormat = errors.New("invalid file format")
	ErrTooLarge      = errors.New("file too large")
	ErrInvalidParam  = errors.New("invalid parameter")
)

// IsClientError reports whether err is caused by the request rather than by a failure of the service.
func IsClientError(err error) bool {
	for _, target := range []error{ErrNotFound, ErrForbidden, ErrInvalidFormat, ErrTooLarge, ErrInvalidParam} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// Outcome is how an operation ended, as reported to monitoring.
type Outcome string

// Outcomes: rejected is a client error such as a bad file, error is a failure of the service.
const (
	OutcomeSuccess  Outcome = "success"
	OutcomeRejected Outcome = "rejected"
	OutcomeError    Outcome = "error"
)

// OutcomeOf classifies the error an operation returned.
func OutcomeOf(err error) Outcome {
	switch {
	case err == nil:
		return OutcomeSuccess
	case IsClientError(err):
		return OutcomeRejected
	default:
		return OutcomeError
	}
}
