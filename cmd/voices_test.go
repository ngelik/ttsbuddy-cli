package cmd

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
)

func TestVoicesCurated(t *testing.T) {
	r := runCLI(t, nil, "voices")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "af_heart", "stdout")
	assertContains(t, r.Stdout, "bf_emma", "stdout")
}

func TestVoicesCuratedShowsSupertonicNativeNames(t *testing.T) {
	r := runCLI(t, nil, "voices")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "st_f1", "stdout")
	assertContains(t, r.Stdout, "Ava", "stdout")
	assertContains(t, r.Stdout, "Liam", "stdout")
	assertContains(t, r.Stdout, "Louis", "stdout")
	assertContains(t, r.Stdout, "민준", "stdout")
	assertNotContains(t, r.Stdout, "\tF1\t", "stdout")
	assertNotContains(t, r.Stdout, "\tM1\t", "stdout")
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

func TestVoicesAllJSONFallbackWarning(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, append(
		envForTest(home, "", "ttsb_test_key"),
		"TTSBUDDY_TTS_API_BASE_URL=http://127.0.0.1:1",
	), "voices", "--all", "--json")
	assertExitCode(t, r, 0)
	assertValidJSON(t, r.Stdout)
	assertContains(t, r.Stderr, "catalog may be stale", "stderr")
}

func TestVoicesFilterByLanguageAndEngine(t *testing.T) {
	r := runCLI(t, nil, "voices", "--language", "fr", "--engine", "supertonic", "--json")
	assertExitCode(t, r, 0)
	var voices []api.Voice
	if err := json.Unmarshal([]byte(r.Stdout), &voices); err != nil {
		t.Fatalf("decode filtered voices: %v", err)
	}
	if len(voices) != 10 {
		t.Fatalf("French Supertonic filter returned %d voices, want 10", len(voices))
	}
	for _, voice := range voices {
		if voice.LanguageCode != "fr" || voice.Engine != "supertonic" {
			t.Fatalf("unexpected filtered voice: %+v", voice)
		}
	}
}

func TestVoicesRecommendedReturnsOneValidPair(t *testing.T) {
	r := runCLI(t, nil, "voices", "--language", "en", "--engine", "supertonic", "--recommended", "--json")
	assertExitCode(t, r, 0)
	var voices []api.Voice
	if err := json.Unmarshal([]byte(r.Stdout), &voices); err != nil {
		t.Fatalf("decode recommended voices: %v", err)
	}
	if len(voices) != 1 {
		t.Fatalf("recommended output returned %d voices, want 1", len(voices))
	}
	voice := voices[0]
	if voice.ID != "st_m1" || voice.LanguageCode != "en" || voice.Engine != "supertonic" {
		t.Fatalf("recommended voice = %+v, want st_m1/en/supertonic", voice)
	}
	if voice.MinSpeed == nil || *voice.MinSpeed != api.AcceptedSpeedMin ||
		voice.MaxSpeed == nil || *voice.MaxSpeed != api.AcceptedSpeedMax ||
		voice.RecommendedSpeed == nil || *voice.RecommendedSpeed != api.RecommendedSpeed {
		t.Fatalf("recommended voice has invalid speed metadata: %+v", voice)
	}
}

func TestVoicesUnsupportedFilterReturnsEmptyArray(t *testing.T) {
	r := runCLI(t, nil, "voices", "--language", "zz", "--json")
	assertExitCode(t, r, 0)
	if r.Stdout != "[]\n" {
		t.Fatalf("unsupported filter output = %q, want empty JSON array", r.Stdout)
	}
	if r.Stderr != "" {
		t.Fatalf("JSON empty result should not add diagnostics, got %q", r.Stderr)
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

func TestVoicesAllCanonicalizesSupertonicAliases(t *testing.T) {
	srv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"voices": [
				{
					"voice_id": "st_f1",
					"code": "F1",
					"name": "F1",
					"gender": "female",
					"language": "Multilingual",
					"language_code": "en",
					"engine": "supertonic",
					"supported_language_codes": ["en", "fr"]
				},
				{
					"voice_id": "st_m1",
					"code": "M1",
					"name": "M1",
					"gender": "male",
					"language": "Multilingual",
					"language_code": "en",
					"engine": "supertonic",
					"supported_language_codes": ["en", "fr"]
				}
			]
		}`))
	}))
	r := runCLI(t, []string{
		"TTSBUDDY_TTS_API_BASE_URL=" + srv,
	}, "voices", "--all")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stdout, "st_f1", "stdout")
	assertContains(t, r.Stdout, "Ava", "stdout")
	assertContains(t, r.Stdout, "Liam", "stdout")
	assertContains(t, r.Stdout, "Louis", "stdout")
	assertNotContains(t, r.Stdout, "\tF1\t", "stdout")
	assertNotContains(t, r.Stdout, "\tM1\t", "stdout")
}

func notFoundHandler() *notFoundH { return &notFoundH{} }

type notFoundH struct{}

func (h *notFoundH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(404)
}
