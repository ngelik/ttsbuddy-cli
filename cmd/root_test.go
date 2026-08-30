package cmd

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestVersion(t *testing.T) {
	r := runCLI(t, nil, "version")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "ttsbuddy ", "stdout")
	assertNotContains(t, r.Stdout, "ttsbuddy-cli", "stdout")
}

func TestVersionJSON(t *testing.T) {
	r := runCLI(t, nil, "version", "--json")
	assertExitCode(t, r, 0)
	assertValidJSON(t, r.Stdout)

	var info map[string]string
	if err := json.Unmarshal([]byte(r.Stdout), &info); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if info["go"] == "" {
		t.Error("missing 'go' field in version JSON")
	}
}

func TestDashDashVersion(t *testing.T) {
	r := runCLI(t, nil, "--version")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "ttsbuddy ", "stdout")
	assertNotContains(t, r.Stdout, "ttsbuddy-cli", "stdout")
}

func TestHelpExitsZero(t *testing.T) {
	r := runCLI(t, nil, "--help")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "Usage:", "stdout")
}

func TestArgErrorShowsCommandHelp(t *testing.T) {
	r := runCLI(t, nil, "web")
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "Fetch a webpage", "stderr")
	assertContains(t, r.Stderr, "Usage:", "stderr")
	assertContains(t, r.Stderr, "ttsbuddy web <url>", "stderr")
	assertNotContains(t, r.Stderr, "Error:", "stderr")
	assertNotContains(t, r.Stderr, "accepts 1 arg(s)", "stderr")
}

func TestAvailableCommandArgErrorsShowCommandHelp(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "completion subcommand", args: []string{"completion", "bash", "extra"}, want: "ttsbuddy completion bash"},
		{name: "config", args: []string{"config", "extra"}, want: "ttsbuddy config"},
		{name: "config get", args: []string{"config", "get"}, want: "ttsbuddy config get <key>"},
		{name: "config set", args: []string{"config", "set", "voice"}, want: "ttsbuddy config set <key> <value>"},
		{name: "speak", args: []string{"speak", "one", "two"}, want: "ttsbuddy speak [text]"},
		{name: "status", args: []string{"status", "one", "two"}, want: "ttsbuddy status [job_id]"},
		{name: "version", args: []string{"version", "extra"}, want: "ttsbuddy version"},
		{name: "voices", args: []string{"voices", "extra"}, want: "ttsbuddy voices"},
		{name: "web", args: []string{"web"}, want: "ttsbuddy web <url>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runCLI(t, nil, tt.args...)
			assertExitCode(t, r, 2)
			assertContains(t, r.Stderr, "Usage:", "stderr")
			assertContains(t, r.Stderr, tt.want, "stderr")
			assertNotContains(t, r.Stderr, "Error:", "stderr")
		})
	}
}

func TestFlagErrorShowsCommandHelp(t *testing.T) {
	r := runCLI(t, nil, "web", "--bogus")
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "Usage:", "stderr")
	assertContains(t, r.Stderr, "ttsbuddy web <url>", "stderr")
	assertNotContains(t, r.Stderr, "Error:", "stderr")
}

func TestJSONArgErrorStaysJSONOnly(t *testing.T) {
	r := runCLI(t, nil, "web", "--json")
	assertExitCode(t, r, 2)
	assertValidJSON(t, r.Stdout)
	assertContains(t, r.Stdout, "CLI_ERROR", "stdout")
	assertContains(t, r.Stdout, "accepts 1 arg(s)", "stdout")
	assertNotContains(t, r.Stderr, "Usage:", "stderr")
}

func TestVersionWithBrokenHome(t *testing.T) {
	r := runCLI(t, []string{"HOME=/nonexistent"}, "version")
	assertExitCode(t, r, 0)
}

func TestMissingKeyErrorsPointToDashboardSettings(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "speak", args: []string{"speak", "hello"}},
		{name: "status", args: []string{"status", "job-123"}},
		{name: "web", args: []string{"web", "https://example.com/article"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runCLI(t, envForTest(t.TempDir(), "", ""), tt.args...)
			assertExitCode(t, r, 2)
			assertContains(t, r.Stderr, "Dashboard -> Settings", "stderr")
			assertContains(t, r.Stderr, "https://ttsbuddy.com/dashboard", "stderr")
		})
	}
}

func TestJSONErrorOutput(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", ""), "speak", "--json", "test")
	assertExitCode(t, r, 2)
	assertValidJSON(t, r.Stdout)
	assertContains(t, r.Stdout, "CLI_ERROR", "stdout")
}

func TestRunCLIDoesNotInheritTTSBuddyCredentials(t *testing.T) {
	var called atomic.Bool
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"status":  "failed",
			"error":   map[string]string{"code": "INVALID_API_KEY", "message": "parent key used"},
		})
	}))

	t.Setenv("TTSBUDDY_API_KEY", "ttsb_parent_secret")
	t.Setenv("TTSBUDDY_API_URL", apiSrv)

	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", ""), "speak", "hello")
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "no API key configured", "stderr")
	assertNotContains(t, r.Stderr, "ttsb_parent_secret", "stderr")
	assertNotContains(t, r.Stdout, "ttsb_parent_secret", "stdout")
	if called.Load() {
		t.Fatal("subprocess inherited parent TTSBUDDY_* env and called the API")
	}
}

func TestCustomHTTPSAPIURLDeniedBeforeNetworkCommand(t *testing.T) {
	home := t.TempDir()
	env := append(envForTest(home, "https://api.example.com/v1/agent-tts", "ttsb_test_key"), "TTSBUDDY_ALLOW_CUSTOM_API_URL=false")
	r := runCLI(t, env, "speak", "hello")
	assertExitCode(t, r, 1)
	assertContains(t, r.Stderr, "custom API URL", "stderr")
}

func TestCustomHTTPSAPIURLAllowedWithEnvOptIn(t *testing.T) {
	home := t.TempDir()
	env := append(envForTest(home, "https://api.example.com/v1/agent-tts", "ttsb_test_key"), "TTSBUDDY_ALLOW_CUSTOM_API_URL=true")
	r := runCLI(t, env, "speak", "")
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "no text provided", "stderr")
	assertNotContains(t, r.Stderr, "custom API URL", "stderr")
}

func TestConfigSetAllowCustomAPIURLBypassesCredentialedAPIGate(t *testing.T) {
	home := t.TempDir()
	env := append(envForTest(home, "https://api.example.com/v1/agent-tts", "ttsb_test_key"), "TTSBUDDY_ALLOW_CUSTOM_API_URL=false")
	r := runCLI(t, env, "config", "set", "allow_custom_api_url", "true")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stderr, "allow_custom_api_url set: true", "stderr")
}

func TestInvalidAllowCustomAPIURLEnvWarningPrintsBeforeGate(t *testing.T) {
	home := t.TempDir()
	env := append(envForTest(home, "https://api.example.com/v1/agent-tts", "ttsb_test_key"), "TTSBUDDY_ALLOW_CUSTOM_API_URL=sometimes")
	r := runCLI(t, env, "speak", "hello")
	assertExitCode(t, r, 1)
	assertContains(t, r.Stderr, "Warning: invalid TTSBUDDY_ALLOW_CUSTOM_API_URL", "stderr")
	assertContains(t, r.Stderr, "custom API URL", "stderr")
}
