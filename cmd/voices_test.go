package cmd

import (
	"net/http"
	"testing"
)

func TestVoicesCurated(t *testing.T) {
	r := runCLI(t, nil, "voices")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "af_heart", "stdout")
	assertContains(t, r.Stdout, "bf_emma", "stdout")
}

func TestVoicesJSON(t *testing.T) {
	r := runCLI(t, nil, "voices", "--json")
	assertExitCode(t, r, 0)
	assertValidJSON(t, r.Stdout)
	assertContains(t, r.Stdout, "af_heart", "stdout")
}

func TestVoicesOfflineNoBrokenConfig(t *testing.T) {
	r := runCLI(t, []string{"HOME=/nonexistent"}, "voices")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "af_heart", "stdout")
}

func TestVoicesAllFallback(t *testing.T) {
	// Point at a server that returns an error
	apiSrv := startMockAPI(t, notFoundHandler())
	home := t.TempDir()

	r := runCLI(t, append(
		envForTest(home, apiSrv, "ttsb_test_key"),
		"TTSBUDDY_TTS_API_BASE_URL=http://127.0.0.1:1", // unreachable
	), "voices", "--all")
	assertExitCode(t, r, 0)
	// Should fall back to curated list
	assertContains(t, r.Stdout, "af_heart", "stdout")
}

func TestVoicesAllJSONNoStderr(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, append(
		envForTest(home, "", "ttsb_test_key"),
		"TTSBUDDY_TTS_API_BASE_URL=http://127.0.0.1:1",
	), "voices", "--all", "--json")
	assertExitCode(t, r, 0)
	assertValidJSON(t, r.Stdout)
	// --json should suppress stderr warning about fallback
	if r.Stderr != "" {
		t.Errorf("--json should suppress stderr, got: %q", r.Stderr)
	}
}

func notFoundHandler() *notFoundH { return &notFoundH{} }

type notFoundH struct{}

func (h *notFoundH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(404)
}
