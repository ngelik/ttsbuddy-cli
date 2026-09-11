package cmd

import (
	"errors"
	"net/http"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/clerkfapi"
)

// structuredExitError keeps human copy and machine recovery metadata together.
// The metadata is intentionally fixed/allowlisted; provider strings are never
// copied into a JSON envelope.
func structuredExitError(code int, message, errorCode, reason, nextAction string, retryable bool, retryAfter int) *exitError {
	return &exitError{
		code:              code,
		msg:               message,
		errorCode:         errorCode,
		reason:            reason,
		nextAction:        nextAction,
		retryable:         retryable,
		retryAfterSeconds: retryAfter,
	}
}

func structuredErrorPayload(err *exitError) api.CLIError {
	if err == nil {
		return api.NewCLIError("CLI_ERROR", "command failed")
	}
	code := err.errorCode
	if code == "" {
		code = "CLI_ERROR"
	}
	payload := api.NewCLIErrorWithRecovery(code, err.msg, err.reason, err.nextAction, err.retryable, err.retryAfterSeconds)
	payload.Error.ServerCode = err.serverCode
	payload.Error.Action = err.action
	payload.Error.HumanActionRequired = err.reason == "BROWSER_AUTH_REQUIRED"
	payload.Error.IdempotencyKey = err.idempotencyKey
	return payload
}

func classifyClerkAuthError(err error, signup bool) *exitError {
	if err == nil {
		return structuredExitError(1, "email authentication failed", "AUTH_ERROR", "AUTH_FAILED", authMethodSuggestion, false, 0)
	}
	code := clerkfapi.FailureCode(err)
	message := "email authentication failed. " + authMethodSuggestion
	next := authMethodSuggestion
	reason := "AUTH_FAILED"
	retryable := false
	var providerRetryAfter int
	var requestErr *clerkfapi.RequestError
	if errors.As(err, &requestErr) && requestErr.RetryAfterSeconds >= 1 && requestErr.RetryAfterSeconds <= 300 {
		providerRetryAfter = requestErr.RetryAfterSeconds
	}
	if requestErr != nil {
		// RequestError.Error is a fixed, provider-safe status phrase. Preserve
		// that useful context for unknown 4xx responses without exposing Clerk
		// response bodies or account identifiers.
		message = requestErr.Error()
		reason = "AUTH_PROVIDER_ERROR"
	}
	switch {
	case clerkfapi.IsSignupEmailExists(err):
		message = "An account already exists for this email. " + authMethodSuggestion + ". For structured login, run: ttsbuddy auth email start --email <address> --json"
		reason = "ACCOUNT_ALREADY_EXISTS"
		next = "ttsbuddy auth email start --email <address> --json"
	case clerkfapi.IsSignupBrowserFallback(err):
		message = "This signup needs a browser step. Run: ttsbuddy auth browser"
		reason = "BROWSER_AUTH_REQUIRED"
		next = "ttsbuddy auth browser"
	case clerkfapi.IsBrowserAuthRequired(err):
		message = "This sign-in needs a browser step. Run: ttsbuddy auth browser"
		reason = "BROWSER_AUTH_REQUIRED"
		next = "ttsbuddy auth browser"
	case clerkfapi.IsEmailCodeExpired(err):
		message = "The email code expired. Start a new email authentication challenge."
		reason = "CODE_EXPIRED"
		if signup {
			next = "ttsbuddy auth email start --email <address> --signup --json"
		} else {
			next = "ttsbuddy auth email start --email <address> --json"
		}
	case clerkfapi.IsEmailCodeIncorrect(err):
		message = "The email code was incorrect. Retry the same challenge, or start a new one if needed."
		reason = "INVALID_CODE"
		retryable = true
		next = "ttsbuddy auth email verify --challenge-id <id> --code-stdin --json"
	case code == "form_email_address_blocked":
		message = signupEmailAddressBlockedMessage + ". Or run: ttsbuddy auth browser"
		reason = "EMAIL_ADDRESS_NOT_ACCEPTED"
	case code == "form_param_format_invalid" || code == "form_param_missing":
		message = "Clerk request returned status 422: the supplied authentication input was not accepted. Check the provided values and try again."
		reason = "INVALID_INPUT"
		if signup {
			next = "ttsbuddy auth email start --email <address> --signup --json"
		} else {
			next = "ttsbuddy auth email start --email <address> --json"
		}
	case code == "form_identifier_not_found":
		message = "No TTS Buddy account was found for this email. To create one, run: ttsbuddy auth email --signup. Structured automation: ttsbuddy auth email start --email <address> --signup --json"
		reason = "ACCOUNT_NOT_FOUND"
		next = "ttsbuddy auth email start --email <address> --signup --json"
	case code == "form_identifier_exists" || code == "identifier_exists" || code == "email_address_exists" || code == "email_exists":
		message = "An account already exists for this email. " + authMethodSuggestion + ". For structured login, run: ttsbuddy auth email start --email <address> --json"
		reason = "ACCOUNT_ALREADY_EXISTS"
		next = "ttsbuddy auth email start --email <address> --json"
	case code == "form_code_incorrect" || code == "form_code_invalid":
		message = "The email code was incorrect. Retry the same challenge, or start a new one if needed."
		reason = "INVALID_CODE"
		retryable = true
		next = "ttsbuddy auth email verify --challenge-id <id> --code-stdin --json"
	case code == "form_code_expired":
		message = "The email code expired. Start a new email authentication challenge."
		reason = "CODE_EXPIRED"
		if signup {
			next = "ttsbuddy auth email start --email <address> --signup --json"
		} else {
			next = "ttsbuddy auth email start --email <address> --json"
		}
	case code == "captcha_required" || code == "legal_acceptance_required" || code == "legal_accepted_required" || code == "mfa_required" || code == "multi_factor_required" || code == "second_factor_required" || code == "needs_second_factor" || code == "client_trust_required" || code == "needs_client_trust" || code == "new_password_required" || code == "needs_new_password":
		message = "This sign-in needs a browser step. Run: ttsbuddy auth browser"
		reason = "BROWSER_AUTH_REQUIRED"
		next = "ttsbuddy auth browser"
	case code == "rate_limit_exceeded" || code == "too_many_requests":
		message = "Clerk rate limited this request. Wait before trying again."
		reason = "RATE_LIMITED"
		retryable = true
		next = "Wait for the provided retry delay or bounded backoff, then retry the authentication command."
	case requestErr != nil && requestErr.StatusCode == http.StatusTooManyRequests:
		message = "Clerk rate limited this request. Wait before trying again."
		reason = "RATE_LIMITED"
		retryable = true
		next = "Wait for the provided retry delay or bounded backoff, then retry the authentication command."
	case requestErr != nil && requestErr.StatusCode >= 500:
		message = "Clerk authentication is temporarily unavailable. Try again later."
		reason = "SERVICE_UNAVAILABLE"
		retryable = true
		next = "Retry the same authentication command later."
	default:
		// Do not expose unknown provider response text or account details.
	}
	mapped := structuredExitError(1, message, "AUTH_ERROR", reason, next, retryable, providerRetryAfter)
	switch reason {
	case "BROWSER_AUTH_REQUIRED":
		mapped.action = browserAuthAction()
	case "INVALID_CODE":
		mapped.action = verifyCodeAction("")
	case "ACCOUNT_NOT_FOUND", "ACCOUNT_ALREADY_EXISTS", "EMAIL_ADDRESS_NOT_ACCEPTED", "INVALID_INPUT":
		mapped.action = authenticateAction()
	default:
		if retryable {
			mapped.action = authenticateAction()
		}
	}
	return mapped
}

func classifyCLIAuthHTTPError(err error, action string) *exitError {
	var httpErr *api.CLIAuthHTTPError
	if !errors.As(err, &httpErr) {
		return structuredExitError(1, "CLI authentication request failed", "CLI_AUTH_ERROR", "TRANSPORT_ERROR", action, true, 0)
	}
	message := "CLI authentication request failed. Try again."
	reason := "CLI_AUTH_ERROR"
	next := action
	retryable := false
	switch httpErr.StatusCode {
	case http.StatusUnauthorized:
		message = "CLI session is no longer valid. " + authMethodSuggestion
		reason = "SESSION_REJECTED"
		next = authMethodSuggestion
	case http.StatusTooManyRequests:
		message = "CLI authentication is rate limited. Wait before trying again."
		reason = "RATE_LIMITED"
		retryable = true
		next = "Wait for the provided retry delay or bounded backoff, then retry the authentication command."
	case http.StatusServiceUnavailable:
		message = "CLI authentication is temporarily unavailable. Try again later."
		reason = "SERVICE_UNAVAILABLE"
		retryable = true
		next = "Retry the authentication command later."
	}
	mapped := structuredExitError(1, message, "CLI_AUTH_ERROR", reason, next, retryable, httpErr.RetryAfterSeconds)
	if reason == "SESSION_REJECTED" {
		mapped.action = authenticateAction()
	} else if retryable {
		mapped.action = authenticateAction()
	}
	return mapped
}

func classifyAPIError(err error, status int) *exitError {
	return classifyAPIErrorWithKey(err, status, "")
}

func classifyAPIErrorWithKey(err error, status int, idempotencyKey string) *exitError {
	var apiErr *api.APIResponseError
	if !errors.As(err, &apiErr) {
		mapped := structuredExitError(1, "API request failed", "CLI_ERROR", "TRANSPORT_ERROR", "Retry the same request with --idempotency-key <same-value> if it was not accepted.", true, 0)
		mapped.idempotencyKey = idempotencyKey
		mapped.action = submissionRetryAction()
		return mapped
	}
	code := apiErr.ErrorCode()
	message := "API request failed"
	reason := "API_ERROR"
	next := ""
	retryable := false
	retryAfter := 0
	if apiErr.Response.RetryAfterSeconds != nil {
		retryAfter = *apiErr.Response.RetryAfterSeconds
	}
	switch code {
	case api.ErrInvalidKey:
		message = "invalid credential. " + authMethodSuggestion + ". For automation with a permanent API key, use: ttsbuddy config set key <your-key>"
		reason = "SESSION_REJECTED"
		next = authMethodSuggestion
	case api.ErrInactiveSubscription:
		message = "subscription inactive. Reactivate at https://ttsbuddy.com/billing"
		reason = "SUBSCRIPTION_INACTIVE"
		next = "https://ttsbuddy.com/billing"
	case api.ErrNoAPIAccess:
		message = "your plan does not include API access. Check your plan or contact support."
		reason = "API_ACCESS_UNAVAILABLE"
		next = "https://ttsbuddy.com/billing"
	case api.ErrUsageLimitExceeded:
		message = "monthly TTS minutes exhausted. Upgrade at https://ttsbuddy.com/billing"
		reason = "QUOTA_EXCEEDED"
		next = "https://ttsbuddy.com/billing"
		if apiErr.Response.Error != nil {
			if details, ok := apiErr.Response.Error.Details.(map[string]interface{}); ok {
				if upgradeURL, ok := details["upgrade_url"].(string); ok && upgradeURL != "" {
					message = "monthly TTS minutes exhausted. Upgrade at " + upgradeURL
					next = upgradeURL
				}
			}
		}
	case api.ErrTextTooLong:
		message = "input exceeds 500,000 characters. Split into smaller chunks."
		reason = "INPUT_TOO_LONG"
		next = "Split the supplied text, then retry with a fresh idempotency key."
	case api.ErrInvalidRequest:
		message = "the TTS request was not accepted. Check the supplied input and options."
		reason = "INVALID_INPUT"
		next = "Correct the supplied input and options, then retry with a fresh idempotency key."
	case api.ErrRateLimited:
		message = "rate limited. Please wait and try again."
		reason = "RATE_LIMITED"
		retryable = true
		next = "Wait for the provided retry delay or bounded backoff, then retry with --idempotency-key <same-value>."
	case api.ErrNotFound:
		message = "job not found. Check the job ID."
		reason = "JOB_NOT_FOUND"
	case api.ErrFileExpired:
		message = "audio file has expired. Submit a new request."
		reason = "AUDIO_EXPIRED"
		next = "Submit a new request with a fresh idempotency key."
	case api.ErrTTSProviderError, api.ErrInternalError:
		message = "TTS service error. Try again later."
		reason = "SERVICE_ERROR"
		retryable = true
		next = "Retry the same request with --idempotency-key <same-value>."
	case api.ErrForbidden:
		message = "access denied (HTTP 403). Check your subscription and API access at https://ttsbuddy.com/billing"
		reason = "API_ACCESS_UNAVAILABLE"
		next = "https://ttsbuddy.com/billing"
	default:
		if status == http.StatusTooManyRequests {
			message = "rate limited. Please wait and try again."
			reason = "RATE_LIMITED"
			retryable = true
			next = "Wait for the provided retry delay or bounded backoff, then retry with --idempotency-key <same-value>."
		} else if status >= 500 {
			message = "TTS service error. Try again later."
			reason = "SERVICE_ERROR"
			retryable = true
			next = "Retry the same request with --idempotency-key <same-value>."
		}
	}
	if apiErr.Response.Error != nil && api.NeedsNewIdempotencyKey(apiErr.Response.Error) {
		next = "Submit again with a fresh idempotency key (for example: --idempotency-key <new-value>)."
		retryable = false
	}
	exitCode := 1
	if code == api.ErrTextTooLong {
		exitCode = 2
	}
	mapped := structuredExitError(exitCode, message, "CLI_ERROR", reason, next, retryable, retryAfter)
	mapped.serverCode = code
	mapped.idempotencyKey = idempotencyKey
	switch reason {
	case "SESSION_REJECTED":
		mapped.action = authenticateAction()
	case "SUBSCRIPTION_INACTIVE", "API_ACCESS_UNAVAILABLE", "QUOTA_EXCEEDED":
		mapped.action = accountAction("https://ttsbuddy.com/billing")
	case "INPUT_TOO_LONG", "INVALID_INPUT":
		if idempotencyKey != "" {
			mapped.action = inputCorrectionAction()
		}
	case "RATE_LIMITED", "SERVICE_ERROR", "TRANSPORT_ERROR":
		if idempotencyKey != "" {
			mapped.action = submissionRetryAction()
		}
	}
	if apiErr.Response.Error != nil && api.NeedsNewIdempotencyKey(apiErr.Response.Error) {
		// The provider definitively rejected this identity; do not suggest
		// replaying it or expose it as an ambiguous recovery key.
		mapped.idempotencyKey = ""
	}
	if !mapped.retryable {
		mapped.idempotencyKey = ""
	}
	return mapped
}
