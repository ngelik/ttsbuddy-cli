package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
)

func TestClerkCleanupRunsOnlyBeforeBackendExchange(t *testing.T) {
	if shouldAttemptClerkCleanup(true) {
		t.Fatal("successful backend exchange already revoked the temporary Clerk session")
	}
	if !shouldAttemptClerkCleanup(false) {
		t.Fatal("pre-exchange failure must retain best-effort Clerk cleanup")
	}
}

func authFixtureToken() string {
	return "ttsc_" + strings.Repeat("a", 8) + "_" + strings.Repeat("b", 48)
}

func writeAuthConfig(t *testing.T, home string) {
	writeAuthConfigWithExpiry(t, home, time.Now().Add(time.Hour))
}

func writeAuthConfigWithExpiry(t *testing.T, home string, expires time.Time) {
	t.Helper()
	dir := filepath.Join(home, ".ttsbuddy")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"api_key":     "ttsb_" + strings.Repeat("c", 8) + "_" + strings.Repeat("d", 48),
		"cli_session": map[string]string{"credential": authFixtureToken(), "expires_at": expires.UTC().Format(time.RFC3339)},
	})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAuthStatusChecksRemoteEvenWhenLocalExpiryPassed(t *testing.T) {
	var contacted atomic.Bool
	server := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacted.Store(true)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "credential": map[string]any{"type": "cli_session", "status": "expired", "usable": false, "expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}, "entitlement": map[string]any{"status": "active", "api_access": true}})
	}))
	home := t.TempDir()
	writeAuthConfigWithExpiry(t, home, time.Now().Add(-time.Hour))
	env := []string{"HOME=" + home, "TTSBUDDY_CLI_AUTH_URL=" + server + "/v1/cli-auth", "TTSBUDDY_ALLOW_CUSTOM_API_URL=true"}
	result := runCLI(t, env, "--json", "auth", "status")
	if result.ExitCode != 0 || !contacted.Load() || !strings.Contains(result.Stdout, `"status":"expired"`) {
		t.Fatalf("result=%#v contacted=%t", result, contacted.Load())
	}
}

func TestAuthCommandsRegisteredAndSignedOutLifecycle(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{{"auth", "--help"}, {"auth", "login", "extra"}, {"auth", "email", "extra"}, {"auth", "browser", "extra"}, {"auth", "status", "extra"}} {
		result := runCLI(t, []string{"HOME=" + home}, args...)
		if args[1] == "--help" && result.ExitCode != 0 {
			t.Fatalf("help: %#v", result)
		}
		if args[1] != "--help" && result.ExitCode != 2 {
			t.Fatalf("args: %#v", result)
		}
	}
	status := runCLI(t, []string{"HOME=" + home}, "auth", "status")
	if status.ExitCode != 1 || !strings.Contains(status.Stderr, "Not signed in") {
		t.Fatalf("status=%#v", status)
	}
	logout := runCLI(t, []string{"HOME=" + home}, "--json", "auth", "logout")
	if logout.ExitCode != 0 || strings.TrimSpace(logout.Stdout) != `{"status":"signed_out","success":true}` {
		t.Fatalf("logout=%#v", logout)
	}
}

func TestAuthHelpListsEmailAndBrowserMethods(t *testing.T) {
	result := runCLI(t, []string{"HOME=" + t.TempDir()}, "auth", "--help")
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "email") || !strings.Contains(result.Stdout, "browser") {
		t.Fatalf("result=%#v", result)
	}
}

func TestAuthSignupFlagRegisteredForEmailAndLogin(t *testing.T) {
	for _, command := range []string{"email", "login"} {
		result := runCLI(t, []string{"HOME=" + t.TempDir()}, "auth", command, "--help")
		if result.ExitCode != 0 || !strings.Contains(result.Stdout, "--signup") {
			t.Fatalf("%s help=%#v", command, result)
		}
	}
}

func TestAuthEmailSignupExchangesAndStoresCLIOnly(t *testing.T) {
	issued := authFixtureToken()
	var clerkStep atomic.Int32
	clerkServer := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step := clerkStep.Add(1)
		if r.URL.Query().Get("_is_native") != "true" || r.Header.Get("Clerk-API-Version") != "2026-05-12" {
			t.Fatalf("missing pinned native request headers: url=%s version=%q", r.URL.String(), r.Header.Get("Clerk-API-Version"))
		}
		if step == 1 {
			if r.URL.Path != "/v1/client" || r.Method != http.MethodPost {
				t.Fatalf("step 1=%s %s", r.Method, r.URL.Path)
			}
			w.Header().Set("Authorization", "client-1")
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"id": "client_123"}})
			return
		}
		token := "client-" + string(rune('0'+step-1))
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("step %d authorization=%q want Bearer %s", step, r.Header.Get("Authorization"), token)
		}
		switch {
		case step == 2 && r.URL.Path == "/v1/client/sign_ups":
			w.Header().Set("Authorization", "client-2")
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{
				"id": "su_123", "status": "missing_requirements", "unverified_fields": []string{"email_address"},
				"verifications": map[string]any{"email_address": map[string]any{"supported_strategies": []string{"email_code"}}},
			}})
		case step == 3 && r.URL.Path == "/v1/client/sign_ups/su_123/prepare_verification":
			w.Header().Set("Authorization", "client-3")
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{
				"id": "su_123", "status": "missing_requirements", "unverified_fields": []string{"email_address"},
			}})
		case step == 4 && r.URL.Path == "/v1/client/sign_ups/su_123/attempt_verification":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "code=654321") {
				t.Fatalf("signup code was not sent to Clerk request")
			}
			w.Header().Set("Authorization", "client-4")
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{
				"id": "su_123", "status": "complete", "created_session_id": "sess_123", "unverified_fields": []string{},
			}})
		case step == 5 && r.URL.Path == "/v1/client/sessions/sess_123":
			w.Header().Set("Authorization", "client-5")
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"id": "sess_123", "status": "active"}})
		case step == 6 && r.URL.Path == "/v1/client/sessions/sess_123/tokens":
			w.Header().Set("Authorization", "client-6")
			_ = json.NewEncoder(w).Encode(map[string]any{"jwt": "jwt-private-proof"})
		default:
			t.Fatalf("unexpected Clerk step %d: %s %s", step, r.Method, r.URL.Path)
		}
	}))
	backendServer := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer jwt-private-proof" {
			t.Fatalf("backend exchange method=%s authorization=%q", r.Method, r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":    true,
			"credential": map[string]any{"token": issued, "type": "cli_session", "scope": "agent_tts", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
		})
	}))

	home := t.TempDir()
	result := runCLIInput(t, "new@example.com\n654321\n", []string{
		"HOME=" + home,
		"TTSBUDDY_CLERK_FRONTEND_API_URL=" + clerkServer,
		"TTSBUDDY_CLI_AUTH_URL=" + backendServer + "/v1/cli-auth",
		"TTSBUDDY_ALLOW_CUSTOM_API_URL=true",
	}, "auth", "email", "--signup")
	if result.ExitCode != 0 {
		t.Fatalf("signup result=%#v", result)
	}
	if strings.Contains(result.Stdout+result.Stderr, "jwt-private-proof") || strings.Contains(result.Stdout+result.Stderr, issued) || strings.Contains(result.Stdout+result.Stderr, "654321") {
		t.Fatal("signup leaked proof, credential, or code")
	}
	body, err := os.ReadFile(filepath.Join(home, ".ttsbuddy", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), issued) || strings.Contains(string(body), "jwt-private-proof") {
		t.Fatalf("stored config=%s", body)
	}
	if got := clerkStep.Load(); got != 6 {
		t.Fatalf("Clerk steps=%d, want 6", got)
	}
}

func TestAuthEmailSignupBrowserFallbackGuidesToBrowser(t *testing.T) {
	tests := []struct {
		name     string
		response map[string]any
		status   int
	}{
		{
			name: "missing requirements",
			response: map[string]any{"response": map[string]any{
				"id": "su_123", "status": "missing_requirements",
				"missing_fields": []string{"legal_accepted"}, "unverified_fields": []string{"email_address"},
			}},
		},
		{
			name:     "captcha",
			status:   http.StatusUnprocessableEntity,
			response: map[string]any{"errors": []map[string]any{{"code": "captcha_required"}}},
		},
		{
			name:     "mfa",
			status:   http.StatusUnprocessableEntity,
			response: map[string]any{"errors": []map[string]any{{"code": "mfa_required"}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var step atomic.Int32
			clerkServer := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				current := step.Add(1)
				switch current {
				case 1:
					if r.Method != http.MethodPost || r.URL.Path != "/v1/client" {
						t.Fatalf("step 1=%s %s", r.Method, r.URL.Path)
					}
					w.Header().Set("Authorization", "client-1")
					_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"id": "client_123"}})
				case 2:
					if r.Method != http.MethodPost || r.URL.Path != "/v1/client/sign_ups" {
						t.Fatalf("step 2=%s %s", r.Method, r.URL.Path)
					}
					if tt.status != 0 {
						w.WriteHeader(tt.status)
					}
					_ = json.NewEncoder(w).Encode(tt.response)
				case 3:
					if r.Method != http.MethodDelete || r.URL.Path != "/v1/client" {
						t.Fatalf("cleanup=%s %s", r.Method, r.URL.Path)
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Fatalf("unexpected extra Clerk request: %s %s", r.Method, r.URL.Path)
				}
			}))
			result := runCLIInput(t, "new@example.com\n", []string{
				"HOME=" + t.TempDir(),
				"TTSBUDDY_CLERK_FRONTEND_API_URL=" + clerkServer,
				"TTSBUDDY_CLI_AUTH_URL=" + clerkServer + "/v1/cli-auth",
				"TTSBUDDY_ALLOW_CUSTOM_API_URL=true",
			}, "auth", "email", "--signup")
			if result.ExitCode != 1 {
				t.Fatalf("result=%#v", result)
			}
			if !strings.Contains(result.Stderr, "ttsbuddy auth browser") {
				t.Fatalf("fallback did not direct to browser: %s", result.Stderr)
			}
			if got := step.Load(); got != 3 {
				t.Fatalf("Clerk steps=%d, want signup rejection plus cleanup only", got)
			}
		})
	}
}

func TestAuthEmailKeepsLoginCompatibilityOutput(t *testing.T) {
	env := []string{"HOME=" + t.TempDir()}
	login := runCLIInput(t, "", env, "auth", "login")
	email := runCLIInput(t, "", env, "auth", "email")
	if login.ExitCode != email.ExitCode || login.Stdout != email.Stdout || login.Stderr != email.Stderr {
		t.Fatalf("login=%#v email=%#v", login, email)
	}
}

func TestAuthBrowserFailsBeforeFlowWhenClientIDMissing(t *testing.T) {
	result := runCLI(t, []string{
		"HOME=" + t.TempDir(),
		"TTSBUDDY_TEST_CLEAR_CLERK_OAUTH_CLIENT_ID=1",
	}, "auth", "browser")
	if result.ExitCode != 1 || !strings.Contains(result.Stderr, "missing Clerk OAuth client ID") {
		t.Fatalf("result=%#v", result)
	}
}

func TestAuthBrowserEnvOverridesRequireCustomOptIn(t *testing.T) {
	result := runCLI(t, []string{
		"HOME=" + t.TempDir(),
		"TTSBUDDY_TEST_CLEAR_CLERK_OAUTH_CLIENT_ID=1",
		"TTSBUDDY_CLERK_OAUTH_CLIENT_ID=development-client",
		"TTSBUDDY_TEST_BROWSER_AUTH_TOKEN=oauth-proof-should-not-run",
	}, "auth", "browser")
	if result.ExitCode != 1 || !strings.Contains(result.Stderr, "missing Clerk OAuth client ID") {
		t.Fatalf("result=%#v", result)
	}
}

func TestAuthBrowserHasProductionClientIDForGoInstallBuilds(t *testing.T) {
	if ClerkOAuthClientID != "gRApqxGCscvVfceh" {
		t.Fatalf("ClerkOAuthClientID=%q", ClerkOAuthClientID)
	}
}

func TestAuthBrowserExchangesProofAndStoresOnlyCLISession(t *testing.T) {
	oauthProof := "oauth-proof-private-fixture"
	issued := authFixtureToken()
	server := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+oauthProof || string(body) != `{"method":"browser"}` {
			t.Fatalf("method=%s auth=%q body=%q", r.Method, r.Header.Get("Authorization"), body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":    true,
			"credential": map[string]any{"token": issued, "type": "cli_session", "scope": "agent_tts", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
		})
	}))
	home := t.TempDir()
	permanent := "ttsb_" + strings.Repeat("c", 8) + "_" + strings.Repeat("d", 48)
	dir := filepath.Join(home, ".ttsbuddy")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"api_key":"`+permanent+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	result := runCLI(t, []string{
		"HOME=" + home,
		"TTSBUDDY_CLI_AUTH_URL=" + server + "/v1/cli-auth",
		"TTSBUDDY_ALLOW_CUSTOM_API_URL=true",
		"TTSBUDDY_CLERK_OAUTH_ISSUER=" + server,
		"TTSBUDDY_CLERK_OAUTH_CLIENT_ID=development-client",
		"TTSBUDDY_TEST_BROWSER_AUTH_TOKEN=" + oauthProof,
	}, "auth", "browser")
	if result.ExitCode != 0 {
		t.Fatalf("result=%#v", result)
	}
	if strings.Contains(result.Stdout+result.Stderr, oauthProof) || strings.Contains(result.Stdout+result.Stderr, issued) {
		t.Fatal("authentication secret leaked")
	}
	configBody, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configBody), issued) || !strings.Contains(string(configBody), permanent) || strings.Contains(string(configBody), oauthProof) {
		t.Fatalf("stored config does not preserve credential boundaries: %s", configBody)
	}
}

func TestAuthLoginRejectsJSONAndKeyBeforeReadingInput(t *testing.T) {
	for _, args := range [][]string{{"--json", "auth", "login"}, {"--key", "fixture", "auth", "login"}} {
		result := runCLIInput(t, "must-not-be-read\n", []string{"HOME=" + t.TempDir()}, args...)
		if result.ExitCode != 2 {
			t.Fatalf("result=%#v", result)
		}
		if strings.Contains(result.Stderr+result.Stdout, "must-not-be-read") {
			t.Fatal("stdin leaked")
		}
	}
}

func TestAuthStatusAndLogoutRejectKeyBeforeNetwork(t *testing.T) {
	for _, command := range []string{"status", "logout"} {
		result := runCLI(t, []string{"HOME=" + t.TempDir()}, "--key", "fixture", "auth", command)
		if result.ExitCode != 2 || !strings.Contains(result.Stderr, "--key is not supported") {
			t.Fatalf("%s result=%#v", command, result)
		}
	}
}

func TestAuthStatusAndLogoutUseOnlyStoredCLISession(t *testing.T) {
	seen := []string{}
	server := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+":"+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "credential": map[string]any{"type": "cli_session", "token": authFixtureToken(), "status": "active", "usable": true, "expires_at": "2026-09-06T00:00:00Z"}, "entitlement": map[string]any{"status": "active", "api_access": true}})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "status": "revoked"})
		}
	}))
	home := t.TempDir()
	writeAuthConfig(t, home)
	env := []string{"HOME=" + home, "TTSBUDDY_CLI_AUTH_URL=" + server + "/v1/cli-auth", "TTSBUDDY_ALLOW_CUSTOM_API_URL=true", "TTSBUDDY_API_KEY=environment-must-be-ignored"}
	status := runCLI(t, env, "--json", "auth", "status")
	if status.ExitCode != 0 || status.Stderr != "" {
		t.Fatalf("status=%#v", status)
	}
	if strings.Contains(status.Stdout, authFixtureToken()) {
		t.Fatal("status leaked credential")
	}
	logout := runCLI(t, env, "--json", "auth", "logout")
	if logout.ExitCode != 0 {
		t.Fatalf("logout=%#v", logout)
	}
	for _, call := range seen {
		if !strings.Contains(call, "Bearer "+authFixtureToken()) || strings.Contains(call, "environment") {
			t.Fatalf("call=%q", call)
		}
	}
	body, _ := os.ReadFile(filepath.Join(home, ".ttsbuddy", "config.json"))
	if strings.Contains(string(body), "ttsc_") {
		t.Fatal("CLI session not cleared")
	}
	if !strings.Contains(string(body), "ttsb_") {
		t.Fatal("permanent key removed")
	}
}

func TestValidateLoginCredentialMatchesExchangeContract(t *testing.T) {
	valid := &api.CLIAuthResponse{Success: true, Credential: &api.CLIAuthCredential{
		Token: authFixtureToken(), Type: "cli_session", Scope: "agent_tts",
		ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}}
	if _, err := validateLoginCredential(valid); err != nil {
		t.Fatalf("valid credential rejected: %v", err)
	}
	for name, mutate := range map[string]func(*api.CLIAuthResponse){
		"wrong type":  func(response *api.CLIAuthResponse) { response.Credential.Type = "permanent" },
		"wrong scope": func(response *api.CLIAuthResponse) { response.Credential.Scope = "other" },
		"expired": func(response *api.CLIAuthResponse) {
			response.Credential.ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		},
	} {
		t.Run(name, func(t *testing.T) {
			copyResponse := *valid
			copyCredential := *valid.Credential
			copyResponse.Credential = &copyCredential
			mutate(&copyResponse)
			if _, err := validateLoginCredential(&copyResponse); err == nil {
				t.Fatal("invalid credential accepted")
			}
		})
	}
}

func TestAuthLogoutLocalOnlySkipsNetworkAndPreservesPermanent(t *testing.T) {
	home := t.TempDir()
	writeAuthConfig(t, home)
	result := runCLI(t, []string{"HOME=" + home}, "--json", "auth", "logout", "--local-only")
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, `"remote_revocation_confirmed":false`) {
		t.Fatalf("result=%#v", result)
	}
}

func TestAuthLogoutLocalOnlyWarnsAboutRemoteValidity(t *testing.T) {
	home := t.TempDir()
	writeAuthConfig(t, home)
	result := runCLI(t, []string{"HOME=" + home}, "auth", "logout", "--local-only")
	if result.ExitCode != 0 || !strings.Contains(result.Stderr, "server validity may continue until") {
		t.Fatalf("result=%#v", result)
	}
}

func TestAuthLogoutUnauthorizedClearsAlreadyUnusableSession(t *testing.T) {
	server := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	home := t.TempDir()
	writeAuthConfig(t, home)
	env := []string{"HOME=" + home, "TTSBUDDY_CLI_AUTH_URL=" + server + "/v1/cli-auth", "TTSBUDDY_ALLOW_CUSTOM_API_URL=true"}
	result := runCLI(t, env, "--json", "auth", "logout")
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, `"status":"already_unusable"`) {
		t.Fatalf("result=%#v", result)
	}
	configBody, _ := os.ReadFile(filepath.Join(home, ".ttsbuddy", "config.json"))
	if strings.Contains(string(configBody), "ttsc_") {
		t.Fatal("already-unusable session was retained")
	}
}

func TestAuthLogoutFailureRetainsSessionAndReportsStructuredJSON(t *testing.T) {
	server := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	home := t.TempDir()
	writeAuthConfig(t, home)
	env := []string{"HOME=" + home, "TTSBUDDY_CLI_AUTH_URL=" + server + "/v1/cli-auth", "TTSBUDDY_ALLOW_CUSTOM_API_URL=true"}
	result := runCLI(t, env, "--json", "auth", "logout")
	if result.ExitCode != 1 {
		t.Fatalf("result=%#v", result)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &body); err != nil {
		t.Fatal(err)
	}
	if body["local_session_retained"] != true || body["retry_after_seconds"] != float64(17) {
		t.Fatalf("body=%#v", body)
	}
	configBody, _ := os.ReadFile(filepath.Join(home, ".ttsbuddy", "config.json"))
	if !strings.Contains(string(configBody), "ttsc_") {
		t.Fatal("session was cleared")
	}
}

func TestAuthLogoutUnconfirmedSuccessRetainsSession(t *testing.T) {
	for _, body := range []map[string]any{
		{"success": false, "status": "still_active"},
		{"success": true, "status": "unexpected"},
	} {
		t.Run(body["status"].(string), func(t *testing.T) {
			server := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(body)
			}))
			home := t.TempDir()
			writeAuthConfig(t, home)
			env := []string{"HOME=" + home, "TTSBUDDY_CLI_AUTH_URL=" + server + "/v1/cli-auth", "TTSBUDDY_ALLOW_CUSTOM_API_URL=true"}
			result := runCLI(t, env, "--json", "auth", "logout")
			if result.ExitCode != 1 {
				t.Fatalf("result=%#v", result)
			}
			configBody, _ := os.ReadFile(filepath.Join(home, ".ttsbuddy", "config.json"))
			if !strings.Contains(string(configBody), "ttsc_") {
				t.Fatal("session was cleared without confirmed revocation")
			}
		})
	}
}
