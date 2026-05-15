package cmd

import (
	"encoding/json"
	"testing"
)

func TestVersion(t *testing.T) {
	r := runCLI(t, nil, "version")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "ttsbuddy-cli", "stdout")
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
	assertContains(t, r.Stdout, "ttsbuddy-cli", "stdout")
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

func TestJSONErrorOutput(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", ""), "speak", "--json", "test")
	assertExitCode(t, r, 2)
	assertValidJSON(t, r.Stdout)
	assertContains(t, r.Stdout, "CLI_ERROR", "stdout")
}
