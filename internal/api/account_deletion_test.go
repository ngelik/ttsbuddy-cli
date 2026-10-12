package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const deletionTestUUID = "11111111-1111-4111-8111-111111111111"
const deletionCompleteBody = `{"success":true,"state":"completed","correlation_id":"` + deletionTestUUID + `","apple_authorization":"not_linked"}`

func deletionTestClient(t *testing.T, handler http.HandlerFunc) *AccountDeletionClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewAccountDeletionClient(server.URL+"/functions/v1/delete-account", "test-clerk-proof", "test-version", true)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestAccountDeletionEndpointValidation(t *testing.T) {
	for _, test := range []struct {
		name, endpoint, bearer string
		custom, valid          bool
	}{
		{"default", "", "proof", false, true},
		{"pinned", DefaultAccountDeletionURL, "proof", false, true},
		{"custom opted", "https://example.com/functions/v1/delete-account", "proof", true, true},
		{"custom rejected", "https://example.com/functions/v1/delete-account", "proof", false, false},
		{"official other host rejected", "https://ttsbuddy.com/v1/delete-account", "proof", false, false},
		{"loopback opted", "http://127.0.0.1:3456/functions/v1/delete-account", "proof", true, true},
		{"loopback requires opt", "http://127.0.0.1:3456/functions/v1/delete-account", "proof", false, false},
		{"remote http", "http://example.com/functions/v1/delete-account", "proof", true, false},
		{"query", DefaultAccountDeletionURL + "?token=secret", "proof", true, false},
		{"empty query", DefaultAccountDeletionURL + "?", "proof", true, false},
		{"fragment", DefaultAccountDeletionURL + "#secret", "proof", true, false},
		{"userinfo", "https://secret@example.com/functions/v1/delete-account", "proof", true, false},
		{"wrong path", "https://example.com/wrong", "proof", true, false},
		{"missing host", "/functions/v1/delete-account", "proof", true, false},
		{"empty proof", "", "", false, false},
		{"speech key", "", "ttsb_secret", false, false},
		{"CLI session", "", "ttsc_secret", false, false},
		{"agent identity", "", "ttsa_secret", false, false},
		{"header injection", "", "proof\r\nsecret", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewAccountDeletionClient(test.endpoint, test.bearer, "test", test.custom)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v, err=%v", test.valid, err)
			}
			if client != nil && client.httpClient.Timeout != 60*time.Second {
				t.Fatal("missing bounded timeout")
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("error leaked untrusted input")
			}
		})
	}
}

func TestAccountDeletionRequestContract(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			var calls int
			client := deletionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodDelete || r.Header.Get("Authorization") != "Bearer test-clerk-proof" || r.Header.Get("Idempotency-Key") != deletionTestUUID || r.Header.Get("User-Agent") != "ttsbuddy-cli/test-version" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("wrong request contract")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["idempotency_key"] != deletionTestUUID {
					t.Error("body/header request identity mismatch")
				}
				value, exists := body["apple_manual_fallback_confirmed"]
				if exists != fallback || (exists && value != true) {
					t.Error("fallback must be true or absent")
				}
				expectedFields := 1
				if fallback {
					expectedFields = 2
				}
				if len(body) != expectedFields {
					t.Error("unexpected body fields")
				}
				_, _ = w.Write([]byte(deletionCompleteBody))
			})
			// Caller-directed recovery preserves the same identity; the client never invents it.
			for i := 0; i < 2; i++ {
				response, status, err := client.Delete(context.Background(), deletionTestUUID, fallback)
				if err != nil || status != 200 || response.State != "completed" {
					t.Fatalf("%+v %d %v", response, status, err)
				}
			}
			if calls != 2 {
				t.Fatalf("unexpected retries: %d", calls)
			}
		})
	}
}

func TestAccountDeletionResponses(t *testing.T) {
	for _, test := range []struct {
		name, method, body string
		status             int
		valid, wantErr     bool
	}{
		{"completed delete", "DELETE", deletionCompleteBody, 200, true, false},
		{"completed status", "GET", deletionCompleteBody, 200, true, false},
		{"none", "GET", `{"success":false,"state":"none","partial_cleanup":false,"retryable":false}`, 404, true, false},
		{"none delete", "DELETE", `{"success":false,"state":"none"}`, 404, false, true},
		{"none 200", "GET", `{"success":false,"state":"none"}`, 200, false, true},
		{"unauthorized", "GET", `{"success":false,"error_category":"authentication","error":"SECRET MUST NOT LEAK"}`, 401, true, true},
		{"forbidden", "DELETE", `{"success":false}`, 403, true, true},
		{"partial", "DELETE", `{"success":false,"state":"failed","partial_cleanup":true,"retryable":true,"error_category":"database_cleanup","pending_count":2,"retry_after_seconds":30,"correlation_id":"` + deletionTestUUID + `"}`, 500, true, true},
		{"running status", "GET", `{"success":true,"state":"running","retryable":true,"correlation_id":"` + deletionTestUUID + `"}`, 200, true, false},
		{"requested status", "GET", `{"success":true,"state":"requested","correlation_id":"` + deletionTestUUID + `"}`, 200, true, false},
		{"failed status", "GET", `{"success":true,"state":"failed","error_category":"database_cleanup","correlation_id":"` + deletionTestUUID + `"}`, 200, true, false},
		{"running delete", "DELETE", `{"success":true,"state":"running","correlation_id":"` + deletionTestUUID + `"}`, 200, true, true},
		{"completed wrong HTTP", "DELETE", deletionCompleteBody, 202, false, true},
		{"completed failure HTTP", "DELETE", deletionCompleteBody, 500, false, true},
		{"completed success false", "DELETE", strings.Replace(deletionCompleteBody, `true`, `false`, 1), 200, false, true},
		{"completed missing UUID", "DELETE", `{"success":true,"state":"completed","apple_authorization":"not_linked"}`, 200, false, true},
		{"completed invalid UUID", "DELETE", strings.Replace(deletionCompleteBody, deletionTestUUID, "SECRET", 1), 200, false, true},
		{"completed missing Apple", "DELETE", `{"success":true,"state":"completed","correlation_id":"` + deletionTestUUID + `"}`, 200, false, true},
		{"completed legacy Apple", "DELETE", strings.Replace(deletionCompleteBody, "not_linked", "legacy_unknown", 1), 200, false, true},
		{"completed retryable", "DELETE", strings.Replace(deletionCompleteBody, `"success":true`, `"success":true,"retryable":true`, 1), 200, false, true},
		{"completed error", "DELETE", strings.Replace(deletionCompleteBody, `"success":true`, `"success":true,"error_category":"unknown"`, 1), 200, false, true},
		{"unknown state", "GET", `{"success":true,"state":"unknown"}`, 200, false, true},
		{"unknown category", "GET", `{"success":false,"error_category":"SECRET"}`, 500, false, true},
		{"unsafe error code", "GET", `{"success":false,"error_code":"secret material"}`, 500, false, true},
		{"pending negative", "GET", `{"success":false,"pending_count":-1}`, 500, false, true},
		{"retry too large", "GET", `{"success":false,"retry_after_seconds":301}`, 500, false, true},
		{"missing success", "GET", `{}`, 200, false, true},
		{"null success", "GET", `{"success":null}`, 500, false, true},
		{"malformed", "DELETE", `SECRET`, 200, false, true},
		{"null", "DELETE", `null`, 200, false, true},
		{"array", "DELETE", `[]`, 200, false, true},
		{"duplicate", "DELETE", strings.Replace(deletionCompleteBody, `"success":true`, `"success":false,"success":true`, 1), 200, false, true},
		{"case alias success", "DELETE", strings.Replace(deletionCompleteBody, `"success":true`, `"success":false,"SUCCESS":true`, 1), 200, false, true},
		{"trailing JSON", "DELETE", deletionCompleteBody + `{}`, 200, false, true},
		{"too large", "DELETE", strings.Repeat(" ", maxAccountDeletionBody+1), 200, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			client := deletionTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			})
			var response *AccountDeletionResponse
			var status int
			var err error
			if test.method == "GET" {
				response, status, err = client.Status(context.Background())
			} else {
				response, status, err = client.Delete(context.Background(), deletionTestUUID, false)
			}
			if status != test.status || (response != nil) != test.valid || (err != nil) != test.wantErr {
				t.Fatalf("response=%+v status=%d err=%v", response, status, err)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Fatal("secret leaked")
			}
			if calls != 1 {
				t.Fatalf("automatic retry: %d", calls)
			}
		})
	}
}

func TestAccountDeletionAppleOutcomes(t *testing.T) {
	for _, outcome := range []string{"not_linked", "revoked", "already_revoked_by_apple", "manual_action_required"} {
		body := strings.Replace(deletionCompleteBody, "not_linked", outcome, 1)
		if _, err := parseAccountDeletionResponse([]byte(body), http.MethodDelete, 200); err != nil {
			t.Errorf("%s: %v", outcome, err)
		}
	}
}

func TestAccountDeletionDoesNotRedirect(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forwarded.Add(1)
		_, _ = w.Write([]byte(deletionCompleteBody))
	}))
	defer target.Close()
	for _, code := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			client := deletionTestClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, code) })
			_, status, err := client.Delete(context.Background(), deletionTestUUID, false)
			if err == nil || status != code {
				t.Fatalf("status=%d err=%v", status, err)
			}
		})
	}
	if forwarded.Load() != 0 {
		t.Fatal("forwarded deletion credentials")
	}
}

func TestAccountDeletionCancellationAndBadRequest(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	defer close(release)
	client := deletionTestClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); <-release })
	if _, _, err := client.Delete(context.Background(), "invalid", false); err == nil {
		t.Fatal("invalid UUID accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request contacted backend")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, _, err := client.Delete(ctx, deletionTestUUID, false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("unexpected request count %d", calls.Load())
	}
}
