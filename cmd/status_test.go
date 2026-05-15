package cmd

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestStatusExplicitID(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"status":    "completed",
			"job_id":    "test-job",
			"audio_url": "https://example.com/audio.mp3",
			"audio":     map[string]interface{}{"format": "mp3", "duration_seconds": 5.0},
			"meta":      map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "status", "test-job")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stderr, "completed", "stderr")
	assertContains(t, r.Stderr, "audio.mp3", "stderr")
}

func TestStatusLastJob(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true, "status": "completed", "job_id": "saved-job",
			"audio_url": "https://example.com/a.mp3",
			"meta":      map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	// Create last_job.json
	dir := filepath.Join(home, ".ttsbuddy")
	_ = os.MkdirAll(dir, 0700)
	lj := `{"job_id":"saved-job","timestamp":"2026-04-05T12:00:00Z"}`
	_ = os.WriteFile(filepath.Join(dir, "last_job.json"), []byte(lj), 0600)

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "status")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stderr, "saved-job", "stderr")
}

func TestStatusNoJobNoLastJob(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, envForTest(home, "", "ttsb_test_key"), "status")
	assertExitCode(t, r, 2)
}

func TestStatus404(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   map[string]string{"code": "NOT_FOUND", "message": "Job not found"},
			"meta":    map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "status", "nonexistent")
	assertExitCode(t, r, 1)
}

func TestStatusProcessing(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":             true,
			"status":              "processing",
			"job_id":              "proc-job",
			"retry_after_seconds": 5,
			"meta":                map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "status", "proc-job")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stderr, "still processing", "stderr")
}

func TestStatusFailed(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false, "status": "failed", "job_id": "fail-job",
			"error": map[string]string{"code": "TTS_PROVIDER_ERROR", "message": "failed"},
			"meta":  map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "status", "fail-job")
	assertExitCode(t, r, 1)
}

func TestStatusExpired(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false, "status": "expired", "job_id": "exp-job",
			"error": map[string]string{"code": "FILE_EXPIRED", "message": "expired"},
			"meta":  map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "status", "exp-job")
	assertExitCode(t, r, 1)
}

func TestStatusJSON(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"status":    "completed",
			"job_id":    "json-job",
			"audio_url": "https://example.com/a.mp3",
			"meta":      map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "status", "--json", "json-job")
	assertExitCode(t, r, 0)
	assertValidJSON(t, r.Stdout)
	assertContains(t, r.Stdout, "completed", "stdout")
}

func TestStatusUnknownStatus(t *testing.T) {
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true, "status": "mystery_status", "job_id": "mys-job",
			"meta": map[string]string{"request_id": "r1", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "status", "mys-job")
	assertExitCode(t, r, 1)
	assertContains(t, r.Stderr, "unknown status", "stderr")
}

func TestStatusWatchCompletes(t *testing.T) {
	calls := 0
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 2 {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true, "status": "processing", "job_id": "watch-job",
				"retry_after_seconds": 0,
				"progress": map[string]interface{}{
					"phase":   "processing",
					"percent": 42,
					"message": "Processing audio",
				},
				"meta": map[string]string{"request_id": "r1", "api_version": "2026-04"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"status":    "completed",
			"job_id":    "watch-job",
			"audio_url": "https://example.com/a.mp3",
			"stats": map[string]interface{}{
				"speech_length_seconds":       90,
				"file_size_bytes":             4096,
				"generation_chars_per_second": 250,
			},
			"meta": map[string]string{"request_id": "r3", "api_version": "2026-04"},
		})
	}))
	home := t.TempDir()

	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "status", "watch-job", "--watch", "--timeout", "30s")
	assertExitCode(t, r, 0)
	assertContains(t, r.Stderr, "Processing 42%", "stderr")
	assertContains(t, r.Stderr, "Speech length: 1m30s", "stderr")
	assertContains(t, r.Stderr, "MP3 size: 4.0 KB", "stderr")
}
