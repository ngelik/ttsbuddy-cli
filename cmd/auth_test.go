package cmd

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func authFixtureToken() string {
	return "ttsc_" + strings.Repeat("a", 8) + "_" + strings.Repeat("b", 48)
}

func writeAuthConfig(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".ttsbuddy")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"api_key":     "ttsb_" + strings.Repeat("c", 8) + "_" + strings.Repeat("d", 48),
		"cli_session": map[string]string{"credential": authFixtureToken(), "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
	})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0600); err != nil {
		t.Fatal(err)
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

func TestAuthStatusAndLogoutUseOnlyStoredCLISession(t *testing.T) {
	seen := []string{}
	server := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+":"+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "credential": map[string]any{"type": "cli_session", "status": "active", "usable": true, "expires_at": "2026-09-06T00:00:00Z"}, "entitlement": map[string]any{"status": "active", "api_access": true}})
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

func TestAuthLogoutLocalOnlySkipsNetworkAndPreservesPermanent(t *testing.T) {
	home := t.TempDir()
	writeAuthConfig(t, home)
	result := runCLI(t, []string{"HOME=" + home}, "--json", "auth", "logout", "--local-only")
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, `"remote_revocation_confirmed":false`) {
		t.Fatalf("result=%#v", result)
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
