package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestInterruptedSubmissionExposesEffectiveKeyForExplicitRetry(t *testing.T) {
	home := t.TempDir()
	var postCount atomic.Int32
	received := make(chan string, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	var firstKey atomic.Value
	apiServer := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			t.Error("submission omitted idempotency key")
		}
		count := postCount.Add(1)
		body, _ := io.ReadAll(r.Body)
		var request map[string]any
		decodeErr := json.Unmarshal(body, &request)
		textValue, _ := request["text"].(string)
		if decodeErr != nil || strings.TrimSpace(textValue) != "interrupted submission" {
			t.Fatalf("unexpected retry request body=%s err=%v", body, decodeErr)
		}
		if count == 1 {
			firstKey.Store(key)
			received <- key
			<-release
			return
		}
		if key != firstKey.Load().(string) {
			t.Fatalf("explicit retry key=%q, want %q", key, firstKey.Load().(string))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "status": "completed", "job_id": "job-original", "audio_url": apiServerURLPlaceholder(r)})
	}))
	t.Cleanup(releaseFn)

	cmdArgs := []string{"-test.run=TestHelperProcess", "--", "speak", "-", "--json", "--no-download"}
	child := exec.Command(os.Args[0], cmdArgs...)
	child.Env = subprocessTestEnv([]string{"HOME=" + home, "TTSBUDDY_API_URL=" + apiServer, "TTSBUDDY_API_KEY=" + testSubscriptionCredential(), "TTSBUDDY_ALLOW_CUSTOM_API_URL=true"})
	child.Stdin = strings.NewReader("interrupted submission\n")
	var stdout, stderr bytes.Buffer
	child.Stdout = &stdout
	child.Stderr = &stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	var key string
	select {
	case key = <-received:
	case <-time.After(5 * time.Second):
		_ = child.Process.Kill()
		t.Fatal("timed out waiting for the ambiguous submission")
	}
	if err := child.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err := child.Wait()
	releaseFn()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 130 {
		t.Fatalf("interrupted child err=%v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	var recovery map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &recovery); err != nil {
		t.Fatalf("recovery JSON=%q err=%v", stdout.String(), err)
	}
	errorDetail, _ := recovery["error"].(map[string]any)
	if got, _ := errorDetail["idempotency_key"].(string); got != key {
		t.Fatalf("recovery key=%q, want sent key %q: %#v", got, key, recovery)
	}
	if strings.Contains(stdout.String()+stderr.String(), "interrupted submission") {
		t.Fatal("recovery output echoed input text")
	}

	retry := runCLIInput(t, "interrupted submission\n", []string{"HOME=" + home, "TTSBUDDY_API_URL=" + apiServer, "TTSBUDDY_API_KEY=" + testSubscriptionCredential(), "TTSBUDDY_ALLOW_CUSTOM_API_URL=true"}, "speak", "-", "--json", "--no-download", "--idempotency-key", key)
	if retry.ExitCode != 0 || !strings.Contains(retry.Stdout, `"job_id": "job-original"`) || postCount.Load() != 2 {
		t.Fatalf("explicit retry=%#v posts=%d", retry, postCount.Load())
	}
}

// The retry fixture only needs an allowlisted URL; the speak command does not
// download in this regression. Keep it tied to the request host without
// exposing provider data in the recovery assertion.
func apiServerURLPlaceholder(r *http.Request) string {
	return "http://" + r.Host + "/audio.mp3"
}
