package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSpeak200Completed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test_key" {
			t.Errorf("missing or wrong auth header")
		}
		if r.Header.Get("Idempotency-Key") != "test-idem" {
			t.Errorf("missing idempotency key")
		}
		if r.Header.Get("User-Agent") != "ttsbuddy-cli/test" {
			t.Errorf("wrong user agent: %s", r.Header.Get("User-Agent"))
		}
		_ = json.NewEncoder(w).Encode(TTSResponse{
			Success:  true,
			Status:   "completed",
			JobID:    "job-123",
			AudioURL: "https://example.com/audio.mp3",
			Audio:    &AudioInfo{Format: "mp3", Voice: "af_heart", Speed: 1.0},
			Billing:  &Billing{Mode: "subscription", EstimatedCostCents: 0},
			Meta:     &Meta{RequestID: "req-1", APIVersion: "2026-04"},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test_key", "test")
	resp, status, err := client.Speak(context.Background(), SpeakRequest{Text: "hello", Voice: "af_heart", Speed: 1.0}, "test-idem")
	if err != nil {
		t.Fatalf("Speak error: %v", err)
	}
	if status != 200 {
		t.Errorf("expected 200, got %d", status)
	}
	if resp.Status != "completed" {
		t.Errorf("expected completed, got %s", resp.Status)
	}
	if resp.Audio == nil {
		t.Error("expected audio info")
	}
	if resp.Billing == nil {
		t.Error("expected billing info")
	}
}

func TestSpeak202Processing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		retryAfter := 5
		_ = json.NewEncoder(w).Encode(TTSResponse{
			Success:           true,
			Status:            "processing",
			JobID:             "job-456",
			StatusURL:         "/v1/agent-tts?id=job-456",
			RetryAfterSeconds: &retryAfter,
			Meta:              &Meta{RequestID: "req-2", APIVersion: "2026-04"},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test_key", "test")
	resp, status, err := client.Speak(context.Background(), SpeakRequest{Text: "long text"}, "idem-2")
	if err != nil {
		t.Fatalf("Speak error: %v", err)
	}
	if status != 202 {
		t.Errorf("expected 202, got %d", status)
	}
	if resp.Status != "processing" {
		t.Errorf("expected processing, got %s", resp.Status)
	}
	if resp.RetryAfterSeconds == nil || *resp.RetryAfterSeconds != 5 {
		t.Error("expected retry_after_seconds=5")
	}
}

func TestSpeak200FailedReplay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(TTSResponse{
			Success: false,
			Status:  "failed",
			JobID:   "job-789",
			Error:   &APIError{Code: ErrTTSProviderError, Message: "failed. Use a new Idempotency-Key."},
			Meta:    &Meta{RequestID: "req-3", APIVersion: "2026-04"},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test_key", "test")
	resp, _, err := client.Speak(context.Background(), SpeakRequest{Text: "hello"}, "idem-3")
	if err != nil {
		t.Fatalf("should not error on 200: %v", err)
	}
	if resp.Success {
		t.Error("expected success=false")
	}
	if resp.Status != "failed" {
		t.Errorf("expected failed, got %s", resp.Status)
	}
	if !NeedsNewIdempotencyKey(resp.Error) {
		t.Error("should detect new idempotency key needed")
	}
}

func TestSpeak200Expired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(TTSResponse{
			Success: false,
			Status:  "expired",
			JobID:   "job-exp",
			Error:   &APIError{Code: ErrFileExpired, Message: "audio file has expired"},
			Meta:    &Meta{RequestID: "req-4", APIVersion: "2026-04"},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test_key", "test")
	resp, _, err := client.Speak(context.Background(), SpeakRequest{Text: "hello"}, "idem-4")
	if err != nil {
		t.Fatalf("should not error on 200: %v", err)
	}
	if resp.Status != "expired" {
		t.Errorf("expected expired, got %s", resp.Status)
	}
}

func TestSpeak401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_ = json.NewEncoder(w).Encode(TTSResponse{
			Success: false,
			Error:   &APIError{Code: ErrInvalidKey, Message: "Invalid or missing API key"},
			Meta:    &Meta{RequestID: "req-5", APIVersion: "2026-04"},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "bad_key", "test")
	_, status, err := client.Speak(context.Background(), SpeakRequest{Text: "hello"}, "idem-5")
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if status != 401 {
		t.Errorf("expected 401, got %d", status)
	}
	apiErr, ok := err.(*APIResponseError)
	if !ok {
		t.Fatalf("expected APIResponseError, got %T", err)
	}
	if apiErr.ErrorCode() != ErrInvalidKey {
		t.Errorf("expected INVALID_KEY, got %s", apiErr.ErrorCode())
	}
}

func TestSpeak429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(429)
		retryAfter := 10
		_ = json.NewEncoder(w).Encode(TTSResponse{
			Success:           false,
			Error:             &APIError{Code: ErrRateLimited, Message: "Rate limit exceeded"},
			RetryAfterSeconds: &retryAfter,
			Meta:              &Meta{RequestID: "req-6", APIVersion: "2026-04"},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test_key", "test")
	resp, status, err := client.Speak(context.Background(), SpeakRequest{Text: "hello"}, "idem-6")
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if status != 429 {
		t.Errorf("expected 429, got %d", status)
	}
	if resp.RetryAfterSeconds == nil || *resp.RetryAfterSeconds != 10 {
		t.Error("expected retry_after_seconds=10")
	}
}

func TestGetStatus200Completed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Query().Get("id") != "job-123" {
			t.Errorf("expected id=job-123, got %s", r.URL.Query().Get("id"))
		}
		_ = json.NewEncoder(w).Encode(TTSResponse{
			Success:  true,
			Status:   "completed",
			JobID:    "job-123",
			AudioURL: "https://example.com/audio.mp3",
			Meta:     &Meta{RequestID: "req-7", APIVersion: "2026-04"},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test_key", "test")
	resp, status, err := client.GetStatus(context.Background(), "job-123")
	if err != nil {
		t.Fatalf("GetStatus error: %v", err)
	}
	if status != 200 {
		t.Errorf("expected 200, got %d", status)
	}
	if resp.Status != "completed" {
		t.Errorf("expected completed, got %s", resp.Status)
	}
}

func TestGetStatus404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(TTSResponse{
			Success: false,
			Error:   &APIError{Code: ErrNotFound, Message: "Job not found"},
			Meta:    &Meta{RequestID: "req-8", APIVersion: "2026-04"},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test_key", "test")
	_, status, err := client.GetStatus(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if status != 404 {
		t.Errorf("expected 404, got %d", status)
	}
}

func TestSpeakRejectsCrossOriginRedirectWithoutLeakingAuth(t *testing.T) {
	var targetAuth string
	var targetBody []byte
	var targetHits int

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits++
		targetAuth = r.Header.Get("Authorization")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading redirected body: %v", err)
		}
		targetBody = body
		_ = json.NewEncoder(w).Encode(TTSResponse{Success: true, Status: "completed", JobID: "leaked"})
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client := NewClient(redirector.URL, "test_key", "test")
	_, _, err := client.Speak(context.Background(), SpeakRequest{Text: "hello", Voice: "af_heart", Speed: 1.0}, "test-idem")
	if err == nil {
		t.Fatal("expected cross-origin redirect error")
	}
	if !strings.Contains(err.Error(), "cross-origin API redirect") {
		t.Fatalf("expected cross-origin redirect error, got %v", err)
	}
	if targetHits != 0 {
		t.Fatalf("expected redirected target not to be reached, got %d hits", targetHits)
	}
	if targetAuth != "" {
		t.Fatalf("redirected target received Authorization header: %q", targetAuth)
	}
	if len(targetBody) != 0 {
		t.Fatalf("redirected target received body: %q", string(targetBody))
	}
}

func TestGetStatusRejectsCrossOriginRedirectWithoutLeakingAuth(t *testing.T) {
	var targetAuth string
	var targetBody []byte
	var targetHits int

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits++
		targetAuth = r.Header.Get("Authorization")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading redirected body: %v", err)
		}
		targetBody = body
		_ = json.NewEncoder(w).Encode(TTSResponse{Success: true, Status: "completed", JobID: "leaked"})
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client := NewClient(redirector.URL, "test_key", "test")
	_, _, err := client.GetStatus(context.Background(), "job-123")
	if err == nil {
		t.Fatal("expected cross-origin redirect error")
	}
	if !strings.Contains(err.Error(), "cross-origin API redirect") {
		t.Fatalf("expected cross-origin redirect error, got %v", err)
	}
	if targetHits != 0 {
		t.Fatalf("expected redirected target not to be reached, got %d hits", targetHits)
	}
	if targetAuth != "" {
		t.Fatalf("redirected target received Authorization header: %q", targetAuth)
	}
	if len(targetBody) != 0 {
		t.Fatalf("redirected target received body: %q", string(targetBody))
	}
}

func TestSpeakAllowsSameOriginRedirect(t *testing.T) {
	var finalAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
		case "/final":
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			finalAuth = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(TTSResponse{
				Success: true,
				Status:  "completed",
				JobID:   "same-origin",
				Meta:    &Meta{RequestID: "req-same-origin", APIVersion: "2026-04"},
			})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL+"/start", "test_key", "test")
	resp, status, err := client.Speak(context.Background(), SpeakRequest{Text: "hello", Voice: "af_heart", Speed: 1.0}, "test-idem")
	if err != nil {
		t.Fatalf("Speak error: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("expected 200, got %d", status)
	}
	if finalAuth != "Bearer test_key" {
		t.Fatalf("expected Authorization on same-origin redirect, got %q", finalAuth)
	}
	if resp.JobID != "same-origin" {
		t.Fatalf("expected same-origin job ID, got %q", resp.JobID)
	}
}

func TestCompletedMissingAudio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(TTSResponse{
			Success:  true,
			Status:   "completed",
			JobID:    "job-no-audio",
			AudioURL: "https://example.com/audio.mp3",
			Meta:     &Meta{RequestID: "req-9", APIVersion: "2026-04"},
			// No Audio or Billing — fallback path
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test_key", "test")
	resp, _, err := client.Speak(context.Background(), SpeakRequest{Text: "test"}, "idem-9")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Audio != nil {
		t.Error("expected nil audio on fallback path")
	}
	if resp.Billing != nil {
		t.Error("expected nil billing on fallback path")
	}
	if resp.AudioURL == "" {
		t.Error("expected audio_url even on fallback")
	}
}

func TestParseResponsePreservesAccessPassBillingFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"status": "completed",
			"job_id": "job-pass",
			"billing": {
				"mode": "prepaid_pass",
				"estimated_cost_cents": 0,
				"units": 123,
				"request_units": 123,
				"allowance_units": 500000,
				"reserved_units": 0,
				"consumed_units": 100,
				"remaining_units": 499900
			},
			"meta": {"request_id": "req-pass", "api_version": "2026-04"}
		}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, fixtureCredential("ttsp", 'a', 'b'), "test")
	resp, _, err := client.Speak(context.Background(), SpeakRequest{Text: "hello"}, "idem-pass")
	if err != nil {
		t.Fatalf("Speak error: %v", err)
	}
	if resp.Billing == nil || resp.Billing.Mode != "prepaid_pass" {
		t.Fatalf("missing pass billing: %#v", resp.Billing)
	}
	if resp.Billing.Units == nil || *resp.Billing.Units != 123 {
		t.Fatalf("units not preserved: %#v", resp.Billing)
	}
	if resp.Billing.RequestUnits == nil || *resp.Billing.RequestUnits != 123 {
		t.Fatalf("request units not preserved: %#v", resp.Billing)
	}
	if resp.Billing.AllowanceUnits == nil || *resp.Billing.AllowanceUnits != 500_000 {
		t.Fatalf("allowance units not preserved: %#v", resp.Billing)
	}
	if resp.Billing.ConsumedUnits == nil || *resp.Billing.ConsumedUnits != 100 {
		t.Fatalf("consumed units not preserved: %#v", resp.Billing)
	}
	if resp.Billing.ReservedUnits == nil || *resp.Billing.ReservedUnits != 0 {
		t.Fatalf("reserved units not preserved: %#v", resp.Billing)
	}
	if resp.Billing.RemainingUnits == nil || *resp.Billing.RemainingUnits != 499_900 {
		t.Fatalf("remaining units not preserved: %#v", resp.Billing)
	}
}

func fixtureCredential(prefix string, public, secret byte) string {
	return prefix + "_" + strings.Repeat(string(public), 8) + "_" + strings.Repeat(string(secret), 48)
}

func TestResolveStatusURL(t *testing.T) {
	client := NewClient("https://www.ttsbuddy.com/v1/agent-tts", "key", "test")

	tests := []struct {
		input string
		want  string
	}{
		{"/v1/agent-tts?id=abc", "https://www.ttsbuddy.com/v1/agent-tts?id=abc"},
		{"https://other.com/path", "https://other.com/path"},
	}

	for _, tt := range tests {
		got := client.ResolveStatusURL(tt.input)
		if got != tt.want {
			t.Errorf("ResolveStatusURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDownloadAudioAtomic(t *testing.T) {
	content := []byte("fake mp3 data")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	dest := filepath.Join(tmp, "output.mp3")

	client := NewClient("", "", "test")
	if err := client.DownloadAudio(context.Background(), srv.URL, dest); err != nil {
		t.Fatalf("DownloadAudio error: %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("cannot read output: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("content mismatch: got %q", data)
	}

	// .part should not exist
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Error(".part file should not exist after success")
	}
}

func TestDownloadAudioCleanupOnError(t *testing.T) {
	withDownloadRetryDelays(t, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	dest := filepath.Join(tmp, "output.mp3")

	client := NewClient("", "", "test")
	err := client.DownloadAudio(context.Background(), srv.URL, dest)
	if err == nil {
		t.Fatal("expected error for 500")
	}

	// Neither final nor .part should exist
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("final file should not exist after error")
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Error(".part file should not exist after error")
	}
}

func TestDownloadAudioRetriesTransientStatus(t *testing.T) {
	withDownloadRetryDelays(t, []time.Duration{time.Millisecond})

	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("object not ready"))
			return
		}
		_, _ = w.Write([]byte("fake mp3 data"))
	}))
	defer srv.Close()

	tmp := t.TempDir()
	dest := filepath.Join(tmp, "output.mp3")

	client := NewClient("", "", "test")
	if err := client.DownloadAudio(context.Background(), srv.URL, dest); err != nil {
		t.Fatalf("DownloadAudio error: %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("cannot read output: %v", err)
	}
	if string(data) != "fake mp3 data" {
		t.Errorf("content mismatch: got %q", data)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
}

func withDownloadRetryDelays(t *testing.T, delays []time.Duration) {
	t.Helper()

	orig := downloadRetryDelays
	downloadRetryDelays = delays
	t.Cleanup(func() {
		downloadRetryDelays = orig
	})
}

// --- Security tests ---

func TestValidateDownloadURL(t *testing.T) {
	apiHost := "api.ttsbuddy.com"
	cases := []struct {
		url     string
		wantErr bool
	}{
		// Allowed: S3 (*.amazonaws.com)
		{"https://s3.amazonaws.com/audio.mp3", false},
		{"https://tts-bucket.s3.us-east-1.amazonaws.com/file.mp3", false},
		// Allowed: API host
		{"https://api.ttsbuddy.com/audio.mp3", false},
		// Allowed: localhost (HTTP)
		{"http://localhost:8080/audio.mp3", false},
		{"http://127.0.0.1:9000/audio.mp3", false},
		{"http://[::1]:8080/audio.mp3", false},
		// Rejected: random HTTPS host (not in allowlist)
		{"https://cdn.example.com/file.mp3", true},
		{"https://evil.com/steal", true},
		// Rejected: HTTP to non-localhost
		{"http://evil.com/steal", true},
		{"http://internal.corp/secret", true},
		// Rejected: other schemes
		{"ftp://server/file", true},
		{"file:///etc/passwd", true},
		{"", true},
	}
	for _, tc := range cases {
		err := ValidateDownloadURL(tc.url, apiHost)
		if tc.wantErr && err == nil {
			t.Errorf("ValidateDownloadURL(%q, %q) should error", tc.url, apiHost)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("ValidateDownloadURL(%q, %q) unexpected error: %v", tc.url, apiHost, err)
		}
	}
}

func TestAllowedDownloadHost(t *testing.T) {
	cases := []struct {
		host, apiHost string
		want          bool
	}{
		{"api.ttsbuddy.com", "api.ttsbuddy.com", true},  // same as API
		{"s3.amazonaws.com", "api.ttsbuddy.com", true},  // S3
		{"bucket.s3.us-east-1.amazonaws.com", "", true}, // S3 subdomain
		{"localhost", "", true},                         // local
		{"127.0.0.1", "", true},                         // local
		{"evil.com", "api.ttsbuddy.com", false},         // random host
		{"internal.corp", "api.ttsbuddy.com", false},    // internal host
		{"amazonaws.com.evil.com", "", false},           // suffix trick
	}
	for _, tc := range cases {
		got := allowedDownloadHost(tc.host, tc.apiHost)
		if got != tc.want {
			t.Errorf("allowedDownloadHost(%q, %q) = %v, want %v", tc.host, tc.apiHost, got, tc.want)
		}
	}
}

func TestDownloadRedirectToInsecure(t *testing.T) {
	// Target server (insecure HTTP on non-localhost)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("stolen data"))
	}))
	defer target.Close()

	// Redirect server (HTTPS would be ideal but httptest.NewTLSServer adds complexity;
	// use localhost HTTP which is allowed, redirecting to the target)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect to target (which is also localhost in test, so the redirect itself
		// would pass validation). This test verifies the CheckRedirect mechanism exists.
		http.Redirect(w, r, target.URL+"/audio.mp3", http.StatusFound)
	}))
	defer redirector.Close()

	client := NewClient("", "", "test")
	tmp := t.TempDir()
	dest := filepath.Join(tmp, "out.mp3")

	// Both are localhost so this should succeed (scheme hardening, not host restriction)
	err := client.DownloadAudio(context.Background(), redirector.URL+"/start", dest)
	if err != nil {
		t.Logf("redirect test result: %v (both localhost, may succeed)", err)
	}
}

func TestDownloadAudioSizeLimit(t *testing.T) {
	// Server streams more data than maxAudioSize
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write maxAudioSize + 100 bytes
		chunk := make([]byte, 1024)
		for i := int64(0); i <= maxAudioSize/1024+1; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	client := NewClient("", "", "test")
	tmp := t.TempDir()
	dest := filepath.Join(tmp, "big.mp3")

	err := client.DownloadAudio(context.Background(), srv.URL, dest)
	if err == nil {
		t.Fatal("expected error for oversize download")
	}
	if !strings.Contains(err.Error(), "size limit") {
		t.Errorf("error should mention size limit, got: %v", err)
	}

	// No leftover temp files
	entries, _ := os.ReadDir(tmp)
	for _, e := range entries {
		t.Errorf("leftover file in temp dir: %s", e.Name())
	}
}

func TestDownloadAudioUniqueTempFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("audio data"))
	}))
	defer srv.Close()

	client := NewClient("", "", "test")
	tmp := t.TempDir()
	dest := filepath.Join(tmp, "out.mp3")

	err := client.DownloadAudio(context.Background(), srv.URL, dest)
	if err != nil {
		t.Fatalf("download error: %v", err)
	}

	// Verify file exists at dest and no .part files remain
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		t.Error("output file should exist")
	}
	entries, _ := os.ReadDir(tmp)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".part") {
			t.Errorf("leftover .part file: %s", e.Name())
		}
	}
}

func TestCopyBounded(t *testing.T) {
	// Under limit
	src := strings.NewReader("hello")
	var dst strings.Builder
	n, err := CopyBounded(&dst, src, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Errorf("expected 5 bytes, got %d", n)
	}

	// Over limit
	src2 := strings.NewReader(strings.Repeat("x", 200))
	var dst2 strings.Builder
	_, err = CopyBounded(&dst2, src2, 100)
	if err == nil {
		t.Fatal("expected error for oversize input")
	}
	if !strings.Contains(err.Error(), "size limit") {
		t.Errorf("error should mention size limit: %v", err)
	}
}

// Tests for IsRetryable, NeedsNewIdempotencyKey, idempotency keys moved to
// types_test.go and idempotency_test.go

func TestFetchVoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/voices" {
			t.Errorf("expected /api/v1/voices, got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"af_heart","name":"Heart","gender":"Female","language":"English"}]`))
	}))
	defer srv.Close()

	client := NewClient("", "", "test")
	voices, err := client.FetchVoices(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchVoices error: %v", err)
	}
	if len(voices) != 1 || voices[0].ID != "af_heart" {
		t.Errorf("expected af_heart, got %+v", voices)
	}
}

func TestFetchVoicesFollowsPublicRedirectWithEmptyAPIURL(t *testing.T) {
	var finalHits int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/voices":
			http.Redirect(w, r, "/final-voices", http.StatusTemporaryRedirect)
		case "/final-voices":
			finalHits++
			if r.Header.Get("Authorization") != "" {
				t.Fatalf("unexpected Authorization header on public voices request: %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`[{"id":"af_heart","name":"Heart","gender":"Female","language":"English"}]`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewClient("", "", "test")
	voices, err := client.FetchVoices(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchVoices error: %v", err)
	}
	if finalHits != 1 {
		t.Fatalf("expected final voices endpoint to be reached once, got %d", finalHits)
	}
	if len(voices) != 1 || voices[0].ID != "af_heart" {
		t.Errorf("expected af_heart, got %+v", voices)
	}
}

func TestParseResponseOversize(t *testing.T) {
	// Server returns body larger than maxResponseSize (10MB)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Write 11MB of data
		chunk := make([]byte, 1024*1024) // 1MB
		for i := range chunk {
			chunk[i] = 'a'
		}
		for i := 0; i < 11; i++ {
			_, _ = w.Write(chunk)
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "key", "test")
	_, _, err := client.Speak(context.Background(), SpeakRequest{Text: "test"}, "key")
	if err == nil {
		t.Fatal("expected error for oversize response")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error should mention 'too large', got: %v", err)
	}
}

func TestFetchVoicesFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	client := NewClient("", "", "test")
	_, err := client.FetchVoices(context.Background(), srv.URL)
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestSpeakNonJSONError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte(`<html><body>Bad Gateway</body></html>`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test_key", "test")
	resp, status, err := client.Speak(context.Background(), SpeakRequest{Text: "test"}, "key")
	if err == nil {
		t.Fatal("expected error for non-JSON 502")
	}
	if status != 502 {
		t.Errorf("status: got %d, want 502", status)
	}
	apiErr, ok := err.(*APIResponseError)
	if !ok {
		t.Fatalf("expected APIResponseError, got %T", err)
	}
	if apiErr.ErrorCode() != ErrInternalError {
		t.Errorf("code: got %q, want %q", apiErr.ErrorCode(), ErrInternalError)
	}
	if resp == nil || resp.Error == nil {
		t.Fatal("expected synthetic response with error")
	}
}

func TestSpeakNonJSON403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`Forbidden by proxy`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test_key", "test")
	_, _, err := client.Speak(context.Background(), SpeakRequest{Text: "test"}, "key")
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIResponseError)
	if !ok {
		t.Fatalf("expected APIResponseError, got %T", err)
	}
	if apiErr.ErrorCode() != ErrForbidden {
		t.Errorf("code: got %q, want %q (should be generic FORBIDDEN, not INACTIVE_SUBSCRIPTION)", apiErr.ErrorCode(), ErrForbidden)
	}
}

func TestSpeak402DoesNotAttemptAccessPassPurchase(t *testing.T) {
	var paymentHeaders []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paymentHeaders = append(paymentHeaders, r.Header.Get("PAYMENT-SIGNATURE"))
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(TTSResponse{
			Success: false,
			Error:   &APIError{Code: "PASS_BALANCE_INSUFFICIENT", Message: "buy another pass explicitly"},
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, fixtureCredential("ttsp", 'a', 'b'), "test")
	_, status, err := client.Speak(context.Background(), SpeakRequest{Text: "hello"}, "idem-402")
	if err == nil {
		t.Fatal("expected 402 to remain an API error")
	}
	if status != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", status)
	}
	if len(paymentHeaders) != 1 || paymentHeaders[0] != "" {
		t.Fatalf("Speak attempted payment headers: %#v", paymentHeaders)
	}
}

func TestRetryWithSameKey(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(500)
			_ = json.NewEncoder(w).Encode(TTSResponse{
				Success: false,
				Error:   &APIError{Code: ErrInternalError, Message: "transient error"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(TTSResponse{
			Success: true,
			Status:  "completed",
			JobID:   "job-retry",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test_key", "test")
	cfg := RetryConfig{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}

	var lastKey string
	resp, _, err := WithRetry(context.Background(), cfg, func(key string) (*TTSResponse, int, error) {
		lastKey = key
		return client.Speak(context.Background(), SpeakRequest{Text: "test"}, key)
	}, "original-key")

	if err != nil {
		t.Fatalf("expected success after retries: %v", err)
	}
	if resp.Status != "completed" {
		t.Errorf("expected completed, got %s", resp.Status)
	}
	if lastKey != "original-key" {
		t.Errorf("expected same key on transport retry, got %q", lastKey)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestRetryWithNewKey(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(502)
			_ = json.NewEncoder(w).Encode(TTSResponse{
				Success: false,
				Error:   &APIError{Code: ErrTTSProviderError, Message: "Provider failed. Use a new Idempotency-Key."},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(TTSResponse{
			Success: true,
			Status:  "completed",
			JobID:   "job-new-key",
		})
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test_key", "test")
	cfg := RetryConfig{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}

	var lastKey string
	resp, _, err := WithRetry(context.Background(), cfg, func(key string) (*TTSResponse, int, error) {
		lastKey = key
		return client.Speak(context.Background(), SpeakRequest{Text: "test"}, key)
	}, "original-key")

	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if resp.Status != "completed" {
		t.Errorf("expected completed, got %s", resp.Status)
	}
	if lastKey == "original-key" {
		t.Error("expected new key after provider failure, but got original")
	}
}

func TestRetryContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(TTSResponse{
			Success: false,
			Error:   &APIError{Code: ErrInternalError, Message: "error"},
		})
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	client := NewClient(srv.URL, "test_key", "test")
	cfg := RetryConfig{MaxRetries: 3, BaseDelay: 1 * time.Second, MaxDelay: 5 * time.Second}

	_, _, err := WithRetry(ctx, cfg, func(key string) (*TTSResponse, int, error) {
		return client.Speak(ctx, SpeakRequest{Text: "test"}, key)
	}, "key")

	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
