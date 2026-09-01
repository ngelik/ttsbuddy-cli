package clerkfapi

import (
	"errors"
	"fmt"
)

type SignInState string

const (
	SignInNeedsFirstFactor SignInState = "needs_first_factor"
	SignInComplete         SignInState = "complete"
)

type Challenge struct {
	SignInID       string
	EmailAddressID string
}

type SessionProof struct {
	Token     string
	SessionID string
}

type flowError struct {
	stage string
	err   error
}

func (e *flowError) Error() string { return e.err.Error() }
func (e *flowError) Unwrap() error { return e.err }

func wrapFlowError(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &flowError{stage: stage, err: err}
}

// FailureStage returns a fixed, non-sensitive development diagnostic label.
// It never includes Clerk response data, account identifiers, or credentials.
func FailureStage(err error) string {
	var flowErr *flowError
	if errors.As(err, &flowErr) {
		return flowErr.stage
	}
	return ""
}

// APIVersion is the pinned Clerk Frontend API contract used by this package.
// Keep this value visible to the development probe so its redacted evidence
// can identify the exact protocol version without exposing request details.
const APIVersion = "2026-05-12"

type RequestError struct {
	StatusCode        int
	RetryAfterSeconds int
	RequestID         string
	Code              string
}

func (e *RequestError) Error() string {
	switch e.StatusCode {
	case 429:
		return "Clerk request was rate limited"
	case 401:
		return "Clerk request was rejected"
	case 403:
		return "Clerk request was forbidden"
	default:
		if e.StatusCode >= 500 {
			return "Clerk request failed"
		}
		return fmt.Sprintf("Clerk request returned status %d", e.StatusCode)
	}
}

type signInResponse struct {
	ID                    string                `json:"id"`
	Status                SignInState           `json:"status"`
	CreatedSessionID      string                `json:"created_session_id"`
	SupportedFirstFactors []firstFactorResponse `json:"supported_first_factors"`
	CurrentTask           *sessionTaskResponse  `json:"current_task"`
	Tasks                 []sessionTaskResponse `json:"tasks"`
}

type firstFactorResponse struct {
	Strategy       string `json:"strategy"`
	EmailAddressID string `json:"email_address_id"`
}

type sessionResponse struct {
	ID          string                `json:"id"`
	Status      string                `json:"status"`
	CurrentTask *sessionTaskResponse  `json:"current_task"`
	Tasks       []sessionTaskResponse `json:"tasks"`
}

type sessionTaskResponse struct {
	Key string `json:"key"`
}

type sessionTokenResponse struct {
	JWT string `json:"jwt"`
}

type clerkEnvelope struct {
	Response  []byte               `json:"-"`
	RequestID string               `json:"request_id"`
	Errors    []clerkErrorResponse `json:"errors"`
	JWT       string               `json:"jwt"`
}

type clerkErrorResponse struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	LongMessage string `json:"long_message"`
}
