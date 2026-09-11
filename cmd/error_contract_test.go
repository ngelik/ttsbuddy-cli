package cmd

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/clerkfapi"
)

func TestClassifyClerkAuthErrorKeepsUnknownProviderDetailPrivate(t *testing.T) {
	err := &clerkfapi.RequestError{StatusCode: http.StatusUnprocessableEntity, Code: "provider_secret_code", RequestID: "req-private"}
	mapped := classifyClerkAuthError(err, false)
	if mapped.msg != "Clerk request returned status 422" || mapped.reason != "AUTH_PROVIDER_ERROR" {
		t.Fatalf("mapped=%#v", mapped)
	}
	if mapped.retryable {
		t.Fatal("unknown 422 should not be retryable")
	}
	if strings.Contains(mapped.msg, "provider_secret_code") || strings.Contains(mapped.msg, "req-private") {
		t.Fatalf("provider detail leaked: %q", mapped.msg)
	}
}

func TestClassifyClerkAuthErrorUsesHTTP429WithoutProviderCode(t *testing.T) {
	mapped := classifyClerkAuthError(&clerkfapi.RequestError{StatusCode: http.StatusTooManyRequests, RetryAfterSeconds: 12}, false)
	if mapped.reason != "RATE_LIMITED" || !mapped.retryable || mapped.retryAfterSeconds != 12 {
		t.Fatalf("mapped=%#v", mapped)
	}
}

func TestClassifyAPIErrorPreservesServerCodeAndRecovery(t *testing.T) {
	retry := 9
	err := &api.APIResponseError{StatusCode: http.StatusTooManyRequests, Response: api.TTSResponse{Error: &api.APIError{Code: api.ErrRateLimited, Message: "provider detail must not leak"}, RetryAfterSeconds: &retry}}
	mapped := classifyAPIError(err, http.StatusTooManyRequests)
	if mapped.serverCode != api.ErrRateLimited || !mapped.retryable || mapped.retryAfterSeconds != retry || mapped.errorCode != "CLI_ERROR" {
		t.Fatalf("mapped=%#v", mapped)
	}
	if errors.Is(mapped, err) {
		t.Fatal("mapped error unexpectedly unwraps provider response")
	}
}
