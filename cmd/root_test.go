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
