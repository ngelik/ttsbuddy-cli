package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const demoStubTTSBuddy = `#!/usr/bin/env bash
set -euo pipefail

{
  printf 'TTSBUDDY_API_URL=%s\n' "${TTSBUDDY_API_URL:-}"
  printf 'TTSBUDDY_API_KEY=%s\n' "${TTSBUDDY_API_KEY:-}"
  printf 'ARGS=%s\n' "$*"
} >> "${TTSBUDDY_DEMO_STUB_LOG:?}"

out=""
args=("$@")
for ((i=0; i<${#args[@]}; i++)); do
  if [[ "${args[$i]}" == "-o" && $((i+1)) -lt ${#args[@]} ]]; then
    out="${args[$((i+1))]}"
  fi
done

if [[ "$out" == "-" ]]; then
  printf 'ID3FAKE'
elif [[ -n "$out" ]]; then
  mkdir -p "$(dirname "$out")"
  printf 'ID3FAKE' > "$out"
fi

if [[ " $* " == *" --json "* ]]; then
  printf '{"success":true,"audio_url":"https://example.invalid/audio.mp3"}\n'
fi
`

func TestDemoScriptDoesNotLeakInheritedAPIKey(t *testing.T) {
	demoRoot := copyDemoScriptToTempRoot(t)

	binDir := t.TempDir()
	stubPath := filepath.Join(binDir, "ttsbuddy")
	if err := os.WriteFile(stubPath, []byte(demoStubTTSBuddy), 0o755); err != nil {
		t.Fatal(err)
	}
	stubLog := filepath.Join(t.TempDir(), "stub.log")

	cmd := exec.Command("bash", filepath.Join(demoRoot, "demo", "cli-demo.sh"))
	cmd.Env = []string{
		"HOME=" + t.TempDir(),
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TTSBUDDY_API_KEY=ttsb_real_secretvalue",
		"TTSBUDDY_API_URL=https://api.example.com/v1/agent-tts",
		"TTSBUDDY_DEMO_STUB_LOG=" + stubLog,
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("demo script failed: %v\n%s", err, output)
	}

	text := string(output)
	assertNotContains(t, text, "ttsb_real_secretvalue", "demo output")
	assertNotContains(t, text, "secretvalue", "demo output")
	assertNotContains(t, text, "ttsb_demo_cli", "demo output")
	assertNotContains(t, text, "https://api.example.com/v1/agent-tts", "demo output")
	assertContains(t, text, "API URL: https://www.ttsbuddy.com/v1/cli-demo", "demo output")
	assertContains(t, text, "API key: ttsb_demo_...", "demo output")

	logBytes, err := os.ReadFile(stubLog)
	if err != nil {
		t.Fatal(err)
	}
	stubEnv := string(logBytes)
	if strings.Contains(stubEnv, "ttsb_real_secretvalue") || strings.Contains(stubEnv, "https://api.example.com/v1/agent-tts") {
		t.Fatalf("demo script passed inherited credentials to stub:\n%s", stubEnv)
	}
	assertContains(t, stubEnv, "TTSBUDDY_API_URL=https://www.ttsbuddy.com/v1/cli-demo", "stub env")
	assertContains(t, stubEnv, "TTSBUDDY_API_KEY=ttsb_demo_cli", "stub env")
}

func TestDemoScriptRealKeyModeDefaultsAPIURLAndRedactsKey(t *testing.T) {
	demoRoot := copyDemoScriptToTempRoot(t)

	binDir := t.TempDir()
	stubPath := filepath.Join(binDir, "ttsbuddy")
	if err := os.WriteFile(stubPath, []byte(demoStubTTSBuddy), 0o755); err != nil {
		t.Fatal(err)
	}
	stubLog := filepath.Join(t.TempDir(), "stub.log")

	cmd := exec.Command("bash", filepath.Join(demoRoot, "demo", "cli-demo.sh"))
	cmd.Env = []string{
		"HOME=" + t.TempDir(),
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TTSBUDDY_API_KEY=ttsb_real_secretvalue",
		"TTSBUDDY_DEMO_USE_REAL_KEY=1",
		"TTSBUDDY_DEMO_STUB_LOG=" + stubLog,
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("demo script failed: %v\n%s", err, output)
	}

	text := string(output)
	assertNotContains(t, text, "ttsb_real_secretvalue", "demo output")
	assertNotContains(t, text, "secretvalue", "demo output")
	assertContains(t, text, "API URL: https://www.ttsbuddy.com/v1/agent-tts", "demo output")
	assertContains(t, text, "API key: ttsb_real_...", "demo output")

	logBytes, err := os.ReadFile(stubLog)
	if err != nil {
		t.Fatal(err)
	}
	stubEnv := string(logBytes)
	assertContains(t, stubEnv, "TTSBUDDY_API_URL=https://www.ttsbuddy.com/v1/agent-tts", "stub env")
	assertContains(t, stubEnv, "TTSBUDDY_API_KEY=ttsb_real_secretvalue", "stub env")
}

func TestDemoScriptRealKeyModeRequiresAPIKey(t *testing.T) {
	demoRoot := copyDemoScriptToTempRoot(t)

	cmd := exec.Command("bash", filepath.Join(demoRoot, "demo", "cli-demo.sh"))
	cmd.Env = []string{
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
		"TTSBUDDY_DEMO_USE_REAL_KEY=1",
	}
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("demo script succeeded without required API key:\n%s", output)
	}
	assertContains(t, string(output), "TTSBUDDY_API_KEY is required when TTSBUDDY_DEMO_USE_REAL_KEY=1", "demo output")
}

func copyDemoScriptToTempRoot(t *testing.T) string {
	t.Helper()

	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(repoRoot, "demo", "cli-demo.sh")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	demoDir := filepath.Join(root, "demo")
	if err := os.MkdirAll(demoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(demoDir, "cli-demo.sh"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}
