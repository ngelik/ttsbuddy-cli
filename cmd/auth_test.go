package cmd

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
)

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
	for _, args := range [][]string{{"auth", "--help"}, {"auth", "login", "extra"}, {"auth", "status", "extra"}} {
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

func TestValidateLoginCredentialFailsClosed(t *testing.T) {
	valid := &api.CLIAuthResponse{Success: true, Credential: &api.CLIAuthCredential{
		Token: authFixtureToken(), Type: "cli_session", Scope: "agent_tts", Status: "active", Usable: true,
		ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}}
	if _, err := validateLoginCredential(valid); err != nil {
		t.Fatalf("valid credential rejected: %v", err)
	}
	for name, mutate := range map[string]func(*api.CLIAuthResponse){
		"unusable": func(response *api.CLIAuthResponse) { response.Credential.Usable = false },
		"revoked":  func(response *api.CLIAuthResponse) { response.Credential.Status = "revoked" },
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
