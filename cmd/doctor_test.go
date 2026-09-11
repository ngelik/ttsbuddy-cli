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

func decodeDoctorReport(t *testing.T, output string) doctorReport {
	t.Helper()
	var report doctorReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("doctor JSON: %v\n%s", err, output)
	}
	return report
}

func TestDoctorJSONSignedOutIsActionableAndNonZero(t *testing.T) {
	result := runCLI(t, []string{"HOME=" + t.TempDir()}, "doctor", "--json")
	if result.ExitCode != 1 {
		t.Fatalf("doctor exit=%d stdout=%s stderr=%s", result.ExitCode, result.Stdout, result.Stderr)
	}
	report := decodeDoctorReport(t, result.Stdout)
	if report.Ready {
		t.Fatal("signed-out doctor report is ready")
	}
	if len(report.NextActions) == 0 || !strings.Contains(strings.Join(report.NextActions, " "), "auth email") {
		t.Fatalf("missing auth next action: %#v", report.NextActions)
	}
	if result.Stderr != "" {
		t.Fatalf("doctor --json wrote diagnostics to stderr: %q", result.Stderr)
	}
}

func TestDoctorCredentialPrecedenceAndInvalidFlag(t *testing.T) {
	home := t.TempDir()
	writeAuthConfig(t, home)
	validEnv := testSubscriptionCredential()
	result := runCLI(t, []string{"HOME=" + home, "TTSBUDDY_API_KEY=" + validEnv}, "doctor", "--json", "--key", "not-a-key")
	if result.ExitCode != 1 {
		t.Fatalf("invalid flag doctor exit=%d: %s", result.ExitCode, result.Stdout)
	}
	report := decodeDoctorReport(t, result.Stdout)
	for _, check := range report.Checks {
		if check.Name == "credential" {
			if check.Status != "warning" || check.Details["source"] != "flag" {
				t.Fatalf("credential check=%#v", check)
			}
		}
	}

	result = runCLI(t, []string{"HOME=" + home, "TTSBUDDY_API_KEY=" + validEnv}, "doctor", "--json")
	if result.ExitCode != 0 {
		t.Fatalf("env-over-session doctor exit=%d: %s", result.ExitCode, result.Stdout)
	}
	report = decodeDoctorReport(t, result.Stdout)
	for _, check := range report.Checks {
		if check.Name == "credential" && check.Details["source"] != "environment" {
			t.Fatalf("env should win over session: %#v", check)
		}
	}
}

func TestDoctorDemoCredentialIsScopedToDemoEndpoint(t *testing.T) {
	result := runCLI(t, []string{"HOME=" + t.TempDir()}, "doctor", "--json", "--key", "ttsb_demo_cli")
	if result.ExitCode != 1 {
		t.Fatalf("demo key on real endpoint should not be ready: %d %s", result.ExitCode, result.Stdout)
	}
	result = runCLI(t, []string{"HOME=" + t.TempDir(), "TTSBUDDY_API_URL=https://www.ttsbuddy.com/v1/cli-demo-other"}, "doctor", "--json", "--key", "ttsb_demo_cli")
	if result.ExitCode != 1 {
		t.Fatalf("demo key on lookalike endpoint should not be ready: %d %s", result.ExitCode, result.Stdout)
	}
	result = runCLI(t, []string{"HOME=" + t.TempDir(), "TTSBUDDY_API_URL=https://www.ttsbuddy.com/v1/cli-demo"}, "doctor", "--json", "--key", "ttsb_demo_cli")
	if result.ExitCode != 0 {
		t.Fatalf("demo key on demo endpoint should be ready: %d %s", result.ExitCode, result.Stdout)
	}
	report := decodeDoctorReport(t, result.Stdout)
	for _, check := range report.Checks {
		if check.Name == "credential" && (check.Details["source"] != "flag" || check.Details["kind"] != "demo") {
			t.Fatalf("demo credential details=%#v", check)
		}
	}
}

func TestDoctorExpiredSessionIsNotReady(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ttsbuddy")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := `{"cli_session":{"credential":"` + authFixtureToken() + `","expires_at":"` + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) + `"}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	result := runCLI(t, []string{"HOME=" + home}, "doctor", "--json")
	if result.ExitCode != 1 {
		t.Fatalf("expired session doctor exit=%d: %s", result.ExitCode, result.Stdout)
	}
	report := decodeDoctorReport(t, result.Stdout)
	if report.Ready {
		t.Fatal("expired session reported ready")
	}
}

func TestDoctorOnlineChecksEffectiveCLISession(t *testing.T) {
	server := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/cli-auth" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ttsc_") {
			t.Fatalf("unexpected doctor status request: %s %s", r.Method, r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":     true,
			"credential":  map[string]any{"type": "cli_session", "status": "active", "usable": true, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
			"entitlement": map[string]any{"status": "active", "api_access": true},
		})
	}))
	home := t.TempDir()
	writeAuthConfig(t, home)
	result := runCLI(t, []string{"HOME=" + home, "TTSBUDDY_CLI_AUTH_URL=" + server + "/v1/cli-auth", "TTSBUDDY_ALLOW_CUSTOM_API_URL=true"}, "doctor", "--online", "--json")
	if result.ExitCode != 0 {
		t.Fatalf("online doctor exit=%d stdout=%s stderr=%s", result.ExitCode, result.Stdout, result.Stderr)
	}
	report := decodeDoctorReport(t, result.Stdout)
	for _, check := range report.Checks {
		if check.Name == "connectivity" && (check.Status != "healthy" || !strings.Contains(check.Message, "entitlement")) {
			t.Fatalf("online connectivity check=%#v", check)
		}
	}
}

func TestDoctorReportsInvalidConfigDirAsJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	result := runCLI(t, []string{"HOME=" + t.TempDir()}, "doctor", "--json", "--config-dir", path)
	if result.ExitCode != 1 || result.Stderr != "" {
		t.Fatalf("doctor invalid config dir=%#v", result)
	}
	report := decodeDoctorReport(t, result.Stdout)
	if report.Ready {
		t.Fatal("invalid config directory reported ready")
	}
	for _, check := range report.Checks {
		if check.Name == "config_directory" && check.Status != "failed" {
			t.Fatalf("config directory check=%#v", check)
		}
	}
}
