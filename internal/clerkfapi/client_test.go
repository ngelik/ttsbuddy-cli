package clerkfapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func testIssuedEmailCode() string {
	return strings.Join([]string{"654", "321"}, "")
}

func testIncorrectEmailCode() string {
	return strings.Join([]string{"bad", "otp"}, "")
}

func testExpiredEmailCode() string {
	return strings.Join([]string{"old", "otp"}, "")
}

func TestNewRejectsNonOriginFrontendAPIURL(t *testing.T) {
	tests := []string{
		"https://clerk.ttsbuddy.com/native",
		"https://clerk.ttsbuddy.com?x=1",
		"https://clerk.ttsbuddy.com#frag",
		"https://user:pass@clerk.ttsbuddy.com",
		"mailto:clerk.ttsbuddy.com",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			_, err := New(input, "test")
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "invalid Clerk Frontend API URL") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	client, err := New("HTTPS://CLERK.TTSBUDDY.COM", "test")
	if err != nil {
		t.Fatalf("expected pure origin to be accepted: %v", err)
	}
	if client.frontendAPIURL != "https://clerk.ttsbuddy.com" {
		t.Fatalf("frontendAPIURL = %q, want %q", client.frontendAPIURL, "https://clerk.ttsbuddy.com")
	}

	withSlash, err := New("https://clerk.ttsbuddy.com/", "test")
	if err != nil {
		t.Fatalf("expected trailing slash origin to be accepted: %v", err)
	}
	if withSlash.frontendAPIURL != "https://clerk.ttsbuddy.com" {
		t.Fatalf("frontendAPIURL = %q, want %q", withSlash.frontendAPIURL, "https://clerk.ttsbuddy.com")
	}
}

func TestStartEmailCodeCreatesNativeClientAndPreparesChallenge(t *testing.T) {
	srv := newScriptedServer(t, []scriptedResponse{
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client", "", map[string]string{}),
			cookies:  []http.Cookie{{Name: "__client", Value: "client-1"}},
			bodyJSON: map[string]any{
				"request_id": "req_client",
				"response":   map[string]any{"id": "client_123"},
			},
		},
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client/sign_ins", "client-1", map[string]string{
				"identifier": "person@example.com",
			}),
			cookies: []http.Cookie{{Name: "__client", Value: "client-2"}},
			bodyJSON: map[string]any{
				"request_id": "req_sign_in",
				"response": map[string]any{
					"id":     "si_123",
					"status": string(SignInNeedsFirstFactor),
					"supported_first_factors": []map[string]any{
						{
							"strategy":         "email_code",
							"email_address_id": "idn_123",
						},
					},
				},
			},
		},
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client/sign_ins/si_123/prepare_first_factor", "client-2", map[string]string{
				"strategy":         "email_code",
				"email_address_id": "idn_123",
			}),
			cookies: []http.Cookie{{Name: "__client", Value: "client-3"}},
			bodyJSON: map[string]any{
				"request_id": "req_prepare",
				"response": map[string]any{
					"id":     "si_123",
					"status": string(SignInNeedsFirstFactor),
				},
			},
		},
	})
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	challenge, err := client.StartEmailCode(context.Background(), "person@example.com")
	if err != nil {
		t.Fatalf("StartEmailCode() error: %v", err)
	}
	if challenge.SignInID != "si_123" {
		t.Fatalf("SignInID = %q, want %q", challenge.SignInID, "si_123")
	}
	if challenge.EmailAddressID != "idn_123" {
		t.Fatalf("EmailAddressID = %q, want %q", challenge.EmailAddressID, "idn_123")
	}
	if client.nativeClientToken != "client-3" {
		t.Fatalf("nativeClientToken = %q, want %q", client.nativeClientToken, "client-3")
	}
	if got := client.RequestIDs(); len(got) != 3 || got[0] != "req_client" || got[2] != "req_prepare" {
		t.Fatalf("RequestIDs() = %v, want three ordered ids", got)
	}
}

func TestStartEmailCodeRejectsMissingNativeClientToken(t *testing.T) {
	srv := newScriptedServer(t, []scriptedResponse{
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client", "", map[string]string{}),
			bodyJSON: map[string]any{"response": map[string]any{"id": "client_123"}},
		},
	})
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	_, err = client.StartEmailCode(context.Background(), "person@example.com")
	if err == nil {
		t.Fatal("expected error for missing native client token")
	}
	if !strings.Contains(err.Error(), "native client token") {
		t.Fatalf("expected native client token error, got %v", err)
	}
}

func TestCaptureNativeClientTokenAcceptsRawAuthorizationHeader(t *testing.T) {
	client, err := New("https://clerk.ttsbuddy.com", "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if !client.captureNativeClientToken(&http.Response{Header: http.Header{
		"Authorization": []string{"client-raw"},
	}}) {
		t.Fatal("captureNativeClientToken() = false, want true")
	}
	if client.nativeClientToken != "client-raw" {
		t.Fatalf("nativeClientToken = %q, want %q", client.nativeClientToken, "client-raw")
	}

	if !client.captureNativeClientToken(&http.Response{Header: http.Header{
		"Authorization": []string{"Bearer client-bearer"},
	}}) {
		t.Fatal("captureNativeClientToken() bearer = false, want true")
	}
	if client.nativeClientToken != "client-bearer" {
		t.Fatalf("nativeClientToken = %q, want %q", client.nativeClientToken, "client-bearer")
	}

	if client.captureNativeClientToken(&http.Response{Header: http.Header{
		"Authorization": []string{"Basic client-invalid"},
	}}) {
		t.Fatal("captureNativeClientToken() accepted a non-bearer scheme")
	}
}

func TestFailureStageUnwrapsWithoutChangingPublicError(t *testing.T) {
	base := &RequestError{StatusCode: http.StatusUnprocessableEntity, Code: "form_code_incorrect"}
	err := wrapFlowError("attempt_first_factor", base)

	if got := FailureStage(err); got != "attempt_first_factor" {
		t.Fatalf("FailureStage() = %q, want %q", got, "attempt_first_factor")
	}
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr != base {
		t.Fatalf("wrapped error did not preserve RequestError: %v", err)
	}
	if err.Error() != base.Error() {
		t.Fatalf("wrapped error text = %q, want %q", err.Error(), base.Error())
	}
	if got := FailureStage(errors.New("plain")); got != "" {
		t.Fatalf("FailureStage(plain) = %q, want empty", got)
	}
}

func TestRequestsPreserveNativeClientTokenWhenResponseOmitsRotation(t *testing.T) {
	var seenAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		if len(seenAuth) == 1 {
			w.Header().Set("Authorization", "client-raw")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"status": string(SignInNeedsFirstFactor)}})
	}))
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if _, err := client.doRequest(context.Background(), http.MethodPost, "/v1/client", nil, true); err != nil {
		t.Fatalf("initial request error: %v", err)
	}
	if _, err := client.doRequest(context.Background(), http.MethodPost, "/v1/client/sign_ins", nil, false); err != nil {
		t.Fatalf("rotation-optional request error: %v", err)
	}
	if got, want := strings.Join(seenAuth, ","), ",Bearer client-raw"; got != want {
		t.Fatalf("request Authorization values = %q, want %q", got, want)
	}
}

func TestStartEmailCodeRejectsPendingSignInTask(t *testing.T) {
	srv := newScriptedServer(t, []scriptedResponse{
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client", "", map[string]string{}),
			cookies:  []http.Cookie{{Name: "__client", Value: "client-1"}},
			bodyJSON: map[string]any{"request_id": "req_client", "response": map[string]any{"id": "client_123"}},
		},
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client/sign_ins", "client-1", map[string]string{
				"identifier": "person@example.com",
			}),
			cookies: []http.Cookie{{Name: "__client", Value: "client-2"}},
			bodyJSON: map[string]any{"response": map[string]any{
				"id": "si_123", "status": string(SignInNeedsFirstFactor),
				"current_task": map[string]any{"key": "verify_email_address"},
			}},
		},
	})
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	_, err = client.StartEmailCode(context.Background(), "person@example.com")
	if err == nil || err.Error() != "unable to start Clerk email sign-in" {
		t.Fatalf("expected generic pending-state error, got %v", err)
	}
}

func TestStartEmailCodeDoesNotRevealAccountStateWhenEmailFactorIsUnavailable(t *testing.T) {
	srv := newScriptedServer(t, []scriptedResponse{
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client", "", map[string]string{}),
			cookies:  []http.Cookie{{Name: "__client", Value: "client-1"}},
			bodyJSON: map[string]any{"request_id": "req_client", "response": map[string]any{"id": "client_123"}},
		},
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client/sign_ins", "client-1", map[string]string{
				"identifier": "unknown@example.com",
			}),
			cookies: []http.Cookie{{Name: "__client", Value: "client-2"}},
			bodyJSON: map[string]any{"response": map[string]any{
				"id":     "si_123",
				"status": string(SignInNeedsFirstFactor),
			}},
		},
	})
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	_, err = client.StartEmailCode(context.Background(), "unknown@example.com")
	if err == nil {
		t.Fatal("expected unavailable-factor error")
	}
	if got := err.Error(); got != "unable to start Clerk email sign-in" {
		t.Fatalf("unexpected account-state error: %q", got)
	}
	for _, secret := range []string{"unknown@example.com", "email_code", "client-1", "client-2"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked account/protocol detail %q: %v", secret, err)
		}
	}
}

func TestStartEmailCodeRejectsPreparedStateChanges(t *testing.T) {
	srv := newScriptedServer(t, []scriptedResponse{
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client", "", map[string]string{}),
			cookies:  []http.Cookie{{Name: "__client", Value: "client-1"}},
			bodyJSON: map[string]any{"response": map[string]any{"id": "client_123"}},
		},
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client/sign_ins", "client-1", map[string]string{
				"identifier": "person@example.com",
			}),
			cookies: []http.Cookie{{Name: "__client", Value: "client-2"}},
			bodyJSON: map[string]any{"response": map[string]any{
				"id":     "si_123",
				"status": string(SignInNeedsFirstFactor),
				"supported_first_factors": []map[string]any{{
					"strategy":         "email_code",
					"email_address_id": "idn_123",
				}},
			}},
		},
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client/sign_ins/si_123/prepare_first_factor", "client-2", map[string]string{
				"strategy":         "email_code",
				"email_address_id": "idn_123",
			}),
			cookies: []http.Cookie{{Name: "__client", Value: "client-3"}},
			bodyJSON: map[string]any{"response": map[string]any{
				"id":     "si_123",
				"status": "needs_second_factor",
			}},
		},
	})
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	_, err = client.StartEmailCode(context.Background(), "person@example.com")
	if err == nil || err.Error() != "unable to start Clerk email sign-in" {
		t.Fatalf("expected generic prepared-state error, got %v", err)
	}
}

func TestVerifyEmailCodeRejectsInvalidChallengeBeforeNetwork(t *testing.T) {
	client, err := New("https://clerk.ttsbuddy.com", "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	for _, tt := range []struct {
		name      string
		challenge Challenge
		code      string
	}{
		{name: "missing sign in id", challenge: Challenge{EmailAddressID: "idn_123"}, code: testIssuedEmailCode()},
		{name: "missing code", challenge: Challenge{SignInID: "si_123"}, code: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.VerifyEmailCode(context.Background(), tt.challenge, tt.code)
			if err == nil || !strings.Contains(err.Error(), "invalid Clerk email sign-in challenge") {
				t.Fatalf("expected invalid challenge error, got %v", err)
			}
		})
	}
}

func TestStartEmailCodePreservesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validateFormRequest(http.MethodPost, "/v1/client", "", map[string]string{})(t, r)
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"code": "rate_limited"}},
		})
	}))
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	_, err = client.StartEmailCode(context.Background(), "person@example.com")
	if err == nil {
		t.Fatal("expected error")
	}
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
	if reqErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("StatusCode = %d, want %d", reqErr.StatusCode, http.StatusTooManyRequests)
	}
	if reqErr.RetryAfterSeconds != 17 {
		t.Fatalf("RetryAfterSeconds = %d, want 17", reqErr.RetryAfterSeconds)
	}
}

func TestStartEmailCodePreservesRetryAfterForMalformedErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validateFormRequest(http.MethodPost, "/v1/client", "", map[string]string{})(t, r)
		w.Header().Set("Retry-After", "23")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "not-json")
	}))
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	_, err = client.StartEmailCode(context.Background(), "person@example.com")
	if err == nil {
		t.Fatal("expected error")
	}
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
	if reqErr.StatusCode != http.StatusTooManyRequests || reqErr.RetryAfterSeconds != 23 {
		t.Fatalf("unexpected rate-limit details: %+v", reqErr)
	}
}

func TestStartEmailCodeRejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validateFormRequest(http.MethodPost, "/v1/client", "", map[string]string{})(t, r)
		http.SetCookie(w, &http.Cookie{Name: "__client", Value: "client-1"})
		_, _ = io.WriteString(w, strings.Repeat("a", maxResponseBodyBytes+1))
	}))
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	_, err = client.StartEmailCode(context.Background(), "person@example.com")
	if err == nil {
		t.Fatal("expected oversized response error")
	}
	if !strings.Contains(err.Error(), "response too large") {
		t.Fatalf("expected oversized response error, got %v", err)
	}
}

func TestStartEmailCodeHonorsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validateFormRequest(http.MethodPost, "/v1/client", "", map[string]string{})(t, r)
		<-r.Context().Done()
	}))
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, runErr := client.StartEmailCode(ctx, "person@example.com")
		done <- runErr
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	err = <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestStartEmailCodeAllowsSameOriginRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/client":
			validateFormRequest(http.MethodPost, "/v1/client", "", map[string]string{})(t, r)
			http.SetCookie(w, &http.Cookie{Name: "__client", Value: "client-1"})
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"id": "client_123"}})
		case "/v1/client/sign_ins":
			http.Redirect(w, r, "/v1/client/sign_ins/final", http.StatusTemporaryRedirect)
		case "/v1/client/sign_ins/final":
			validateFormRequest(http.MethodPost, "/v1/client/sign_ins/final", "client-1", map[string]string{
				"identifier": "person@example.com",
			})(t, r)
			http.SetCookie(w, &http.Cookie{Name: "__client", Value: "client-2"})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"response": map[string]any{
					"id":     "si_123",
					"status": string(SignInNeedsFirstFactor),
					"supported_first_factors": []map[string]any{{
						"strategy":         "email_code",
						"email_address_id": "idn_123",
					}},
				},
			})
		case "/v1/client/sign_ins/si_123/prepare_first_factor":
			validateFormRequest(http.MethodPost, "/v1/client/sign_ins/si_123/prepare_first_factor", "client-2", map[string]string{
				"strategy":         "email_code",
				"email_address_id": "idn_123",
			})(t, r)
			http.SetCookie(w, &http.Cookie{Name: "__client", Value: "client-3"})
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"status": string(SignInNeedsFirstFactor)}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if _, err := client.StartEmailCode(context.Background(), "person@example.com"); err != nil {
		t.Fatalf("StartEmailCode() error: %v", err)
	}
}

func TestStartEmailCodeRejectsCrossOriginRedirect(t *testing.T) {
	var targetHits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits++
		http.SetCookie(w, &http.Cookie{Name: "__client", Value: "target-token"})
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"id": "client_456"}})
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client, err := New(redirector.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	_, err = client.StartEmailCode(context.Background(), "person@example.com")
	if err == nil {
		t.Fatal("expected cross-origin redirect error")
	}
	if !strings.Contains(err.Error(), "cross-origin") {
		t.Fatalf("expected cross-origin redirect error, got %v", err)
	}
	if targetHits != 0 {
		t.Fatalf("expected target not to be reached, got %d hits", targetHits)
	}
}

func TestVerifyEmailCodeMintsSessionProofAndRotatesToken(t *testing.T) {
	srv := newScriptedServer(t, []scriptedResponse{
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client", "", map[string]string{}),
			cookies:  []http.Cookie{{Name: "__client", Value: "client-1"}},
			bodyJSON: map[string]any{"request_id": "req_client", "response": map[string]any{"id": "client_123"}},
		},
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client/sign_ins", "client-1", map[string]string{
				"identifier": "person@example.com",
			}),
			cookies: []http.Cookie{{Name: "__client", Value: "client-2"}},
			bodyJSON: map[string]any{
				"request_id": "req_sign_in",
				"response": map[string]any{
					"id":     "si_123",
					"status": string(SignInNeedsFirstFactor),
					"supported_first_factors": []map[string]any{{
						"strategy":         "email_code",
						"email_address_id": "idn_123",
					}},
				},
			},
		},
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client/sign_ins/si_123/prepare_first_factor", "client-2", map[string]string{
				"strategy":         "email_code",
				"email_address_id": "idn_123",
			}),
			cookies:  []http.Cookie{{Name: "__client", Value: "client-3"}},
			bodyJSON: map[string]any{"request_id": "req_prepare", "response": map[string]any{"status": string(SignInNeedsFirstFactor)}},
		},
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client/sign_ins/si_123/attempt_first_factor", "client-3", map[string]string{
				"strategy": "email_code",
				"code":     testIssuedEmailCode(),
			}),
			cookies: []http.Cookie{{Name: "__client", Value: "client-4"}},
			bodyJSON: map[string]any{
				"request_id": "req_attempt",
				"response": map[string]any{
					"id":                 "si_123",
					"status":             string(SignInComplete),
					"created_session_id": "sess_123",
				},
			},
		},
		{
			validate: validateRequest(http.MethodGet, "/v1/client/sessions/sess_123", "client-4"),
			cookies:  []http.Cookie{{Name: "__client", Value: "client-5"}},
			bodyJSON: map[string]any{
				"request_id": "req_session",
				"response": map[string]any{
					"id":     "sess_123",
					"status": "active",
				},
			},
		},
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client/sessions/sess_123/tokens", "client-5", map[string]string{}),
			cookies:  []http.Cookie{{Name: "__client", Value: "client-6"}},
			bodyJSON: map[string]any{
				"request_id": "req_token",
				"jwt":        "jwt_session_token",
			},
		},
	})
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	challenge, err := client.StartEmailCode(context.Background(), "person@example.com")
	if err != nil {
		t.Fatalf("StartEmailCode() error: %v", err)
	}

	proof, err := client.VerifyEmailCode(context.Background(), *challenge, testIssuedEmailCode())
	if err != nil {
		t.Fatalf("VerifyEmailCode() error: %v", err)
	}
	if proof.SessionID != "sess_123" {
		t.Fatalf("SessionID = %q, want %q", proof.SessionID, "sess_123")
	}
	if proof.Token != "jwt_session_token" {
		t.Fatalf("Token = %q, want %q", proof.Token, "jwt_session_token")
	}
	if client.nativeClientToken != "client-6" {
		t.Fatalf("nativeClientToken = %q, want %q", client.nativeClientToken, "client-6")
	}
	if client.createdSessionID != "sess_123" {
		t.Fatalf("createdSessionID = %q, want %q", client.createdSessionID, "sess_123")
	}
	if got := client.RequestIDs(); len(got) != 6 || got[0] != "req_client" || got[5] != "req_token" {
		t.Fatalf("RequestIDs() = %v, want six ids from successful token flow", got)
	}
}

func TestVerifyEmailCodeRejectsUnexpectedStates(t *testing.T) {
	tests := []struct {
		name     string
		response map[string]any
		wantText string
	}{
		{
			name: "needs second factor",
			response: map[string]any{
				"response": map[string]any{"status": "needs_second_factor"},
			},
			wantText: "needs_second_factor",
		},
		{
			name: "needs client trust",
			response: map[string]any{
				"response": map[string]any{"status": "needs_client_trust"},
			},
			wantText: "needs_client_trust",
		},
		{
			name: "needs new password",
			response: map[string]any{
				"response": map[string]any{"status": "needs_new_password"},
			},
			wantText: "needs_new_password",
		},
		{
			name: "needs protect check",
			response: map[string]any{
				"response": map[string]any{"status": "needs_protect_check"},
			},
			wantText: "needs_protect_check",
		},
		{
			name: "unknown future state",
			response: map[string]any{
				"response": map[string]any{"status": "needs_spaceship_factor"},
			},
			wantText: "needs_spaceship_factor",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				validateFormRequest(http.MethodPost, "/v1/client/sign_ins/si_123/attempt_first_factor", "client-3", map[string]string{
					"strategy": "email_code",
					"code":     testIssuedEmailCode(),
				})(t, r)
				http.SetCookie(w, &http.Cookie{Name: "__client", Value: "client-4"})
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer srv.Close()

			client, err := New(srv.URL, "test")
			if err != nil {
				t.Fatalf("New() error: %v", err)
			}
			client.nativeClientToken = "client-3"

			_, err = client.VerifyEmailCode(context.Background(), Challenge{
				SignInID:       "si_123",
				EmailAddressID: "idn_123",
			}, testIssuedEmailCode())
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("expected %q in error, got %v", tt.wantText, err)
			}
		})
	}
}

func TestVerifyEmailCodeRejectsPendingSignInTask(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validateFormRequest(http.MethodPost, "/v1/client/sign_ins/si_123/attempt_first_factor", "client-3", map[string]string{
			"strategy": "email_code",
			"code":     testIssuedEmailCode(),
		})(t, r)
		http.SetCookie(w, &http.Cookie{Name: "__client", Value: "client-4"})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{
				"status":             string(SignInComplete),
				"created_session_id": "sess_123",
				"current_task":       map[string]any{"key": "verify_email_address"},
			},
		})
	}))
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	client.nativeClientToken = "client-3"

	_, err = client.VerifyEmailCode(context.Background(), Challenge{SignInID: "si_123", EmailAddressID: "idn_123"}, testIssuedEmailCode())
	if err == nil || !strings.Contains(err.Error(), "pending sign-in task") {
		t.Fatalf("expected pending sign-in task error, got %v", err)
	}
}

func TestVerifyEmailCodeRejectsIncorrectAndExpiredCodes(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		errCode  string
		wantText string
	}{
		{name: "incorrect", code: testIncorrectEmailCode(), errCode: "form_code_incorrect", wantText: "incorrect"},
		{name: "expired", code: testExpiredEmailCode(), errCode: "form_code_expired", wantText: "expired"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				validateFormRequest(http.MethodPost, "/v1/client/sign_ins/si_123/attempt_first_factor", "client-3", map[string]string{
					"strategy": "email_code",
					"code":     tt.code,
				})(t, r)
				http.SetCookie(w, &http.Cookie{Name: "__client", Value: "client-4"})
				_ = json.NewEncoder(w).Encode(map[string]any{
					"errors": []map[string]any{{
						"code":         tt.errCode,
						"long_message": "server mentioned otp=" + tt.code + " session=sess_secret",
					}},
					"response": map[string]any{
						"status": string(SignInNeedsFirstFactor),
					},
				})
			}))
			defer srv.Close()

			var logs bytes.Buffer
			prev := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(prev)

			client, err := New(srv.URL, "test")
			if err != nil {
				t.Fatalf("New() error: %v", err)
			}
			client.nativeClientToken = "client-3"

			_, err = client.VerifyEmailCode(context.Background(), Challenge{
				SignInID:       "si_123",
				EmailAddressID: "idn_123",
			}, tt.code)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(strings.ToLower(err.Error()), tt.wantText) {
				t.Fatalf("expected %q in error, got %v", tt.wantText, err)
			}
			for _, secret := range []string{tt.code, "client-3", "sess_secret", "jwt_secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked secret %q: %v", secret, err)
				}
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("logs leaked secret %q: %s", secret, logs.String())
				}
			}
		})
	}
}

func TestVerifyEmailCodeRequiresCreatedSessionID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validateFormRequest(http.MethodPost, "/v1/client/sign_ins/si_123/attempt_first_factor", "client-3", map[string]string{
			"strategy": "email_code",
			"code":     testIssuedEmailCode(),
		})(t, r)
		http.SetCookie(w, &http.Cookie{Name: "__client", Value: "client-4"})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{"status": string(SignInComplete)},
		})
	}))
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	client.nativeClientToken = "client-3"

	_, err = client.VerifyEmailCode(context.Background(), Challenge{
		SignInID:       "si_123",
		EmailAddressID: "idn_123",
	}, testIssuedEmailCode())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "created_session_id") {
		t.Fatalf("expected created_session_id error, got %v", err)
	}
}

func TestVerifyEmailCodeRejectsPendingOrInactiveSession(t *testing.T) {
	tests := []struct {
		name     string
		session  map[string]any
		wantText string
	}{
		{
			name: "pending task",
			session: map[string]any{
				"response": map[string]any{
					"id":           "sess_123",
					"status":       "active",
					"current_task": map[string]any{"key": "verify_email_address"},
				},
			},
			wantText: "pending session task",
		},
		{
			name: "session task list",
			session: map[string]any{
				"response": map[string]any{
					"id":     "sess_123",
					"status": "active",
					"tasks":  []map[string]any{{"key": "verify_email_address"}},
				},
			},
			wantText: "pending session task",
		},
		{
			name: "inactive",
			session: map[string]any{
				"response": map[string]any{
					"id":     "sess_123",
					"status": "ended",
				},
			},
			wantText: "inactive session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newScriptedServer(t, []scriptedResponse{
				{
					validate: validateFormRequest(http.MethodPost, "/v1/client/sign_ins/si_123/attempt_first_factor", "client-3", map[string]string{
						"strategy": "email_code",
						"code":     testIssuedEmailCode(),
					}),
					cookies: []http.Cookie{{Name: "__client", Value: "client-4"}},
					bodyJSON: map[string]any{
						"response": map[string]any{
							"status":             string(SignInComplete),
							"created_session_id": "sess_123",
						},
					},
				},
				{
					validate: validateRequest(http.MethodGet, "/v1/client/sessions/sess_123", "client-4"),
					bodyJSON: tt.session,
				},
			})
			defer srv.Close()

			client, err := New(srv.URL, "test")
			if err != nil {
				t.Fatalf("New() error: %v", err)
			}
			client.nativeClientToken = "client-3"

			_, err = client.VerifyEmailCode(context.Background(), Challenge{
				SignInID:       "si_123",
				EmailAddressID: "idn_123",
			}, testIssuedEmailCode())
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(strings.ToLower(err.Error()), tt.wantText) {
				t.Fatalf("expected %q in error, got %v", tt.wantText, err)
			}
			if client.createdSessionID != "sess_123" {
				t.Fatalf("createdSessionID = %q, want %q", client.createdSessionID, "sess_123")
			}
		})
	}
}

func TestVerifyEmailCodeRejectsMismatchedSessionID(t *testing.T) {
	srv := newScriptedServer(t, []scriptedResponse{
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client/sign_ins/si_123/attempt_first_factor", "client-3", map[string]string{
				"strategy": "email_code",
				"code":     testIssuedEmailCode(),
			}),
			cookies: []http.Cookie{{Name: "__client", Value: "client-4"}},
			bodyJSON: map[string]any{"response": map[string]any{
				"status":             string(SignInComplete),
				"created_session_id": "sess_123",
			}},
		},
		{
			validate: validateRequest(http.MethodGet, "/v1/client/sessions/sess_123", "client-4"),
			bodyJSON: map[string]any{"response": map[string]any{
				"id": "sess_other", "status": "active",
			}},
		},
	})
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	client.nativeClientToken = "client-3"

	_, err = client.VerifyEmailCode(context.Background(), Challenge{SignInID: "si_123", EmailAddressID: "idn_123"}, testIssuedEmailCode())
	if err == nil || !strings.Contains(err.Error(), "did not match created session") {
		t.Fatalf("expected mismatched session error, got %v", err)
	}
}

func TestVerifyEmailCodeRetainsCleanupStateAfterTokenMintFailure(t *testing.T) {
	var cleanupAuth string
	srv := newScriptedServer(t, []scriptedResponse{
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client/sign_ins/si_123/attempt_first_factor", "client-3", map[string]string{
				"strategy": "email_code",
				"code":     testIssuedEmailCode(),
			}),
			cookies: []http.Cookie{{Name: "__client", Value: "client-4"}},
			bodyJSON: map[string]any{
				"response": map[string]any{
					"status":             string(SignInComplete),
					"created_session_id": "sess_123",
				},
			},
		},
		{
			validate: validateRequest(http.MethodGet, "/v1/client/sessions/sess_123", "client-4"),
			cookies:  []http.Cookie{{Name: "__client", Value: "client-5"}},
			bodyJSON: map[string]any{
				"response": map[string]any{
					"id":     "sess_123",
					"status": "active",
				},
			},
		},
		{
			validate: validateFormRequest(http.MethodPost, "/v1/client/sessions/sess_123/tokens", "client-5", map[string]string{}),
			status:   http.StatusInternalServerError,
			cookies:  []http.Cookie{{Name: "__client", Value: "client-6"}},
			bodyJSON: map[string]any{
				"errors": []map[string]any{{"code": "server_error"}},
			},
		},
		{
			validate: func(t *testing.T, r *http.Request) {
				cleanupAuth = r.Header.Get("Authorization")
				validateRequest(http.MethodPost, "/v1/client/sessions/sess_123/end", "client-6")(t, r)
			},
			bodyJSON: map[string]any{
				"response": map[string]any{"id": "sess_123"},
			},
		},
	})
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	client.nativeClientToken = "client-3"

	_, err = client.VerifyEmailCode(context.Background(), Challenge{
		SignInID:       "si_123",
		EmailAddressID: "idn_123",
	}, testIssuedEmailCode())
	if err == nil {
		t.Fatal("expected error")
	}
	var reqErr *RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected RequestError 500, got %v", err)
	}
	if client.createdSessionID != "sess_123" {
		t.Fatalf("createdSessionID = %q, want %q", client.createdSessionID, "sess_123")
	}
	if client.nativeClientToken != "client-6" {
		t.Fatalf("nativeClientToken = %q, want %q", client.nativeClientToken, "client-6")
	}

	if err := client.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup() error: %v", err)
	}
	if cleanupAuth != "Bearer client-6" {
		t.Fatalf("cleanup Authorization = %q, want %q", cleanupAuth, "Bearer client-6")
	}
	if client.createdSessionID != "" || client.nativeClientToken != "" {
		t.Fatalf("cleanup did not clear local state: session=%q token=%q", client.createdSessionID, client.nativeClientToken)
	}
}

func TestCleanupUsesLatestTokenAndClearsLocalStateOnFailure(t *testing.T) {
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		validateRequest(http.MethodPost, "/v1/client/sessions/sess_123/end", "client-6")(t, r)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"code": "server_error"}},
		})
	}))
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	client.nativeClientToken = "client-6"
	client.createdSessionID = "sess_123"

	err = client.Cleanup(context.Background())
	if err == nil {
		t.Fatal("expected cleanup error")
	}
	if seenAuth != "Bearer client-6" {
		t.Fatalf("Authorization = %q, want %q", seenAuth, "Bearer client-6")
	}
	if client.nativeClientToken != "" {
		t.Fatalf("nativeClientToken = %q, want empty", client.nativeClientToken)
	}
	if client.createdSessionID != "" {
		t.Fatalf("createdSessionID = %q, want empty", client.createdSessionID)
	}
}

func TestCleanupRemovesClientWhenNoSessionWasCreated(t *testing.T) {
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		validateRequest(http.MethodDelete, "/v1/client", "client-1")(t, r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	client.nativeClientToken = "client-1"

	if err := client.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup() error: %v", err)
	}
	if seenAuth != "Bearer client-1" {
		t.Fatalf("Authorization = %q, want %q", seenAuth, "Bearer client-1")
	}
	if client.nativeClientToken != "" || client.createdSessionID != "" {
		t.Fatalf("cleanup did not clear local state: token=%q session=%q", client.nativeClientToken, client.createdSessionID)
	}
}

func TestCloseClearsLocalState(t *testing.T) {
	client, err := New("https://clerk.ttsbuddy.com", "test")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	client.nativeClientToken = "client-6"
	client.createdSessionID = "sess_123"

	client.Close()

	if client.nativeClientToken != "" {
		t.Fatalf("nativeClientToken = %q, want empty", client.nativeClientToken)
	}
	if client.createdSessionID != "" {
		t.Fatalf("createdSessionID = %q, want empty", client.createdSessionID)
	}
}

type scriptedResponse struct {
	validate func(*testing.T, *http.Request)
	status   int
	cookies  []http.Cookie
	headers  map[string]string
	bodyJSON any
	body     string
}

func newScriptedServer(t *testing.T, steps []scriptedResponse) *httptest.Server {
	t.Helper()

	var index int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if index >= len(steps) {
			t.Fatalf("unexpected extra request %s %s", r.Method, r.URL.Path)
		}
		step := steps[index]
		index++

		if step.validate != nil {
			step.validate(t, r)
		}
		for key, value := range step.headers {
			w.Header().Set(key, value)
		}
		for _, cookie := range step.cookies {
			http.SetCookie(w, &cookie)
		}
		if step.status != 0 {
			w.WriteHeader(step.status)
		}
		if step.bodyJSON != nil {
			if err := json.NewEncoder(w).Encode(step.bodyJSON); err != nil {
				t.Fatalf("encode response: %v", err)
			}
			return
		}
		if step.body != "" {
			_, _ = io.WriteString(w, step.body)
		}
	}))
	t.Cleanup(func() {
		if index != len(steps) {
			t.Fatalf("consumed %d/%d scripted responses", index, len(steps))
		}
	})
	return srv
}

func validateRequest(method, path, token string) func(*testing.T, *http.Request) {
	return func(t *testing.T, r *http.Request) {
		t.Helper()
		if r.Method != method {
			t.Fatalf("method = %s, want %s", r.Method, method)
		}
		if r.URL.Path != path {
			t.Fatalf("path = %s, want %s", r.URL.Path, path)
		}
		if got := r.URL.Query().Get("_is_native"); got != "true" {
			t.Fatalf("_is_native query = %q, want %q", got, "true")
		}
		if got := r.Header.Get("Clerk-API-Version"); got != clerkAPIVersion {
			t.Fatalf("Clerk-API-Version = %q, want %q", got, clerkAPIVersion)
		}
		if got := r.Header.Get("User-Agent"); got != "ttsbuddy-cli/test" {
			t.Fatalf("User-Agent = %q, want %q", got, "ttsbuddy-cli/test")
		}
		wantAuth := ""
		if token != "" {
			wantAuth = "Bearer " + token
		}
		if got := r.Header.Get("Authorization"); got != wantAuth {
			t.Fatalf("Authorization = %q, want %q", got, wantAuth)
		}
	}
}

func validateFormRequest(method, path, token string, want map[string]string) func(*testing.T, *http.Request) {
	return func(t *testing.T, r *http.Request) {
		t.Helper()
		validateRequest(method, path, token)(t, r)
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-www-form-urlencoded") {
			t.Fatalf("Content-Type = %q, want application/x-www-form-urlencoded", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("ParseQuery() error: %v", err)
		}
		if len(values) != len(want) {
			t.Fatalf("form size = %d, want %d (%v)", len(values), len(want), values)
		}
		for key, expected := range want {
			if got := values.Get(key); got != expected {
				t.Fatalf("form[%s] = %q, want %q", key, got, expected)
			}
		}
	}
}

func TestMain(m *testing.M) {
	log.SetFlags(0)
	os.Exit(m.Run())
}
