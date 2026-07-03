package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ngelik/ttsbuddy-cli/internal/webpage"
)

// TestHelperProcess is the subprocess re-entry point for tests that need
// to exercise os.Exit paths or direct os.Stdout/Stderr writes.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("TTSBUDDY_TEST_HELPER") != "1" {
		return
	}
	// Find the "--" separator, everything after is CLI args
	args := os.Args
	for i, a := range args {
		if a == "--" {
			os.Args = append([]string{"ttsbuddy"}, args[i+1:]...)
			break
		}
	}
	if os.Getenv("TTSBUDDY_TEST_FAKE_WEB_ARTICLE") == "1" {
		fetchArticleForWeb = func(ctx context.Context, rawURL, version string) (*webpage.Article, error) {
			return &webpage.Article{
				URL:   rawURL,
				Title: "Docs Page",
				Text:  "Readable article text for the web command test.",
			}, nil
		}
	}
	_ = Execute()
	// If Execute didn't os.Exit, exit 0
	os.Exit(0)
}

// cliResult holds the output of a subprocess CLI invocation.
type cliResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// runCLI runs the CLI as a subprocess, capturing stdout, stderr, and exit code.
// The subprocess re-enters via TestHelperProcess.
func runCLI(t *testing.T, env []string, args ...string) cliResult {
	t.Helper()

	// Build args: -test.run=TestHelperProcess -- <cli args>
	cmdArgs := []string{"-test.run=TestHelperProcess", "--"}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), "TTSBUDDY_TEST_HELPER=1")
	cmd.Env = append(cmd.Env, env...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("failed to run subprocess: %v", err)
		}
	}

	return cliResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}
}

// startMockAPI creates an httptest server and returns its URL.
func startMockAPI(t *testing.T, handler http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

// assertExitCode checks the exit code matches expected.
func assertExitCode(t *testing.T, r cliResult, expected int) {
	t.Helper()
	if r.ExitCode != expected {
		t.Errorf("exit code: got %d, want %d\nstdout: %s\nstderr: %s", r.ExitCode, expected, r.Stdout, r.Stderr)
	}
}

// assertContains checks that s contains substr.
func assertContains(t *testing.T, s, substr, label string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("%s should contain %q, got:\n%s", label, substr, s)
	}
}

// assertNotContains checks that s does NOT contain substr.
func assertNotContains(t *testing.T, s, substr, label string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Errorf("%s should NOT contain %q, got:\n%s", label, substr, s)
	}
}

// assertValidJSON checks that s is valid JSON.
func assertValidJSON(t *testing.T, s string) {
	t.Helper()
	s = strings.TrimSpace(s)
	if !json.Valid([]byte(s)) {
		t.Errorf("expected valid JSON, got:\n%s", s)
	}
}

// envForTest returns env vars for subprocess tests with a test HOME and API URL.
func envForTest(home, apiURL, apiKey string) []string {
	env := []string{
		"HOME=" + home,
	}
	if apiURL != "" {
		env = append(env, "TTSBUDDY_API_URL="+apiURL)
	}
	if apiKey != "" {
		env = append(env, "TTSBUDDY_API_KEY="+apiKey)
	}
	return env
}

// mockCompletedHandler returns a handler that responds with a completed TTS response.
func mockCompletedHandler(audioServerURL string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}

		resp := map[string]interface{}{
			"success":   true,
			"status":    "completed",
			"job_id":    "test-job-001",
			"audio_url": audioServerURL + "/audio.mp3",
			"audio": map[string]interface{}{
				"format":           "mp3",
				"voice":            "af_heart",
				"speed":            1.0,
				"duration_seconds": 5.0,
			},
			"billing": map[string]interface{}{
				"mode":                 "subscription",
				"estimated_cost_cents": 0,
			},
			"meta": map[string]interface{}{
				"request_id":  "req-001",
				"api_version": "2026-04",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// mockAudioHandler serves fake MP3 data.
func mockAudioHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("fake-mp3-data-for-testing"))
	})
}
