package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
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

func TestClassifyClerkAuthErrorInvalidInputPreservesStatusAndNextAction(t *testing.T) {
	codes := []string{"form_param_format_invalid", "form_param_missing"}
	statuses := []int{http.StatusBadRequest, http.StatusUnprocessableEntity}
	for _, code := range codes {
		for _, status := range statuses {
			for _, signup := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s-http%d-signup%t", code, status, signup), func(t *testing.T) {
					mapped := classifyClerkAuthError(&clerkfapi.RequestError{
						StatusCode: status,
						Code:       code,
						RequestID:  "provider-request-id",
					}, signup)
					payload := structuredErrorPayload(mapped)
					expectedNext := "ttsbuddy auth email start --email <address> --json"
					if signup {
						expectedNext = "ttsbuddy auth email start --email <address> --signup --json"
					}
					if payload.Error.Reason != "INVALID_INPUT" || payload.Error.NextAction != expectedNext {
						t.Fatalf("payload=%#v, want INVALID_INPUT next_action=%q", payload.Error, expectedNext)
					}
					statusText := fmt.Sprintf("status %d", status)
					if !strings.Contains(mapped.msg, statusText) {
						t.Fatalf("message=%q, want truthful %s", mapped.msg, statusText)
					}
					if status == http.StatusBadRequest && strings.Contains(mapped.msg, "status 422") {
						t.Fatalf("HTTP 400 message retained a hardcoded 422: %q", mapped.msg)
					}
					encoded, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					jsonText := string(encoded)
					if strings.Contains(jsonText, code) || strings.Contains(jsonText, "provider-request-id") {
						t.Fatalf("provider detail leaked: %s", jsonText)
					}
				})
			}
		}
	}
}

func TestClassifyClerkAuthErrorAccountNotFoundHasExactSignupAction(t *testing.T) {
	const expectedNext = "ttsbuddy auth email start --email <address> --signup --json"
	for _, signup := range []bool{false, true} {
		t.Run(fmt.Sprintf("signup%t", signup), func(t *testing.T) {
			mapped := classifyClerkAuthError(&clerkfapi.RequestError{
				StatusCode: http.StatusUnprocessableEntity,
				Code:       "form_identifier_not_found",
				RequestID:  "provider-request-id",
			}, signup)
			payload := structuredErrorPayload(mapped)
			if payload.Error.Reason != "ACCOUNT_NOT_FOUND" || payload.Error.NextAction != expectedNext {
				t.Fatalf("payload=%#v, want reason ACCOUNT_NOT_FOUND next_action=%q", payload.Error, expectedNext)
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			jsonText := string(encoded)
			if strings.Contains(jsonText, "form_identifier_not_found") || strings.Contains(jsonText, "provider-request-id") {
				t.Fatalf("provider detail leaked: %s", jsonText)
			}
		})
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

func TestStructuredRecoveryCarriesEffectiveIdempotencyKey(t *testing.T) {
	mapped := classifyAPIErrorWithKey(errors.New("transport interrupted"), 0, "idem-effective")
	payload := structuredErrorPayload(mapped)
	if payload.Error.IdempotencyKey != "idem-effective" || payload.Error.NextAction == "" {
		t.Fatalf("payload=%#v", payload)
	}
	if payload.Error.Action == nil || payload.Error.Action.Type != actionRetrySubmission {
		t.Fatalf("missing executable retry action: %#v", payload.Error.Action)
	}
	if len(payload.Error.Action.Argv) != 0 || len(payload.Error.Action.RequiredInputs) != 1 || payload.Error.Action.RequiredInputs[0] != "original_input" {
		t.Fatalf("retry action must declare missing input rather than fabricate argv: %#v", payload.Error.Action)
	}
}

func TestStructuredRecoveryCarriesKnownJobDownloadAction(t *testing.T) {
	action := downloadAction("job-known", "/tmp/audio.mp3")
	mapped := structuredExitError(1, "download failed", "CLI_ERROR", "DOWNLOAD_FAILED", "Retry download for job-known.", true, 0)
	mapped.action = action
	payload := structuredErrorPayload(mapped)
	if payload.Error.Action == nil || payload.Error.Action.Type != actionDownload {
		t.Fatalf("missing download action: %#v", payload.Error.Action)
	}
	joined := strings.Join(payload.Error.Action.Argv, " ")
	if !strings.Contains(joined, "job-known") || !strings.Contains(joined, "/tmp/audio.mp3") || strings.Contains(joined, "audio.mp3?") {
		t.Fatalf("unsafe or incomplete download argv: %#v", payload.Error.Action)
	}
}

func TestDownloadActionPreservesStdoutSentinel(t *testing.T) {
	previousJSON := flagJSON
	flagJSON = false
	defer func() { flagJSON = previousJSON }()
	action := downloadAction("job-stdout", "-")
	if action == nil {
		t.Fatal("missing download action")
	}
	joined := strings.Join(action.Argv, " ")
	if strings.Contains(joined, "/-/") || !strings.Contains(joined, "--output -") {
		t.Fatalf("stdout sentinel was rewritten: %#v", action)
	}
}

func TestRecoveryActionsDoNotEmbedFlagLikeJobIDs(t *testing.T) {
	for _, action := range []*api.CLIAction{statusAction("--key=leak"), downloadAction("--key=leak", "/tmp/audio.mp3")} {
		if action == nil || action.Type == "" || len(action.Argv) != 0 || len(action.RequiredInputs) != 1 || action.RequiredInputs[0] != "job_id" {
			t.Fatalf("unsafe provider job ID produced runnable action: %#v", action)
		}
	}
}

func TestVerifyCodeActionDeclaresProtectedInput(t *testing.T) {
	action := verifyCodeAction("challenge-123")
	if action == nil || len(action.Argv) == 0 || len(action.RequiredInputs) != 1 || action.RequiredInputs[0] != "verification_code" {
		t.Fatalf("verify action=%#v", action)
	}
	if strings.Contains(strings.Join(action.Argv, " "), "123456") {
		t.Fatal("verification action must not contain an OTP")
	}
}

func TestTerminalFailureRequiresFreshKeyAndExpiryAction(t *testing.T) {
	failed := classifyTerminalResponse(&api.TTSResponse{Status: "failed", Error: &api.APIError{Code: api.ErrInternalError, Message: "provider secret"}}, "job-1")
	if failed.retryable || !strings.Contains(failed.nextAction, "fresh idempotency key") || strings.Contains(failed.msg, "provider secret") {
		t.Fatalf("failed=%#v", failed)
	}
	expired := classifyTerminalResponse(&api.TTSResponse{Status: "expired", JobID: "job-2"}, "job-2")
	if expired.reason != "AUDIO_EXPIRED" || !strings.Contains(expired.nextAction, "fresh idempotency key") {
		t.Fatalf("expired=%#v", expired)
	}
}
