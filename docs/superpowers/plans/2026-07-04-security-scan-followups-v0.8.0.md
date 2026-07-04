# Security Scan Followups v0.8.0 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the four remaining security-scan findings from the 2026-07-03 TTSBuddy CLI scan, verify them locally and against production, then release `v0.8.0` only if every gate is green.

**Architecture:** Keep the fixes small and close to the affected boundaries: credential-routing policy in `internal/config`, CLI output safety in `cmd`, demo hygiene in `demo`, and subprocess-test isolation in `cmd/testutil_test.go`. Preserve existing local-development behavior for localhost HTTP endpoints while requiring explicit opt-in before a configured API key can be sent to a non-TTSBuddy HTTPS origin.

**Tech Stack:** Go stdlib, Cobra CLI, existing `httptest`-based unit tests, Bash demo script, existing `make test`/`make lint`/GoReleaser/Homebrew release flow.

---

## Coordination Model

Use subagent-driven development:

- One implementer subagent per finding.
- After each finding, run a spec-compliance review subagent against that task text.
- Then run a code-quality/security review subagent against the diff.
- The controller verifies tests independently before marking a task complete.
- Do not tag or release from `main` until all tasks, reviews, local verification, live acceptance, and a fresh security scan are complete.

Start execution on a branch:

```bash
git checkout -b codex/fix-security-scan-followups-v0.8.0
```

Do not write the temporary production API key into this plan, source files, shell scripts, test fixtures, or committed logs. Use the key supplied in the current chat only as an ephemeral shell environment value during live acceptance.

## Files To Modify

- `internal/config/config.go`: add persisted opt-in config for custom API endpoints and the credentialed-API URL trust gate.
- `internal/config/resolve.go`: resolve `allow_custom_api_url` from config and `TTSBUDDY_ALLOW_CUSTOM_API_URL` from the environment.
- `internal/config/config_test.go`: add config and trust-gate regression tests.
- `cmd/root.go`: replace the current insecure-HTTP-only check with the credentialed API URL trust gate.
- `cmd/config.go`: expose `allow_custom_api_url` in `config`, `config get`, and `config set`.
- `cmd/config_test.go`: verify CLI config get/set behavior for the new boolean setting.
- `cmd/root_test.go`: verify custom HTTPS endpoints are denied unless explicitly allowed.
- `cmd/speak.go`: redact signed query strings and fragments in download-failure diagnostics.
- `cmd/speak_test.go`: add download-failure diagnostic leak regression.
- `demo/cli-demo.sh`: prevent inherited real API keys from being printed or used by demo mode.
- `demo/README.md`: document demo-mode key behavior and explicit real-key opt-in.
- `cmd/demo_script_test.go`: add a no-network regression test for inherited demo key leakage.
- `cmd/testutil_test.go`: filter inherited `TTSBUDDY_*`/`TB_HOME` values out of subprocess test environments.
- `README.md`: document custom API URL opt-in and the security reason for it.

---

### Task 1: Fix Finding 1, Custom API Endpoint Can Receive Stored API Keys

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/resolve.go`
- Modify: `cmd/root.go`
- Modify: `cmd/config.go`
- Test: `internal/config/config_test.go`
- Test: `cmd/config_test.go`
- Test: `cmd/root_test.go`
- Docs: `README.md`

- [ ] **Step 1: Add failing config package tests**

Append these tests near `TestCheckInsecureURL` in `internal/config/config_test.go`.

```go
func TestCheckCredentialedAPIURL(t *testing.T) {
	tests := []struct {
		name        string
		rawURL      string
		allowCustom bool
		wantErr     bool
	}{
		{name: "default www host allowed", rawURL: "https://www.ttsbuddy.com/v1/agent-tts", wantErr: false},
		{name: "apex prod host allowed", rawURL: "https://ttsbuddy.com/v1/agent-tts", wantErr: false},
		{name: "localhost http allowed for dev", rawURL: "http://localhost:54321/v1/agent-tts", wantErr: false},
		{name: "loopback http allowed for tests", rawURL: "http://127.0.0.1:8080/v1/agent-tts", wantErr: false},
		{name: "custom https denied by default", rawURL: "https://api.example.com/v1/agent-tts", wantErr: true},
		{name: "custom https allowed with explicit opt in", rawURL: "https://api.example.com/v1/agent-tts", allowCustom: true, wantErr: false},
		{name: "non-local http denied even with opt in", rawURL: "http://api.example.com/v1/agent-tts", allowCustom: true, wantErr: true},
		{name: "unsupported scheme denied", rawURL: "ftp://ttsbuddy.com/v1/agent-tts", allowCustom: true, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckCredentialedAPIURL(tt.rawURL, tt.allowCustom)
			if tt.wantErr && err == nil {
				t.Fatalf("CheckCredentialedAPIURL(%q, %v) returned nil, want error", tt.rawURL, tt.allowCustom)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("CheckCredentialedAPIURL(%q, %v) returned error: %v", tt.rawURL, tt.allowCustom, err)
			}
		})
	}
}

func TestResolveAllowCustomAPIURL(t *testing.T) {
	t.Setenv("TTSBUDDY_ALLOW_CUSTOM_API_URL", "")

	cfg := &Config{AllowCustomAPIURL: true}
	resolved, warnings := Resolve(cfg, FlagValues{})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if !resolved.AllowCustomAPIURL {
		t.Fatal("config AllowCustomAPIURL should resolve to true")
	}

	t.Setenv("TTSBUDDY_ALLOW_CUSTOM_API_URL", "false")
	resolved, warnings = Resolve(cfg, FlagValues{})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if resolved.AllowCustomAPIURL {
		t.Fatal("env false should override config true")
	}
}

func TestResolveAllowCustomAPIURLWarning(t *testing.T) {
	t.Setenv("TTSBUDDY_ALLOW_CUSTOM_API_URL", "sometimes")
	resolved, warnings := Resolve(&Config{}, FlagValues{})
	if resolved.AllowCustomAPIURL {
		t.Fatal("invalid env value should not enable custom API URLs")
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings: got %d, want 1 (%v)", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "invalid TTSBUDDY_ALLOW_CUSTOM_API_URL") {
		t.Fatalf("warning should mention invalid env var, got %q", warnings[0])
	}
}
```

Run:

```bash
go test ./internal/config -run 'TestCheckCredentialedAPIURL|TestResolveAllowCustomAPIURL' -count=1
```

Expected: fails because `CheckCredentialedAPIURL`, `Config.AllowCustomAPIURL`, and `ResolvedConfig.AllowCustomAPIURL` do not exist yet.

- [ ] **Step 2: Implement persisted config and resolver support**

In `internal/config/config.go`, add the field:

```go
AllowCustomAPIURL bool `json:"allow_custom_api_url,omitempty"`
```

Add the key to `validKeys`:

```go
"allow_custom_api_url": true,
```

Add this case to `Get`:

```go
case "allow_custom_api_url":
	return strconv.FormatBool(cfg.AllowCustomAPIURL), nil
```

Add this case to `Set`:

```go
case "allow_custom_api_url":
	allow, parseErr := strconv.ParseBool(value)
	if parseErr != nil {
		return &ValidationError{Msg: fmt.Sprintf("invalid allow_custom_api_url value: %s (use true or false)", value)}
	}
	cfg.AllowCustomAPIURL = allow
```

In `internal/config/resolve.go`, add the field:

```go
AllowCustomAPIURL bool
```

Initialize it from config:

```go
AllowCustomAPIURL: cfg.AllowCustomAPIURL,
```

Add environment override support inside `applyEnv`:

```go
if v := os.Getenv("TTSBUDDY_ALLOW_CUSTOM_API_URL"); v != "" {
	allow, err := strconv.ParseBool(v)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("invalid TTSBUDDY_ALLOW_CUSTOM_API_URL=%q, using default %v", v, r.AllowCustomAPIURL))
	} else {
		r.AllowCustomAPIURL = allow
	}
}
```

- [ ] **Step 3: Implement the credentialed API URL trust gate**

In `internal/config/config.go`, replace the body of `CheckInsecureURL` with a compatibility wrapper and add the stricter checker below it:

```go
// CheckInsecureURL returns an error if the URL would send credentials over
// insecure HTTP to a non-localhost host. Returns nil if safe.
func CheckInsecureURL(rawURL string) error {
	return CheckCredentialedAPIURL(rawURL, true)
}

// CheckCredentialedAPIURL returns an error if a configured API URL is not a
// trusted destination for bearer credentials.
func CheckCredentialedAPIURL(rawURL string, allowCustom bool) error {
	u, err := url.Parse(rawURL)
	if err != nil || rawURL == "" || u.Scheme == "" || u.Host == "" {
		return nil
	}

	host := strings.ToLower(u.Hostname())
	if isLocalAPIHost(host) {
		return nil
	}

	switch u.Scheme {
	case "http":
		return fmt.Errorf("refusing to send API key over insecure HTTP to %s; use HTTPS or localhost", host)
	case "https":
		if isOfficialAPIHost(host) || allowCustom {
			return nil
		}
		return fmt.Errorf("refusing to send API key to custom API URL host %q; set allow_custom_api_url=true or TTSBUDDY_ALLOW_CUSTOM_API_URL=true to opt in", host)
	default:
		return fmt.Errorf("refusing to send API key to unsupported API URL scheme %q", u.Scheme)
	}
}

func isOfficialAPIHost(host string) bool {
	return host == "ttsbuddy.com" || host == "www.ttsbuddy.com"
}

func isLocalAPIHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
```

Keep `CheckInsecureURL` for any existing tests or callers, but move the production root command to the stricter API.

- [ ] **Step 4: Wire the trust gate into credentialed network commands**

In `cmd/root.go`, replace:

```go
if err := config.CheckInsecureURL(resolvedCfg.APIURL); err != nil {
	return err
}
```

with:

```go
if commandUsesCredentialedAPI(cmd) {
	if err := config.CheckCredentialedAPIURL(resolvedCfg.APIURL, resolvedCfg.AllowCustomAPIURL); err != nil {
		return err
	}
}
```

Add this helper near the root command setup:

```go
func commandUsesCredentialedAPI(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "speak", "web", "status":
			return true
		}
	}
	return false
}
```

Keep `config` commands usable even when the current config contains a custom API URL. `config` does not send the API key over the network, and users need it to enable or disable `allow_custom_api_url`.

- [ ] **Step 5: Expose the opt-in through `ttsbuddy config`**

In `cmd/config.go`, update the valid-keys text in both error/help strings to include `allow_custom_api_url`.

Add plain config output:

```go
_, _ = fmt.Fprintf(os.Stdout, "%-20s %t\n", "allow_custom_api_url:", resolved.AllowCustomAPIURL)
```

Add this case to `getResolvedValue`:

```go
case "allow_custom_api_url":
	return fmt.Sprintf("%t", r.AllowCustomAPIURL)
```

- [ ] **Step 6: Add failing command-level tests**

Append to `cmd/root_test.go`:

```go
func TestCustomHTTPSAPIURLDeniedBeforeNetworkCommand(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "https://api.example.com/v1/agent-tts", "ttsb_test_key"), "speak", "hello")
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
	r := runCLI(t, envForTest(home, "https://api.example.com/v1/agent-tts", "ttsb_test_key"), "config", "set", "allow_custom_api_url", "true")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stderr, "allow_custom_api_url set: true", "stderr")
}
```

Append to `cmd/config_test.go`:

```go
func TestConfigSetAllowCustomAPIURL(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", ""), "config", "set", "allow_custom_api_url", "true")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stderr, "allow_custom_api_url set: true", "stderr")

	r = runCLI(t, envForTest(home, "", ""), "config", "get", "allow_custom_api_url")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "true", "stdout")
}

func TestConfigSetAllowCustomAPIURLInvalid(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", ""), "config", "set", "allow_custom_api_url", "sometimes")
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "invalid allow_custom_api_url", "stderr")
}
```

Run:

```bash
go test ./internal/config ./cmd -run 'TestCheckCredentialedAPIURL|TestResolveAllowCustomAPIURL|TestCustomHTTPSAPIURL|TestConfigSetAllowCustomAPIURL|TestConfigSetAllowCustomAPIURLBypassesCredentialedAPIGate' -count=1
```

Expected after implementation: pass.

- [ ] **Step 7: Update README configuration docs**

In `README.md`, add a row to the configuration table:

```markdown
| Allow custom API URL | `TTSBUDDY_ALLOW_CUSTOM_API_URL` | — | `false` |
```

Add this note below the table:

```markdown
By default, credentialed commands may use the production TTSBuddy hosts or localhost development endpoints. Sending an API key to any other HTTPS API host requires explicit opt-in with `ttsbuddy config set allow_custom_api_url true` or `TTSBUDDY_ALLOW_CUSTOM_API_URL=true`.
```

---

### Task 2: Fix Finding 2, Demo Script Prints Caller-Provided API Keys

**Files:**
- Modify: `demo/cli-demo.sh`
- Modify: `demo/README.md`
- Test: `cmd/demo_script_test.go`

- [ ] **Step 1: Add a failing no-network regression test**

Create `cmd/demo_script_test.go`:

```go
package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemoScriptDoesNotLeakInheritedAPIKey(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(repoRoot, "out")
	_ = os.RemoveAll(outDir)
	t.Cleanup(func() { _ = os.RemoveAll(outDir) })

	binDir := t.TempDir()
	stubPath := filepath.Join(binDir, "ttsbuddy")
	stub := `#!/usr/bin/env bash
set -euo pipefail
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
	if err := os.WriteFile(stubPath, []byte(stub), 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", filepath.Join(repoRoot, "demo", "cli-demo.sh"))
	cmd.Env = []string{
		"HOME=" + t.TempDir(),
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TTSBUDDY_API_KEY=ttsb_real_secretvalue",
		"TTSBUDDY_API_URL=https://api.example.com/v1/agent-tts",
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("demo script failed: %v\n%s", err, output)
	}

	text := string(output)
	if strings.Contains(text, "ttsb_real_secretvalue") || strings.Contains(text, "secretvalue") {
		t.Fatalf("demo output leaked inherited key:\n%s", text)
	}
	if strings.Contains(text, "https://api.example.com/v1/agent-tts") {
		t.Fatalf("demo output leaked inherited custom API URL:\n%s", text)
	}
	if !strings.Contains(text, "ttsb_demo_...") {
		t.Fatalf("demo output should show only the redacted demo key, got:\n%s", text)
	}
}
```

Run:

```bash
go test ./cmd -run TestDemoScriptDoesNotLeakInheritedAPIKey -count=1
```

Expected before the fix: fails because `demo/cli-demo.sh` prints the inherited full key and inherited URL.

- [ ] **Step 2: Make demo mode ignore inherited real credentials by default**

Replace lines 7-8 in `demo/cli-demo.sh` with:

```bash
DEMO_API_URL="https://www.ttsbuddy.com/v1/cli-demo"
DEMO_API_KEY="ttsb_demo_cli"

redact_api_key() {
  local key="${1:-}"
  if [[ -z "$key" ]]; then
    printf "(none)"
    return
  fi
  if [[ "$key" == ttsb_*_* ]]; then
    local rest="${key#ttsb_}"
    local public_id="${rest%%_*}"
    printf "ttsb_%s_..." "$public_id"
    return
  fi
  printf "***"
}

if [[ "${TTSBUDDY_DEMO_USE_REAL_KEY:-}" == "1" ]]; then
  : "${TTSBUDDY_API_KEY:?TTSBUDDY_API_KEY is required when TTSBUDDY_DEMO_USE_REAL_KEY=1}"
  export TTSBUDDY_API_URL="${TTSBUDDY_API_URL:-https://www.ttsbuddy.com/v1/agent-tts}"
else
  export TTSBUDDY_API_URL="$DEMO_API_URL"
  export TTSBUDDY_API_KEY="$DEMO_API_KEY"
fi
```

Replace:

```bash
echo "API key: $TTSBUDDY_API_KEY"
```

with:

```bash
echo "API key: $(redact_api_key "$TTSBUDDY_API_KEY")"
```

- [ ] **Step 3: Update demo README**

In `demo/README.md`, document the behavior:

```markdown
`demo/cli-demo.sh` runs in constrained demo mode by default. It ignores inherited `TTSBUDDY_API_KEY` and `TTSBUDDY_API_URL`, uses the demo endpoint/key, and prints only a redacted key. To intentionally test the script with a real key, run it with `TTSBUDDY_DEMO_USE_REAL_KEY=1` and an explicit `TTSBUDDY_API_KEY`.
```

- [ ] **Step 4: Verify the demo script fix**

Run:

```bash
bash -n demo/cli-demo.sh
go test ./cmd -run TestDemoScriptDoesNotLeakInheritedAPIKey -count=1
```

Expected: both pass, and no `out/` directory remains after the Go test cleanup.

---

### Task 3: Fix Finding 3, Failed Download Diagnostics Expose Signed Audio URLs

**Files:**
- Modify: `cmd/speak.go`
- Test: `cmd/speak_test.go`

- [ ] **Step 1: Add a failing regression test**

Append this test near the existing audio URL security tests in `cmd/speak_test.go`:

```go
func TestSpeakDownloadFailureRedactsSignedAudioURL(t *testing.T) {
	audioSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not available", http.StatusTeapot)
	}))
	signedURL := audioSrv + "/audio.mp3?X-Amz-Signature=supersecret&token=abc#frag"

	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"status":    "completed",
			"job_id":    "j1",
			"audio_url": signedURL,
			"meta":      map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))

	home := t.TempDir()
	out := filepath.Join(t.TempDir(), "audio.mp3")
	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "-o", out, "--idempotency-key", "fixed")
	assertExitCode(t, r, 1)
	assertContains(t, r.Stderr, "Download failed:", "stderr")
	assertContains(t, r.Stderr, "Audio URL: "+audioSrv+"/audio.mp3", "stderr")
	assertNotContains(t, r.Stderr, signedURL, "stderr")
	assertNotContains(t, r.Stderr, "X-Amz-Signature", "stderr")
	assertNotContains(t, r.Stderr, "supersecret", "stderr")
	assertNotContains(t, r.Stderr, "token=abc", "stderr")
	assertNotContains(t, r.Stderr, "#frag", "stderr")
}
```

Run:

```bash
go test ./cmd -run TestSpeakDownloadFailureRedactsSignedAudioURL -count=1
```

Expected before the fix: fails because `printDownloadFailure` includes the full signed URL.

- [ ] **Step 2: Redact URL diagnostics at the output boundary**

In `cmd/speak.go`, add this helper near `printDownloadFailure`:

```go
func redactURLForDisplay(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := urlPkg.Parse(rawURL)
	if err != nil {
		return "(invalid URL)"
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	return u.String()
}
```

Change `printDownloadFailure` from:

```go
fmt.Fprintf(os.Stderr, "Download failed: %v\nAudio URL: %s\n", dl.err, dl.audioURL)
```

to:

```go
fmt.Fprintf(os.Stderr, "Download failed: %v\nAudio URL: %s\n", dl.err, redactURLForDisplay(dl.audioURL))
```

Do not change `downloadFailure.audioURL`; retry and download logic must keep the original raw URL internally.

- [ ] **Step 3: Verify no intentional URL outputs were changed**

Run:

```bash
go test ./cmd -run 'TestSpeakDownloadFailureRedactsSignedAudioURL|TestSpeakNoDownload|TestSpeakJSON|TestSpeakMissingAudioURL|TestSpeakRejectsMalformedAudioURLWithoutLeakingRawURL' -count=1
```

Expected: pass. `--no-download` and `--json` keep their intentional audio URL behavior; only failure diagnostics redact query and fragment.

---

### Task 4: Fix Finding 4, Subprocess Test Harness Inherits TTSBUDDY Secrets

**Files:**
- Modify: `cmd/testutil_test.go`
- Test: `cmd/root_test.go`

- [ ] **Step 1: Add a failing regression test**

Append to `cmd/root_test.go`:

```go
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
```

Add imports to `cmd/root_test.go`:

```go
	"net/http"
	"sync/atomic"
```

Run:

```bash
go test ./cmd -run TestRunCLIDoesNotInheritTTSBuddyCredentials -count=1
```

Expected before the fix: fails because `runCLI` appends `os.Environ()` and the child sees the parent `TTSBUDDY_API_KEY`.

- [ ] **Step 2: Filter inherited TTSBuddy-related env vars from subprocess tests**

In `cmd/testutil_test.go`, replace:

```go
cmd.Env = append(os.Environ(), "TTSBUDDY_TEST_HELPER=1")
cmd.Env = append(cmd.Env, env...)
```

with:

```go
cmd.Env = subprocessTestEnv(env)
```

Add this helper near `runCLI`:

```go
func subprocessTestEnv(overrides []string) []string {
	filtered := make([]string, 0, len(os.Environ())+len(overrides)+1)
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "TTSBUDDY_") || key == "TB_HOME" {
			continue
		}
		filtered = append(filtered, kv)
	}
	filtered = append(filtered, "TTSBUDDY_TEST_HELPER=1")
	filtered = append(filtered, overrides...)
	return filtered
}
```

This preserves normal OS environment needed by the Go test subprocess while clearing inherited TTSBuddy secrets. Tests can still pass explicit `TTSBUDDY_*` overrides through `envForTest` or direct `append(...)`.

- [ ] **Step 3: Verify explicit test env still works**

Run:

```bash
go test ./cmd -run 'TestRunCLIDoesNotInheritTTSBuddyCredentials|TestSpeakSuccess|TestConfigShowResolved|TestWebCommand' -count=1
```

Expected: pass. Explicit `envForTest` values still reach the subprocess; only accidental inherited values are removed.

---

### Task 5: Documentation, Cross-Finding Review, and Local Verification

**Files:**
- Modify only if needed: `README.md`
- Modify only if needed: `demo/README.md`
- Verify: all modified files

- [ ] **Step 1: Run targeted regression tests**

```bash
go test ./internal/config ./cmd -run 'TestCheckCredentialedAPIURL|TestResolveAllowCustomAPIURL|TestCustomHTTPSAPIURL|TestConfigSetAllowCustomAPIURL|TestDemoScriptDoesNotLeakInheritedAPIKey|TestSpeakDownloadFailureRedactsSignedAudioURL|TestRunCLIDoesNotInheritTTSBuddyCredentials' -count=1
```

Expected: pass.

- [ ] **Step 2: Run the full deterministic Go suite**

```bash
make test
```

Expected: `go test -race -count=1 ./...` exits 0.

- [ ] **Step 3: Run lint and repository policy checks**

```bash
make lint
make check-actions-pinned
```

Expected: both exit 0.

- [ ] **Step 4: Build and snapshot release locally**

```bash
make build
make release-snapshot
```

Expected: both exit 0 and `dist/` contains local snapshot artifacts.

- [ ] **Step 5: Manual source review checklist**

Review the diff with:

```bash
git diff --check
git diff --stat
git diff
```

Confirm:

- No API key or temporary token was committed.
- Custom HTTPS API URLs are denied by default when an API key is present.
- Localhost HTTP endpoints still work for tests and local development.
- The demo script cannot print inherited real keys in default demo mode.
- Download failure diagnostics strip query strings and fragments.
- Test subprocesses still receive explicit `envForTest` overrides.
- Documentation explains the custom API URL opt-in and demo key behavior.

---

### Task 6: Fresh Security Scan, Production Acceptance, and Conditional v0.8.0 Release

**Files:**
- No source edits unless verification finds a regression.

- [ ] **Step 1: Run a fresh Codex Security scan**

Use `codex-security:security-scan` against the updated branch.

Expected:

- The four fixed findings are gone or marked remediated by evidence.
- Any new findings are triaged before release.
- Save the final artifact paths in the release notes or final handoff.

- [ ] **Step 2: Run production acceptance against the local build**

Build first:

```bash
make build
```

In the shell only, set the temporary API key supplied in the current chat:

```bash
export TTSBUDDY_TEMP_API_KEY
export TTSBUDDY_API_KEY="$TTSBUDDY_TEMP_API_KEY"
```

Run the live acceptance suite with isolated config:

```bash
TTSBUDDY_API_KEY="$TTSBUDDY_TEMP_API_KEY" BINARY=bin/ttsbuddy POST_DELAY=65 ./tests/acceptance_test.sh
```

Expected: all non-skipped acceptance tests pass. If a live POST receives a transient `503` or a webpage job times out, rerun the exact failing case once with the same binary before treating it as a CLI regression.

- [ ] **Step 3: Run one explicit production smoke query**

Use isolated state and the local binary:

```bash
TB_HOME="$(mktemp -d)"
HOME="$TB_HOME" TTSBUDDY_API_KEY="$TTSBUDDY_TEMP_API_KEY" bin/ttsbuddy speak "TTSBuddy CLI v0.8.0 security smoke test." --voice st_m1 --language en --no-download --timeout 10m
```

Expected: exit 0 with a production audio URL and no credential leakage. Remove the temp home after recording the result.

- [ ] **Step 4: Merge to main only after all gates pass**

```bash
git status --short
git checkout main
git merge --ff-only codex/fix-security-scan-followups-v0.8.0
git push origin main
```

Expected: clean status before merge, fast-forward merge, push succeeds.

- [ ] **Step 5: Create release `v0.8.0` only if testing looks OK**

```bash
git tag -a v0.8.0 -m "v0.8.0"
git push origin v0.8.0
```

Expected: GitHub Actions Release workflow starts for tag `v0.8.0`, GoReleaser publishes GitHub release assets, and the Homebrew tap formula is updated.

Verify release:

```bash
gh release view v0.8.0 --repo ngelik/ttsbuddy-cli
gh run list --repo ngelik/ttsbuddy-cli --workflow Release --limit 5
```

Expected: release exists and the latest Release workflow for `v0.8.0` succeeded.

- [ ] **Step 6: Update the installed CLI via Homebrew and re-test production**

```bash
brew update
brew upgrade ngelik/tap/ttsbuddy
ttsbuddy version
```

Expected: installed `ttsbuddy` reports `v0.8.0`.

Run installed-binary acceptance:

```bash
TTSBUDDY_API_KEY="$TTSBUDDY_TEMP_API_KEY" BINARY=ttsbuddy POST_DELAY=65 ./tests/acceptance_test.sh
```

Expected: all non-skipped acceptance tests pass against the Homebrew-installed binary.

- [ ] **Step 7: Final release handoff**

Report:

- Commit SHA merged to `main`.
- Tag SHA for `v0.8.0`.
- GitHub release URL.
- Homebrew formula update confirmation.
- Installed `ttsbuddy version` output.
- Local test, lint, snapshot, security scan, local production acceptance, and Homebrew production acceptance results.

Do not claim the release is complete unless every command above has fresh passing output.
