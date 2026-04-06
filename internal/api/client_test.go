package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
			StatusURL:         "/functions/v1/agent-tts?id=job-456",
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

func TestResolveStatusURL(t *testing.T) {
	client := NewClient("https://project.supabase.co/functions/v1/agent-tts", "key", "test")

	tests := []struct {
		input string
		want  string
	}{
		{"/functions/v1/agent-tts?id=abc", "https://project.supabase.co/functions/v1/agent-tts?id=abc"},
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

func TestIdempotencyKeyDeterminism(t *testing.T) {
	key1 := GenerateFromContent("hello world", "af_heart", 1.0)
	key2 := GenerateFromContent("hello world", "af_heart", 1.0)
	if key1 != key2 {
		t.Errorf("same input should produce same key: %q != %q", key1, key2)
	}

	key3 := GenerateFromContent("hello world", "bf_emma", 1.0)
	if key1 == key3 {
		t.Error("different voice should produce different key")
	}
}

func TestIdempotencyKeyUniqueness(t *testing.T) {
	k1 := GenerateFromStdin()
	k2 := GenerateFromStdin()
	if k1 == k2 {
		t.Error("stdin keys should be unique")
	}
}

func TestNeedsNewIdempotencyKey(t *testing.T) {
	if NeedsNewIdempotencyKey(nil) {
		t.Error("nil error should not need new key")
	}
	if NeedsNewIdempotencyKey(&APIError{Message: "some error"}) {
		t.Error("generic error should not need new key")
	}
	if !NeedsNewIdempotencyKey(&APIError{Message: "failed. Use a new Idempotency-Key."}) {
		t.Error("should detect 'new Idempotency-Key' in message")
	}
}

func TestIsRetryable(t *testing.T) {
	if !IsRetryable(ErrRateLimited, 429) {
		t.Error("429 should be retryable")
	}
	if !IsRetryable(ErrInternalError, 500) {
		t.Error("500 should be retryable")
	}
	if !IsRetryable(ErrTTSProviderError, 502) {
		t.Error("502 should be retryable")
	}
	if IsRetryable(ErrInvalidKey, 401) {
		t.Error("401 should not be retryable")
	}
	if IsRetryable(ErrInactiveSubscription, 403) {
		t.Error("403 should not be retryable")
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
