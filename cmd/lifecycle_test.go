package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestStructuredSignupCompleteLifecycle keeps the complete browserless
// account path in the normal race-tested suite. All providers are in-memory
// fixtures; no mailbox, production account, or external credential is used.
func TestStructuredSignupCompleteLifecycle(t *testing.T) {
	issued := authFixtureToken()
	proof := "jwt-lifecycle-proof"
	var clerkStep atomic.Int32
	clerkServer := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step := clerkStep.Add(1)
		if r.URL.Query().Get("_is_native") != "true" {
			t.Fatalf("missing native query: %s", r.URL.String())
		}
		if step == 1 {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/client" {
				t.Fatalf("clerk step 1=%s %s", r.Method, r.URL.Path)
			}
			w.Header().Set("Authorization", "client-1")
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"id": "client_123"}})
			return
		}
		if r.Header.Get("Authorization") != "Bearer client-"+string(rune('0'+step-1)) {
			t.Fatalf("clerk step %d authorization=%q", step, r.Header.Get("Authorization"))
		}
		switch {
		case step == 2 && r.URL.Path == "/v1/client/sign_ups":
			w.Header().Set("Authorization", "client-2")
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{
				"id": "su_lifecycle", "status": "missing_requirements", "unverified_fields": []string{"email_address"},
				"verifications": map[string]any{"email_address": map[string]any{"supported_strategies": []string{"email_code"}}},
			}})
		case step == 3 && r.URL.Path == "/v1/client/sign_ups/su_lifecycle/prepare_verification":
			w.Header().Set("Authorization", "client-3")
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{
				"id": "su_lifecycle", "status": "missing_requirements", "unverified_fields": []string{"email_address"},
			}})
		case step == 4 && r.URL.Path == "/v1/client/sign_ups/su_lifecycle/attempt_verification":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "code=654321") {
				t.Fatalf("signup code not sent to Clerk")
			}
			w.Header().Set("Authorization", "client-4")
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{
				"id": "su_lifecycle", "status": "complete", "created_session_id": "sess_lifecycle", "unverified_fields": []string{},
			}})
		case step == 5 && r.URL.Path == "/v1/client/sessions/sess_lifecycle":
			w.Header().Set("Authorization", "client-5")
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"id": "sess_lifecycle", "status": "active"}})
		case step == 6 && r.URL.Path == "/v1/client/sessions/sess_lifecycle/tokens":
			w.Header().Set("Authorization", "client-6")
			_ = json.NewEncoder(w).Encode(map[string]any{"jwt": proof})
		default:
			t.Fatalf("unexpected Clerk step %d: %s %s", step, r.Method, r.URL.Path)
		}
	}))

	var revoked atomic.Bool
	var ttsPosts atomic.Int32
	const expectedAudio = "ID3 lifecycle-fixture-audio"
	audioServer := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte(expectedAudio))
	}))
	backendServer := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/cli-auth":
			if r.Header.Get("Authorization") == "Bearer "+proof && r.Method == http.MethodPost {
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "credential": map[string]any{
					"token": issued, "type": "cli_session", "scope": "agent_tts", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				}})
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+issued {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Method == http.MethodDelete {
				revoked.Store(true)
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "status": "revoked"})
				return
			}
			if revoked.Load() {
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "credential": map[string]any{
					"type": "cli_session", "status": "revoked", "usable": false, "expires_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
				}, "entitlement": map[string]any{"status": "active", "api_access": true}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "credential": map[string]any{
				"type": "cli_session", "status": "active", "usable": true, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			}, "entitlement": map[string]any{"status": "active", "api_access": true}})
		case "/v1/agent-tts":
			if r.Header.Get("Authorization") != "Bearer "+issued || revoked.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			switch r.Method {
			case http.MethodPost:
				ttsPosts.Add(1)
				if r.Header.Get("Idempotency-Key") == "" {
					t.Fatal("lifecycle synthesis omitted idempotency key")
				}
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["text"] != "lifecycle synthesis" || request["voice"] != "af_heart" || request["speed"] != 0.9 {
					t.Fatalf("unexpected lifecycle synthesis request: %#v err=%v", request, err)
				}
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "status": "processing", "job_id": "job_lifecycle", "retry_after_seconds": 0})
			case http.MethodGet:
				if r.URL.Query().Get("id") != "job_lifecycle" {
					t.Fatalf("unexpected lifecycle status query: %s", r.URL.RawQuery)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "status": "completed", "job_id": "job_lifecycle", "audio_url": audioServer + "/audio.mp3", "audio": map[string]any{"format": "mp3", "voice": "af_heart", "speed": 1.0}})
			}
		default:
			t.Fatalf("unexpected backend path: %s", r.URL.Path)
		}
	}))

	home := t.TempDir()
	env := []string{
		"HOME=" + home,
		"TTSBUDDY_CLERK_FRONTEND_API_URL=" + clerkServer,
		"TTSBUDDY_CLI_AUTH_URL=" + backendServer + "/v1/cli-auth",
		"TTSBUDDY_API_URL=" + backendServer + "/v1/agent-tts",
		"TTSBUDDY_ALLOW_CUSTOM_API_URL=true",
		"TTSBUDDY_VOICE=af_heart",
		"TTSBUDDY_SPEED=0.9",
	}
	started := runCLI(t, env, "auth", "email", "start", "--email", "signup-lifecycle@example.com", "--signup", "--json")
	if started.ExitCode != 0 || started.Stderr != "" {
		t.Fatalf("signup start=%#v", started)
	}
	var pending map[string]any
	if err := json.Unmarshal([]byte(started.Stdout), &pending); err != nil {
		t.Fatal(err)
	}
	challengeID, _ := pending["challenge_id"].(string)
	if challengeID == "" || pending["status"] != "verification_required" {
		t.Fatalf("pending=%#v", pending)
	}
	if strings.Contains(started.Stdout, "signup-lifecycle@example.com") || strings.Contains(started.Stdout, "client-3") {
		t.Fatalf("signup start leaked protected state: %s", started.Stdout)
	}

	verified := runCLIInput(t, "654321\n", env, "auth", "email", "verify", "--challenge-id", challengeID, "--code-stdin", "--json")
	if verified.ExitCode != 0 || verified.Stderr != "" || !strings.Contains(verified.Stdout, `"status":"signed_in"`) {
		t.Fatalf("signup verify=%#v", verified)
	}
	out := filepath.Join(home, "lifecycle.mp3")
	speak := runCLI(t, env, "speak", "lifecycle synthesis", "--output", out, "--quiet")
	if speak.ExitCode != 0 {
		t.Fatalf("speak=%#v", speak)
	}
	data, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(data, []byte(expectedAudio)) {
		t.Fatalf("downloaded lifecycle audio: bytes=%d err=%v", len(data), err)
	}
	if ttsPosts.Load() != 1 {
		t.Fatalf("synthesis POSTs=%d, want one", ttsPosts.Load())
	}
	status := runCLI(t, env, "status", "job_lifecycle", "--json")
	if status.ExitCode != 0 || !strings.Contains(status.Stdout, `"status": "completed"`) {
		t.Fatalf("status=%#v", status)
	}
	logout := runCLI(t, env, "--json", "auth", "logout")
	if logout.ExitCode != 0 || !strings.Contains(logout.Stdout, `"status":"revoked"`) || !revoked.Load() {
		t.Fatalf("logout=%#v revoked=%t", logout, revoked.Load())
	}
	configAfterLogout, _ := os.ReadFile(filepath.Join(home, ".ttsbuddy", "config.json"))
	if strings.Contains(string(configAfterLogout), issued) || strings.Contains(string(configAfterLogout), proof) {
		t.Fatalf("credential/proof retained immediately after logout: %s", configAfterLogout)
	}
	// Production status semantics return a 200 revoked/usable:false record;
	// restore only the captured fixture token to prove the CLI observes it.
	configPath := filepath.Join(home, ".ttsbuddy", "config.json")
	if err := os.WriteFile(configPath, []byte(`{"cli_session":{"credential":"`+issued+`","expires_at":"`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	revokedStatus := runCLI(t, env, "auth", "status", "--json")
	if revokedStatus.ExitCode != 0 || !strings.Contains(revokedStatus.Stdout, `"status":"revoked"`) || strings.Contains(revokedStatus.Stdout, issued) {
		t.Fatalf("revoked status=%#v", revokedStatus)
	}
	oldSynthesis := runCLI(t, env, "speak", "old credential synthesis", "--no-download", "--json")
	if oldSynthesis.ExitCode != 1 || !strings.Contains(oldSynthesis.Stdout, `"SESSION_REJECTED"`) {
		t.Fatalf("old credential synthesis=%#v", oldSynthesis)
	}
	if strings.Contains(oldSynthesis.Stdout, issued) || strings.Contains(oldSynthesis.Stderr, issued) {
		t.Fatalf("old credential leaked: %#v", oldSynthesis)
	}
}
