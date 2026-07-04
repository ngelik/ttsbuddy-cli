package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigShowResolved(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, append(
		envForTest(home, "", "ttsb_test_abc"),
		"TTSBUDDY_VOICE=bf_emma",
	), "config")
	assertExitCode(t, r, 0)
	// Should show resolved value from env
	assertContains(t, r.Stdout, "bf_emma", "stdout")
	// Key should be redacted
	assertContains(t, r.Stdout, "ttsb_test_...", "stdout")
	assertNotContains(t, r.Stdout, "abc", "stdout should not contain secret")
}

func TestConfigJSON(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", "ttsb_test_abc"), "config", "--json")
	assertExitCode(t, r, 0)
	assertValidJSON(t, r.Stdout)

	var cfg map[string]interface{}
	_ = json.Unmarshal([]byte(r.Stdout), &cfg)
	if key, ok := cfg["APIKey"].(string); ok {
		assertContains(t, key, "...", "key should be redacted in JSON")
	}
}

func TestConfigGetVoice(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, append(
		envForTest(home, "", "ttsb_test_key"),
		"TTSBUDDY_VOICE=am_adam",
	), "config", "get", "voice")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "am_adam", "stdout")
}

func TestConfigGetKeyRedacted(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", "ttsb_myid_mysecret"), "config", "get", "key")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "ttsb_myid_...", "stdout")
	assertNotContains(t, r.Stdout, "mysecret", "stdout should not contain secret")
}

func TestConfigSetKey(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", ""), "config", "set", "key", "ttsb_new_secret123")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stderr, "ttsb_new_...", "stderr should show redacted confirmation")

	// Verify persisted
	data, _ := os.ReadFile(filepath.Join(home, ".ttsbuddy", "config.json"))
	assertContains(t, string(data), "ttsb_new_secret123", "config file should contain full key")
}

func TestConfigSetBadKey(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", ""), "config", "set", "key", "badformat")
	assertExitCode(t, r, 2)
}

func TestConfigSetSpeed(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", ""), "config", "set", "speed", "0.8")
	assertExitCode(t, r, 0)
}

func TestConfigSetBadSpeed(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", ""), "config", "set", "speed", "1.0junk")
	assertExitCode(t, r, 2)

	r2 := runCLI(t, envForTest(home, "", ""), "config", "set", "speed", "3.0")
	assertExitCode(t, r2, 2)
}

func TestConfigSetTimeout(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", ""), "config", "set", "timeout", "5m")
	assertExitCode(t, r, 0)
}

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

func TestConfigSetBadTimeout(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", ""), "config", "set", "timeout", "garbage")
	assertExitCode(t, r, 2)
}

func TestConfigSetUnknownKey(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", ""), "config", "set", "bogus", "value")
	assertExitCode(t, r, 2)
}

func TestConfigGetSpeed(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", "ttsb_test_key"), "config", "get", "speed")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "1.2", "stdout")
}

func TestConfigGetTimeout(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", "ttsb_test_key"), "config", "get", "timeout")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "10m", "stdout")
}

func TestConfigGetOutputDir(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", "ttsb_test_key"), "config", "get", "output_dir")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, ".", "stdout")
}

func TestConfigGetApiUrl(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", "ttsb_test_key"), "config", "get", "api_url")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "ttsbuddy.com", "stdout")
}

func TestConfigGetUnknownKey(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", "ttsb_test_key"), "config", "get", "nonexistent")
	assertExitCode(t, r, 2)
}

func TestConfigGetWithEnvOverride(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, append(
		envForTest(home, "", "ttsb_test_key"),
		"TTSBUDDY_VOICE=bf_emma",
	), "config", "get", "voice")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "bf_emma", "stdout should show env override")
}
