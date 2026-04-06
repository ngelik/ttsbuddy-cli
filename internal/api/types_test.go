package api

import "testing"

func TestIsRetryableAll(t *testing.T) {
	cases := []struct {
		code   string
		status int
		want   bool
	}{
		{ErrRateLimited, 429, true},
		{ErrInternalError, 500, true},
		{ErrTTSProviderError, 502, true},
		{ErrInvalidKey, 401, false},
		{ErrInactiveSubscription, 403, false},
		{ErrNoAPIAccess, 403, false},
		{ErrUsageLimitExceeded, 403, false},
		{ErrInvalidRequest, 400, false},
		{ErrTextTooLong, 400, false},
		{ErrNotFound, 404, false},
	}
	for _, tc := range cases {
		got := IsRetryable(tc.code, tc.status)
		if got != tc.want {
			t.Errorf("IsRetryable(%q, %d) = %v, want %v", tc.code, tc.status, got, tc.want)
		}
	}
}

func TestNeedsNewIdempotencyKeyAll(t *testing.T) {
	cases := []struct {
		err  *APIError
		want bool
	}{
		{nil, false},
		{&APIError{Message: "some error"}, false},
		{&APIError{Message: "failed. Use a new Idempotency-Key."}, true},
		{&APIError{Message: "Provider failed. Use a new Idempotency-Key to retry."}, true},
		{&APIError{Message: "new Idempotency-Key"}, true},
	}
	for _, tc := range cases {
		got := NeedsNewIdempotencyKey(tc.err)
		if got != tc.want {
			msg := "<nil>"
			if tc.err != nil {
				msg = tc.err.Message
			}
			t.Errorf("NeedsNewIdempotencyKey(%q) = %v, want %v", msg, got, tc.want)
		}
	}
}

func TestStatusToErrorCode(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{401, ErrInvalidKey},
		{403, ErrForbidden},
		{404, ErrNotFound},
		{429, ErrRateLimited},
		{500, ErrInternalError},
		{502, ErrInternalError},
		{503, ErrInternalError},
		{400, ErrInvalidRequest},
		{418, ErrInvalidRequest}, // unknown 4xx
	}
	for _, tc := range cases {
		got := statusToErrorCode(tc.status)
		if got != tc.want {
			t.Errorf("statusToErrorCode(%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestNewCLIError(t *testing.T) {
	e := NewCLIError("TEST_CODE", "test message")
	if e.Success {
		t.Error("Success should be false")
	}
	if e.Error.Code != "TEST_CODE" {
		t.Errorf("Code: got %q, want %q", e.Error.Code, "TEST_CODE")
	}
	if e.Error.Message != "test message" {
		t.Errorf("Message: got %q, want %q", e.Error.Message, "test message")
	}
}

func TestAPIResponseErrorMessage(t *testing.T) {
	// With error details
	e := &APIResponseError{
		StatusCode: 401,
		Response: TTSResponse{
			Error: &APIError{Code: ErrInvalidKey, Message: "bad key"},
		},
	}
	msg := e.Error()
	if msg != "API error 401: [INVALID_KEY] bad key" {
		t.Errorf("got %q", msg)
	}
	if e.ErrorCode() != ErrInvalidKey {
		t.Errorf("ErrorCode: got %q", e.ErrorCode())
	}

	// Without error details
	e2 := &APIResponseError{StatusCode: 500, Response: TTSResponse{}}
	if e2.Error() != "API error 500" {
		t.Errorf("got %q", e2.Error())
	}
	if e2.ErrorCode() != "" {
		t.Errorf("ErrorCode should be empty, got %q", e2.ErrorCode())
	}
}
