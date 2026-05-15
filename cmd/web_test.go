package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestWebCommandSendsWebpagePayloadWithoutImplicitPreferences(t *testing.T) {
	home := t.TempDir()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body><article><h1>Docs Page</h1><p>Readable article text for the web command test.</p></article></body></html>`))
	}))
	defer page.Close()

	var received map[string]interface{}
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Idempotency-Key"); got == "" {
			t.Fatal("missing idempotency key")
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"status":    "completed",
			"job_id":    "web-job-001",
			"audio_url": "https://example.amazonaws.com/audio.mp3",
			"audio": map[string]interface{}{
				"format": "mp3",
				"voice":  "st_m1",
				"speed":  1.2,
			},
			"stats": map[string]interface{}{
				"speech_length_seconds":       12,
				"file_size_bytes":             1024,
				"generation_chars_per_second": 55,
			},
			"meta": map[string]interface{}{
				"request_id":      "req-web",
				"api_version":     "2026-04",
				"source":          "webpage",
				"source_language": "en",
				"target_language": "ru",
				"translated":      true,
			},
		})
	}))

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "web", page.URL, "--no-download")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stderr, "Extracted \"Docs Page\"", "stderr")
	assertContains(t, r.Stderr, "Translated en -> ru", "stderr")
	assertContains(t, r.Stderr, "Speech length", "stderr")

	if received["source"] != "webpage" {
		t.Fatalf("source = %v, want webpage", received["source"])
	}
	if received["webpage"] != page.URL {
		t.Fatalf("webpage = %v, want %s", received["webpage"], page.URL)
	}
	if _, ok := received["voice"]; ok {
		t.Fatalf("voice should be omitted when --voice is unset: %#v", received)
	}
	if _, ok := received["language"]; ok {
		t.Fatalf("language should be omitted when --language is unset: %#v", received)
	}
	if _, ok := received["speed"]; ok {
		t.Fatalf("speed should be omitted when --speed is unset: %#v", received)
	}
	if received["translate"] != "auto" {
		t.Fatalf("translate = %v, want auto", received["translate"])
	}
}

func TestWebCommandSendsExplicitVoiceLanguageAndSpeed(t *testing.T) {
	home := t.TempDir()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body><article><h1>Docs Page</h1><p>Readable article text for explicit flags.</p></article></body></html>`))
	}))
	defer page.Close()

	var received map[string]interface{}
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"status":    "completed",
			"job_id":    "web-job-002",
			"audio_url": "https://example.amazonaws.com/audio.mp3",
			"audio": map[string]interface{}{
				"format": "mp3",
				"voice":  "st_f3",
				"speed":  1.1,
			},
			"meta": map[string]interface{}{
				"request_id":  "req-web",
				"api_version": "2026-04",
			},
		})
	}))

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "web", page.URL, "--voice", "st_f3", "--language", "ru", "--speed", "1.1", "--no-download")
	assertExitCode(t, r, 0)

	if received["voice"] != "st_f3" {
		t.Fatalf("voice = %v, want st_f3", received["voice"])
	}
	if received["language"] != "ru" {
		t.Fatalf("language = %v, want ru", received["language"])
	}
	if received["speed"] != 1.1 {
		t.Fatalf("speed = %v, want 1.1", received["speed"])
	}
}

func TestWebCommandShowsTranslationMetadataFromAsyncSubmit(t *testing.T) {
	home := t.TempDir()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body><article><h1>Async Page</h1><p>Readable article text for async translation metadata.</p></article></body></html>`))
	}))
	defer page.Close()

	calls := 0
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success":             true,
				"status":              "processing",
				"job_id":              "web-async-job",
				"retry_after_seconds": 0,
				"meta": map[string]interface{}{
					"request_id":      "req-web",
					"api_version":     "2026-04",
					"source":          "webpage",
					"source_language": "en",
					"target_language": "ru",
					"translated":      true,
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"status":    "completed",
			"job_id":    "web-async-job",
			"audio_url": "https://example.amazonaws.com/audio.mp3",
			"meta":      map[string]string{"request_id": "req-web-2", "api_version": "2026-04"},
		})
	}))

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "web", page.URL, "--language", "ru", "--voice", "st_f4", "--no-download", "--timeout", "30s")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stderr, "Translated en -> ru", "stderr")
	if calls < 2 {
		t.Fatalf("expected POST and GET calls, got %d", calls)
	}
}

func TestWebCommandRejectsNonHTTPURL(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "https://example.com/v1/agent-tts", "ttsb_test_key"), "web", "file:///tmp/article.html", "--no-download")
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "http or https", "stderr")
}

func TestWebCommandWritesOutputFile(t *testing.T) {
	home := t.TempDir()
	out := filepath.Join(home, "article.mp3")
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><body><article><h1>Docs Page</h1><p>Readable article text for download.</p></article></body></html>`))
	}))
	defer page.Close()

	audioSrv := httptest.NewServer(mockAudioHandler())
	defer audioSrv.Close()

	apiSrv := startMockAPI(t, mockCompletedHandler(audioSrv.URL))
	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "web", page.URL, "-o", out)
	assertExitCode(t, r, 0)

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != "fake-mp3-data-for-testing" {
		t.Fatalf("unexpected output data: %q", string(data))
	}
}
