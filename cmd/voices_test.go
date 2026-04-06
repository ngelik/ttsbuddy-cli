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

func TestVoicesAllBrokenHome(t *testing.T) {
	// voices --all should work even with broken HOME
	r := runCLI(t, append(
		[]string{"HOME=/nonexistent"},
		"TTSBUDDY_TTS_API_BASE_URL=http://127.0.0.1:1",
	), "voices", "--all")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "af_heart", "stdout")
}

func TestVoicesAllEnvOverride(t *testing.T) {
	// Should use TTSBUDDY_TTS_API_BASE_URL when set
	srv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"custom_voice","name":"Custom","gender":"Other","language":"Test"}]`))
	}))
	r := runCLI(t, []string{
		"TTSBUDDY_TTS_API_BASE_URL=" + srv,
	}, "voices", "--all")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "custom_voice", "stdout should show live voice")
}

func notFoundHandler() *notFoundH { return &notFoundH{} }

type notFoundH struct{}

func (h *notFoundH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(404)
}
