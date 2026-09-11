package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownloadExistingJobJSONWritesActualFileMetadataWithoutPOST(t *testing.T) {
	const audio = "downloaded-mp3-bytes"
	var postCount atomic.Int32
	audioSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte(audio))
	}))
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postCount.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":   true,
			"status":    "completed",
			"job_id":    "existing-job",
			"audio_url": audioSrv + "/audio.mp3",
			"stats": map[string]any{
				"speech_length_seconds": 3.5,
				"duration_source":       "provider",
				"file_size_bytes":       len(audio),
				"file_size_source":      "content_length",
			},
		})
	}))

	home := t.TempDir()
	out := filepath.Join(home, "saved.mp3")
	env := append(envForTest(home, apiSrv, "ttsb_test_key"), "TTSBUDDY_CONFIG_DIR="+filepath.Join(home, "isolated"))
	r := runCLI(t, env, "download", "existing-job", "--output", out, "--json")
	assertExitCode(t, r, 0)
	assertValidJSON(t, r.Stdout)
	if r.Stderr != "" {
		t.Fatalf("JSON download should not write stderr: %q", r.Stderr)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(r.Stdout), &payload); err != nil {
		t.Fatal(err)
	}
	download, ok := payload["download"].(map[string]any)
	if !ok {
		t.Fatalf("missing download metadata: %#v", payload)
	}
	absOut, _ := filepath.Abs(out)
	if download["path"] != absOut || int(download["bytes"].(float64)) != len(audio) {
		t.Fatalf("download metadata=%#v, want path=%q bytes=%d", download, absOut, len(audio))
	}
	data, err := os.ReadFile(out)
	if err != nil || string(data) != audio {
		t.Fatalf("saved bytes=%q err=%v", data, err)
	}
	if postCount.Load() != 0 {
		t.Fatalf("download must never submit synthesis, POSTs=%d", postCount.Load())
	}
}

func TestDownloadProcessingPollsWithoutSubmission(t *testing.T) {
	var gets atomic.Int32
	var posts atomic.Int32
	audioSrv := startMockAPI(t, mockAudioHandler())
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			posts.Add(1)
		case http.MethodGet:
			if gets.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "status": "processing", "job_id": "poll-job", "retry_after_seconds": 0})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "status": "completed", "job_id": "poll-job", "audio_url": audioSrv + "/audio.mp3"})
		}
	}))

	home := t.TempDir()
	out := filepath.Join(home, "poll.mp3")
	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "download", "poll-job", "--output", out, "--timeout", "5s")
	assertExitCode(t, r, 0)
	if gets.Load() < 2 || posts.Load() != 0 {
		t.Fatalf("GETs=%d POSTs=%d, want >=2 GETs and no POST", gets.Load(), posts.Load())
	}
}

func TestDownloadRejectsJSONStdoutBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	home := t.TempDir()
	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "download", "job-stdout", "--output", "-", "--json")
	assertExitCode(t, r, 2)
	assertValidJSON(t, r.Stdout)
	assertContains(t, r.Stdout, "mutually exclusive", "stdout")
	if calls.Load() != 0 {
		t.Fatal("conflicting download flags reached the network")
	}
}

func TestDownloadStorageFailureReturnsKnownJobActionWithoutPOST(t *testing.T) {
	var posts atomic.Int32
	audioSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "status": "completed", "job_id": "failed-download-job", "audio_url": audioSrv + "/audio.mp3"})
	}))
	home := t.TempDir()
	out := filepath.Join(home, "failed.mp3")
	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "download", "failed-download-job", "--output", out, "--json")
	assertExitCode(t, r, 1)
	assertValidJSON(t, r.Stdout)
	assertContains(t, r.Stdout, `"type": "download"`, "JSON action")
	assertContains(t, r.Stdout, "failed-download-job", "JSON action")
	assertNotContains(t, r.Stdout, "audio.mp3?", "JSON action")
	if posts.Load() != 0 {
		t.Fatal("storage failure must not submit a new synthesis job")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("failed download should not finalize output, err=%v", err)
	}
}

func TestDownloadRejectsUnsafeJobID(t *testing.T) {
	var calls atomic.Int32
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	home := t.TempDir()
	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "download", "--", "-unsafe-job")
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "must not start", "stderr")
	if calls.Load() != 0 {
		t.Fatal("unsafe job ID reached the network")
	}
}

func TestDownloadTimeoutRejectedBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	home := t.TempDir()
	r := runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "download", "job-timeout", "--timeout", "-1s")
	assertExitCode(t, r, 2)
	assertContains(t, r.Stderr, "invalid timeout", "stderr")
	if calls.Load() != 0 {
		t.Fatal("invalid timeout reached the network")
	}
}

func TestDownloadInterruptedRecoveryKeepsJobAndOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := wrapDownloadFailure(ctx, "known-job", "/tmp/audio.mp3", "https://ttsbuddy.com/audio.mp3", context.Canceled)
	if exitErr, ok := err.(*exitError); !ok || exitErr.code != 130 {
		t.Fatalf("error=%T %#v, want exit 130", err, err)
	} else if exitErr.action == nil || len(exitErr.action.Argv) == 0 || !strings.Contains(strings.Join(exitErr.action.Argv, " "), "known-job") || !strings.Contains(strings.Join(exitErr.action.Argv, " "), "/tmp/audio.mp3") {
		t.Fatalf("missing download action: %#v", exitErr.action)
	}
}

func TestDownloadTransferInterruptPreservesExistingFileAndRecoveryIsExecutable(t *testing.T) {
	home := t.TempDir()
	out := filepath.Join(home, "existing.mp3")
	oldAudio := []byte("old-audio-must-survive")
	if err := os.WriteFile(out, oldAudio, 0600); err != nil {
		t.Fatal(err)
	}

	transferStarted := make(chan struct{})
	var startOnce sync.Once
	var wrongStatusID atomic.Int32
	var postCount atomic.Int32
	server := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/audio.mp3" {
			startOnce.Do(func() { close(transferStarted) })
			w.Header().Set("Content-Type", "audio/mpeg")
			flusher, _ := w.(http.Flusher)
			chunk := bytes.Repeat([]byte("x"), 64*1024)
			for i := 0; i < 256; i++ {
				select {
				case <-r.Context().Done():
					return
				default:
				}
				if _, err := w.Write(chunk); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
				time.Sleep(5 * time.Millisecond)
			}
			return
		}
		if r.Method == http.MethodPost {
			postCount.Add(1)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Method == http.MethodGet {
			if r.URL.Query().Get("id") != "interrupt-job" {
				wrongStatusID.Add(1)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success":   true,
				"status":    "completed",
				"job_id":    "interrupt-job",
				"audio_url": "http://" + r.Host + "/audio.mp3",
			})
		}
	}))

	env := []string{
		"HOME=" + home,
		"TTSBUDDY_API_URL=" + server,
		"TTSBUDDY_API_KEY=" + testSubscriptionCredential(),
		"TTSBUDDY_ALLOW_CUSTOM_API_URL=true",
	}
	cmdArgs := []string{"-test.run=TestHelperProcess", "--", "download", "interrupt-job", "--output", out, "--json"}
	child := exec.Command(os.Args[0], cmdArgs...)
	child.Env = subprocessTestEnv(env)
	var stdout, stderr bytes.Buffer
	child.Stdout = &stdout
	child.Stderr = &stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-transferStarted:
	case <-time.After(5 * time.Second):
		_ = child.Process.Kill()
		t.Fatal("timed out waiting for audio transfer")
	}
	// Allow at least one body chunk to reach the atomic temp file.
	time.Sleep(75 * time.Millisecond)
	if err := child.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	waitErr := child.Wait()
	if exitErr, ok := waitErr.(*exec.ExitError); !ok || exitErr.ExitCode() != 130 {
		t.Fatalf("interrupted child err=%v stdout=%s stderr=%s", waitErr, stdout.String(), stderr.String())
	}
	if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, oldAudio) {
		t.Fatalf("interrupted download changed existing output: got=%q err=%v", got, err)
	}
	parts, err := filepath.Glob(out + ".part.*")
	if err != nil || len(parts) != 0 {
		t.Fatalf("interrupted download left temp files: %v err=%v", parts, err)
	}

	var recovery map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &recovery); err != nil {
		t.Fatalf("recovery JSON=%q err=%v", stdout.String(), err)
	}
	errorDetail, _ := recovery["error"].(map[string]any)
	action, _ := errorDetail["action"].(map[string]any)
	argvRaw, _ := action["argv"].([]any)
	argv := make([]string, 0, len(argvRaw))
	for _, value := range argvRaw {
		argv = append(argv, value.(string))
	}
	if len(argv) == 0 || strings.Contains(strings.Join(argv, " "), "audio.mp3?") {
		t.Fatalf("invalid recovery argv: %#v", action)
	}

	retryArgs := []string{"-test.run=TestHelperProcess", "--"}
	// The action is a complete shell-style argv beginning with the installed
	// executable name; the Go test helper supplies that executable itself.
	if len(argv) > 0 && argv[0] == "ttsbuddy" {
		argv = argv[1:]
	}
	retryArgs = append(retryArgs, argv...)
	retry := exec.Command(os.Args[0], retryArgs...)
	retry.Env = subprocessTestEnv(env)
	var retryStdout, retryStderr bytes.Buffer
	retry.Stdout = &retryStdout
	retry.Stderr = &retryStderr
	if err := retry.Run(); err != nil {
		t.Fatalf("recovery argv failed: %v stdout=%s stderr=%s argv=%#v", err, retryStdout.String(), retryStderr.String(), argv)
	}
	if wrongStatusID.Load() != 0 {
		t.Fatalf("status requests used an unexpected job ID: %d", wrongStatusID.Load())
	}
	if postCount.Load() != 0 {
		t.Fatalf("download/recovery must never submit synthesis, POSTs=%d", postCount.Load())
	}
	if got, err := os.ReadFile(out); err != nil || len(got) == 0 || bytes.Equal(got, oldAudio) {
		t.Fatalf("recovery argv did not produce a new file: bytes=%d err=%v", len(got), err)
	}
}

func TestPollPreservesClassifierAndTerminalActions(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		terminal    string
		expected    string
		notExpected string
	}{
		{name: "download auth", command: "download", terminal: "auth", expected: `"type": "authenticate"`, notExpected: `"type": "download"`},
		{name: "speak auth", command: "speak", terminal: "auth", expected: `"type": "authenticate"`, notExpected: `"type": "status"`},
		{name: "download expired", command: "download", terminal: "expired", expected: `"type": "retry_submission"`, notExpected: `"type": "download"`},
		{name: "speak failed", command: "speak", terminal: "failed", expected: `"type": "retry_submission"`, notExpected: `"type": "status"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var polls atomic.Int32
			apiSrv := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.command == "speak" && r.Method == http.MethodPost {
					w.WriteHeader(http.StatusAccepted)
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "status": "processing", "job_id": "poll-action-job", "retry_after_seconds": 0})
					return
				}
				if r.Method != http.MethodGet {
					return
				}
				if polls.Add(1) == 1 {
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "status": "processing", "job_id": "poll-action-job", "retry_after_seconds": 0})
					return
				}
				switch tc.terminal {
				case "auth":
					w.WriteHeader(http.StatusUnauthorized)
					_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": map[string]string{"code": "INVALID_KEY", "message": "provider detail"}})
				case "expired":
					_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "status": "expired", "job_id": "poll-action-job"})
				case "failed":
					_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "status": "failed", "job_id": "poll-action-job", "error": map[string]string{"code": "TTS_PROVIDER_ERROR", "message": "provider detail"}})
				}
			}))
			home := t.TempDir()
			var result cliResult
			if tc.command == "download" {
				result = runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "download", "poll-action-job", "--json", "--timeout", "5s")
			} else {
				result = runCLI(t, envForTest(home, apiSrv, "ttsb_test_key"), "speak", "hello", "--json", "--no-download")
			}
			assertExitCode(t, result, 1)
			assertValidJSON(t, result.Stdout)
			assertContains(t, result.Stdout, tc.expected, "recovery action")
			assertNotContains(t, result.Stdout, tc.notExpected, "incorrect recovery action")
		})
	}
}
