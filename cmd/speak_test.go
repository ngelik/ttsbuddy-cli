package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
)

// --- In-process helper tests ---

func TestReadInputPositionalArg(t *testing.T) {
	text, file, fromStdin, err := readInput([]string{"hello world"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello world" {
		t.Errorf("text: got %q", text)
	}
	if file != "" {
		t.Errorf("file should be empty, got %q", file)
	}
	if fromStdin {
		t.Error("fromStdin should be false for positional arg")
	}
}

func TestReadInputFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.txt")
	_ = os.WriteFile(path, []byte("file content"), 0600)

	text, file, fromStdin, err := readInput([]string{}, path)
	if err != nil {
		t.Fatal(err)
	}
	if text != "file content" {
		t.Errorf("text: got %q", text)
	}
	if file != path {
		t.Errorf("file: got %q", file)
	}
	if fromStdin {
		t.Error("fromStdin should be false")
	}
}

func TestReadInputFileNotFound(t *testing.T) {
	_, _, _, err := readInput([]string{}, "/nonexistent/path.txt")
	if err == nil {
		t.Fatal("expected error")
	}
	e, ok := err.(*exitError)
	if !ok || e.code != 2 {
		t.Errorf("expected exit 2, got %+v", err)
	}
}

func TestReadInputDirectory(t *testing.T) {
	dir := t.TempDir()
	_, _, _, err := readInput([]string{}, dir)
	if err == nil {
		t.Fatal("expected error for directory")
	}
	e, ok := err.(*exitError)
	if !ok || e.code != 2 {
		t.Errorf("expected exit 2, got %+v", err)
	}
	if !strings.Contains(e.msg, "not a regular file") {
		t.Errorf("expected 'not a regular file' message, got %q", e.msg)
	}
}

func TestReadInputAmbiguous(t *testing.T) {
	_, _, _, err := readInput([]string{"text"}, "file.txt")
	if err == nil {
		t.Fatal("expected error for ambiguous input")
	}
	e, ok := err.(*exitError)
	if !ok || e.code != 2 {
		t.Errorf("expected exit 2, got %+v", err)
	}
}

func TestReadBoundedUnderLimit(t *testing.T) {
	r := strings.NewReader("small input")
	text, err := readBounded(r)
	if err != nil {
		t.Fatal(err)
	}
	if text != "small input" {
		t.Errorf("got %q", text)
	}
}

func TestReadBoundedOverLimit(t *testing.T) {
	// Create data larger than maxInputSize
	big := strings.Repeat("a", maxInputSize+100)
	r := strings.NewReader(big)
	_, err := readBounded(r)
	if err == nil {
		t.Fatal("expected error for over-limit input")
	}
}

func TestIsMarkdownFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"test.md", true},
		{"test.markdown", true},
		{"test.MD", true},
		{"test.txt", false},
		{"test.pdf", false},
		{"test", false},
	}
	for _, tc := range cases {
		got := isMarkdownFile(tc.path)
		if got != tc.want {
			t.Errorf("isMarkdownFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestIsPermanentErrorAll(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{400, true},
		{401, true},
		{403, true},
		{404, true},
		{429, false},
		{500, false},
		{502, false},
		{200, false},
	}
	for _, tc := range cases {
		got := isPermanentError(nil, tc.status)
		if got != tc.want {
			t.Errorf("isPermanentError(_, %d) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestHandleAPIErrorAllCodes(t *testing.T) {
	codes := []struct {
		code     string
		status   int
		exitCode int
		contains string
	}{
		{api.ErrInvalidKey, 401, 1, "api key"},
		{api.ErrInactiveSubscription, 403, 1, "subscription"},
		{api.ErrNoAPIAccess, 403, 1, "api access"},
		{api.ErrUsageLimitExceeded, 403, 1, "minutes"},
		{api.ErrTextTooLong, 400, 2, "500,000"},
		{api.ErrRateLimited, 429, 1, "rate limited"},
		{api.ErrForbidden, 403, 1, "denied"},
	}
	for _, tc := range codes {
		err := handleAPIError(&api.APIResponseError{
			StatusCode: tc.status,
			Response:   api.TTSResponse{Error: &api.APIError{Code: tc.code, Message: "test"}},
		}, tc.status)
		e, ok := err.(*exitError)
		if !ok {
			t.Errorf("code %s: expected *exitError, got %T", tc.code, err)
			continue
		}
		if e.code != tc.exitCode {
			t.Errorf("code %s: exit %d, want %d", tc.code, e.code, tc.exitCode)
		}
		if !strings.Contains(strings.ToLower(e.msg), tc.contains) {
			t.Errorf("code %s: message %q should contain %q", tc.code, e.msg, tc.contains)
		}
	}
}

// --- Subprocess tests ---

func TestSpeakNoInput(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", "ttsb_test_key"), "speak")
	assertExitCode(t, r, 2)
}

func TestSpeakJSONAndDashO(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", "ttsb_test_key"), "speak", "--json", "-o", "-", "test")
	assertExitCode(t, r, 2)
}

func TestSpeakNoAPIKey(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", ""), "speak", "hello")
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "API key", "stderr")
}

func TestSpeakInline(t *testing.T) {
	audioSrv := startMockAPI(t, mockAudioHandler())
	apiSrv := startMockAPI(t, mockCompletedHandler(audioSrv))
	home := t.TempDir()
	out := filepath.Join(home, "out.mp3")

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "-o", out)
	assertExitCode(t, r, 0)

	if _, err := os.Stat(out); os.IsNotExist(err) {
		t.Error("output file should exist")
	}
}

func TestSpeakJSON(t *testing.T) {
	audioSrv := startMockAPI(t, mockAudioHandler())
	apiSrv := startMockAPI(t, mockCompletedHandler(audioSrv))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "--json")
	assertExitCode(t, r, 0)
	assertValidJSON(t, r.Stdout)
	assertContains(t, r.Stdout, "completed", "stdout")
	// --json should not produce human output on stderr
	if r.Stderr != "" {
		t.Errorf("--json should not write to stderr, got: %q", r.Stderr)
	}
}

func TestSpeakNoDownload(t *testing.T) {
	audioSrv := startMockAPI(t, mockAudioHandler())
	apiSrv := startMockAPI(t, mockCompletedHandler(audioSrv))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "--no-download")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stderr, audioSrv, "stderr should contain audio URL")
}

func TestSpeakAuthError(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   map[string]string{"code": "INVALID_KEY", "message": "bad key"},
			"meta":    map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_bad_key"), "speak", "hello")
	assertExitCode(t, r, 1)
	assertContains(t, r.Stderr, "API key", "stderr")
}

func TestSpeakExpired(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"status":  "expired",
			"job_id":  "j1",
			"error":   map[string]string{"code": "FILE_EXPIRED", "message": "expired"},
			"meta":    map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello")
	assertExitCode(t, r, 1)
	assertContains(t, r.Stderr, "expired", "stderr")
}

func TestSpeakMissingAudioURL(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"status":  "completed",
			"job_id":  "j1",
			// No audio_url
			"meta": map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	// --no-download should still fail
	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "--no-download")
	assertExitCode(t, r, 1)
	assertContains(t, r.Stderr, "no audio URL", "stderr")

	// --json should also fail (audio_url check is now before JSON emit)
	r2 := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "--json")
	assertExitCode(t, r2, 1)
	assertValidJSON(t, r2.Stdout) // Execute() wraps error as CLI_ERROR JSON
	assertContains(t, r2.Stdout, "CLI_ERROR", "stdout")
}

func TestSpeakAsync(t *testing.T) {
	calls := 0
	audioSrv := startMockAPI(t, mockAudioHandler())
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == "POST" {
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success":             true,
				"status":              "processing",
				"job_id":              "async-job",
				"status_url":          "/v1/agent-tts?id=async-job",
				"retry_after_seconds": 0,
				"meta":                map[string]string{"request_id": "r1", "api_version": "2026-04"},
			})
			return
		}
		// GET - return completed on second poll
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"status":    "completed",
			"job_id":    "async-job",
			"audio_url": audioSrv + "/audio.mp3",
			"meta":      map[string]string{"request_id": "r2", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()
	out := filepath.Join(home, "async.mp3")

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "-o", out, "--timeout", "30s")
	assertExitCode(t, r, 0)
	if _, err := os.Stat(out); os.IsNotExist(err) {
		t.Error("output file should exist after async completion")
	}
}

func TestSpeakMarkdownStrip(t *testing.T) {
	var receivedBody string
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"status":    "completed",
			"job_id":    "md-job",
			"audio_url": "https://example.com/audio.mp3",
			"meta":      map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()
	mdFile := filepath.Join(home, "test.md")
	_ = os.WriteFile(mdFile, []byte("# Hello\n\nThis is **bold** text."), 0600)

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "-f", mdFile, "--no-download")
	assertExitCode(t, r, 0)

	// The sent text should not contain markdown syntax
	if strings.Contains(receivedBody, "# Hello") {
		t.Error("markdown heading should have been stripped")
	}
	if strings.Contains(receivedBody, "**") {
		t.Error("bold markers should have been stripped")
	}
}

func TestSpeakSupertonicLanguageFlag(t *testing.T) {
	var received map[string]interface{}
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Fatalf("decoding request body: %v", err)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"status":    "completed",
			"job_id":    "lang-job",
			"audio_url": "https://example.com/audio.mp3",
			"meta":      map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "-v", "st_m1", "--language", "fr", "bonjour", "--no-download")
	assertExitCode(t, r, 0)

	if received["voice"] != "st_m1" {
		t.Fatalf("voice: got %v", received["voice"])
	}
	if received["language"] != "fr" {
		t.Fatalf("language: got %v", received["language"])
	}
}

func TestSpeakConfiguredSupertonicLanguage(t *testing.T) {
	var received map[string]interface{}
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Fatalf("decoding request body: %v", err)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"status":    "completed",
			"job_id":    "lang-job",
			"audio_url": "https://example.com/audio.mp3",
			"meta":      map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, append(envForTest(home, apiSrv, "ttsb_test_key"), "TTSBUDDY_LANGUAGE=de"), "speak", "-v", "st_m1", "hallo", "--no-download")
	assertExitCode(t, r, 0)

	if received["language"] != "de" {
		t.Fatalf("language: got %v", received["language"])
	}
}

func TestSpeakRejectsLanguageForKokoroVoice(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "https://example.com/v1/agent-tts", "ttsb_test_key"), "speak", "-v", "ff_siwis", "--language", "fr", "bonjour")
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "Supertonic", "stderr")
}

// --- Regression tests ---

func TestSpeakNoDownloadMissingURL(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true, "status": "completed", "job_id": "j1",
			"meta": map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "--no-download")
	assertExitCode(t, r, 1)
}

func TestSpeakSavesLastJob(t *testing.T) {
	audioSrv := startMockAPI(t, mockAudioHandler())
	apiSrv := startMockAPI(t, mockCompletedHandler(audioSrv))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "--no-download")
	assertExitCode(t, r, 0)

	// last_job.json should exist
	ljPath := filepath.Join(home, ".ttsbuddy", "last_job.json")
	if _, err := os.Stat(ljPath); os.IsNotExist(err) {
		t.Error("last_job.json should be created after successful speak")
	}
}

func TestSpeakUnknownPollStatus(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true, "status": "processing", "job_id": "j1",
				"retry_after_seconds": 0,
				"meta":                map[string]string{"request_id": "r1", "api_version": "2026-04"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true, "status": "new_thing_never_seen", "job_id": "j1",
			"meta": map[string]string{"request_id": "r2", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "--timeout", "5s")
	assertExitCode(t, r, 1)
	assertContains(t, r.Stderr, "unexpected", "stderr")
}

// --- Stdout download tests (subprocess) ---

func TestSpeakDashOStdout(t *testing.T) {
	audioSrv := startMockAPI(t, mockAudioHandler())
	apiSrv := startMockAPI(t, mockCompletedHandler(audioSrv))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "-o", "-")
	assertExitCode(t, r, 0)
	// stdout should contain the audio bytes
	if !strings.Contains(r.Stdout, "fake-mp3-data") {
		t.Errorf("stdout should contain audio data, got %d bytes", len(r.Stdout))
	}
}

// --- Additional in-process helper tests ---

func TestMinDuration(t *testing.T) {
	if minDuration(3*time.Second, 5*time.Second) != 3*time.Second {
		t.Error("should return smaller")
	}
	if minDuration(10*time.Second, 5*time.Second) != 5*time.Second {
		t.Error("should return smaller")
	}
}

func TestGetResolvedValueAllKeys(t *testing.T) {
	r := &config.ResolvedConfig{
		APIKey:        "ttsb_abc_secret",
		Voice:         "bf_emma",
		Speed:         0.8,
		PollTimeout:   "5m",
		OutputDir:     "/tmp",
		APIURL:        "https://api.example.com",
		TTSAPIBaseURL: "https://tts.example.com",
	}
	cases := []struct {
		key  string
		want string
	}{
		{"key", "ttsb_abc_..."},
		{"api_key", "ttsb_abc_..."},
		{"voice", "bf_emma"},
		{"speed", "0.8"},
		{"timeout", "5m"},
		{"output_dir", "/tmp"},
		{"api_url", "https://api.example.com"},
		{"tts_api_base_url", "https://tts.example.com"},
		{"nonexistent", ""},
	}
	for _, tc := range cases {
		got := getResolvedValue(r, tc.key)
		if got != tc.want {
			t.Errorf("getResolvedValue(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// --- More polling/error coverage ---

func TestSpeakAsyncTimeout(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true, "status": "processing", "job_id": "slow-job",
				"retry_after_seconds": 0,
				"meta":                map[string]string{"request_id": "r1", "api_version": "2026-04"},
			})
			return
		}
		// Always return processing
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true, "status": "processing", "job_id": "slow-job",
			"retry_after_seconds": 0,
			"meta":                map[string]string{"request_id": "r2", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "--timeout", "2s")
	assertExitCode(t, r, 1)
	assertContains(t, r.Stderr, "timed out", "stderr")
}

func TestSpeakPollPermanentError(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true, "status": "processing", "job_id": "perm-job",
				"retry_after_seconds": 0,
				"meta":                map[string]string{"request_id": "r1", "api_version": "2026-04"},
			})
			return
		}
		// GET returns 401 (permanent)
		w.WriteHeader(401)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   map[string]string{"code": "INVALID_KEY", "message": "bad key"},
			"meta":    map[string]string{"request_id": "r2", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "--timeout", "30s")
	assertExitCode(t, r, 1)
	// Should fail fast, not spin for 30s
	assertContains(t, r.Stderr, "API key", "stderr")
}

func TestSpeakQuietMode(t *testing.T) {
	audioSrv := startMockAPI(t, mockAudioHandler())
	apiSrv := startMockAPI(t, mockCompletedHandler(audioSrv))
	home := t.TempDir()
	out := filepath.Join(home, "quiet.mp3")

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "-o", out, "--quiet")
	assertExitCode(t, r, 0)
	if r.Stderr != "" {
		t.Errorf("--quiet should suppress stderr, got: %q", r.Stderr)
	}
}

func TestStatusPollPermanentError(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   map[string]string{"code": "NOT_FOUND", "message": "gone"},
			"meta":    map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "status", "gone-job", "--watch", "--timeout", "30s")
	assertExitCode(t, r, 1)
}

// Ensure speak uses the config module correctly
func init() {
	_ = config.DefaultVoice // import used
}
