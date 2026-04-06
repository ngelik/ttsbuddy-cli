package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFile(t *testing.T) {
	// Override home to a temp dir
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error for missing file: %v", err)
	}
	if cfg.APIKey != "" {
		t.Errorf("expected empty APIKey, got %q", cfg.APIKey)
	}
}

func TestSaveAndLoad(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	original := &Config{
		APIKey:       "ttsb_abcd1234_secret123",
		DefaultVoice: "bf_emma",
		DefaultSpeed: 0.8,
		PollTimeout:  "5m",
	}

	if err := Save(original); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Check file permissions
	path := filepath.Join(tmp, ".ttsbuddy", "config.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("config file not found: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected 0600 permissions, got %04o", perm)
	}

	// Check dir permissions
	dirInfo, err := os.Stat(filepath.Join(tmp, ".ttsbuddy"))
	if err != nil {
		t.Fatalf("config dir not found: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0700 {
		t.Errorf("expected 0700 dir permissions, got %04o", perm)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded.APIKey != original.APIKey {
		t.Errorf("APIKey: got %q, want %q", loaded.APIKey, original.APIKey)
	}
	if loaded.DefaultVoice != original.DefaultVoice {
		t.Errorf("Voice: got %q, want %q", loaded.DefaultVoice, original.DefaultVoice)
	}
	if loaded.DefaultSpeed != original.DefaultSpeed {
		t.Errorf("Speed: got %v, want %v", loaded.DefaultSpeed, original.DefaultSpeed)
	}
}

func TestSetAndGet(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := Set("voice", "am_michael"); err != nil {
		t.Fatalf("Set voice: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultVoice != "am_michael" {
		t.Errorf("voice: got %q, want %q", cfg.DefaultVoice, "am_michael")
	}
}

func TestSetInvalidSpeed(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := Set("speed", "2.0"); err == nil {
		t.Error("expected error for speed=2.0, got nil")
	}
	if err := Set("speed", "0.3"); err == nil {
		t.Error("expected error for speed=0.3, got nil")
	}
	if err := Set("speed", "abc"); err == nil {
		t.Error("expected error for speed=abc, got nil")
	}
}

func TestSetUnknownKey(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := Set("nonexistent", "value"); err == nil {
		t.Error("expected error for unknown key, got nil")
	}
}

func TestRedactKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ttsb_a1b2c3d4_e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8", "ttsb_a1b2c3d4_..."},
		{"ttsb_short_secret", "ttsb_short_..."},
		{"", ""},
		{"not_a_key", "***"},
		{"ttsb_nounderscore", "***"},
	}
	for _, tt := range tests {
		got := RedactKey(tt.input)
		if got != tt.want {
			t.Errorf("RedactKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestResolvePrecedence(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Config file has voice=af_bella
	cfg := &Config{DefaultVoice: "af_bella"}

	// Env var overrides config
	t.Setenv("TTSBUDDY_VOICE", "bf_emma")

	resolved, _ := Resolve(cfg, FlagValues{})
	if resolved.Voice != "bf_emma" {
		t.Errorf("env should override config: got %q", resolved.Voice)
	}

	// Flag overrides env
	flagVoice := "am_adam"
	resolved, _ = Resolve(cfg, FlagValues{Voice: &flagVoice})
	if resolved.Voice != "am_adam" {
		t.Errorf("flag should override env: got %q", resolved.Voice)
	}
}

func TestResolveDefaults(t *testing.T) {
	cfg := &Config{}
	resolved, _ := Resolve(cfg, FlagValues{})

	if resolved.APIURL != DefaultAPIURL {
		t.Errorf("APIURL: got %q, want %q", resolved.APIURL, DefaultAPIURL)
	}
	if resolved.Voice != DefaultVoice {
		t.Errorf("Voice: got %q, want %q", resolved.Voice, DefaultVoice)
	}
	if resolved.Speed != DefaultSpeed {
		t.Errorf("Speed: got %v, want %v", resolved.Speed, DefaultSpeed)
	}
	if resolved.PollTimeout != DefaultPollTimeout {
		t.Errorf("PollTimeout: got %q, want %q", resolved.PollTimeout, DefaultPollTimeout)
	}
}

func TestLastJobRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	jobID := "550e8400-e29b-41d4-a716-446655440000"
	if err := SaveLastJob(jobID); err != nil {
		t.Fatalf("SaveLastJob: %v", err)
	}

	lj, err := LoadLastJob()
	if err != nil {
		t.Fatalf("LoadLastJob error: %v", err)
	}
	if lj == nil {
		t.Fatal("LoadLastJob returned nil")
	}
	if lj.JobID != jobID {
		t.Errorf("JobID: got %q, want %q", lj.JobID, jobID)
	}
	if lj.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
}

func TestLastJobMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	lj, err := LoadLastJob()
	if err != nil {
		t.Errorf("missing file should not error: %v", err)
	}
	if lj != nil {
		t.Errorf("expected nil for missing last_job.json, got %+v", lj)
	}
}

func TestLastJobCorrupt(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir := filepath.Join(tmp, ".ttsbuddy")
	_ = os.MkdirAll(dir, 0700)
	_ = os.WriteFile(filepath.Join(dir, "last_job.json"), []byte("{corrupt"), 0600)

	lj, err := LoadLastJob()
	if err == nil {
		t.Error("corrupt file should return error")
	}
	if lj != nil {
		t.Error("corrupt file should return nil job")
	}
}

func TestIsValidKey(t *testing.T) {
	if !IsValidKey("key") {
		t.Error("'key' should be valid")
	}
	if !IsValidKey("voice") {
		t.Error("'voice' should be valid")
	}
	if IsValidKey("bogus") {
		t.Error("'bogus' should be invalid")
	}
}

func TestGetAllKeys(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg := &Config{
		APIKey:       "ttsb_abc_secret",
		DefaultVoice: "bf_emma",
		DefaultSpeed: 0.8,
		PollTimeout:  "5m",
		OutputDir:    "/tmp",
		APIURL:       "https://example.com",
		TTSAPIBaseURL: "https://tts.example.com",
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	// Key should be redacted
	val, _ := Get(loaded, "key")
	if val != "ttsb_abc_..." {
		t.Errorf("key should be redacted, got %q", val)
	}

	val, _ = Get(loaded, "voice")
	if val != "bf_emma" {
		t.Errorf("voice: got %q", val)
	}

	val, _ = Get(loaded, "speed")
	if val != "0.8" {
		t.Errorf("speed: got %q", val)
	}

	_, err = Get(loaded, "nonexistent")
	if err == nil {
		t.Error("expected error for unknown key")
	}
}

func TestSetTimeoutValid(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := Set("timeout", "5m"); err != nil {
		t.Errorf("5m should be valid: %v", err)
	}
	if err := Set("timeout", "30s"); err != nil {
		t.Errorf("30s should be valid: %v", err)
	}
}

func TestSetTimeoutInvalid(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	err := Set("timeout", "garbage")
	if err == nil {
		t.Error("expected error for invalid timeout")
	}
	if _, ok := err.(*ValidationError); !ok {
		t.Errorf("expected *ValidationError, got %T", err)
	}
}

func TestValidationErrorType(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	err := Set("speed", "bad")
	if _, ok := err.(*ValidationError); !ok {
		t.Errorf("speed validation: expected *ValidationError, got %T", err)
	}

	err = Set("timeout", "bad")
	if _, ok := err.(*ValidationError); !ok {
		t.Errorf("timeout validation: expected *ValidationError, got %T", err)
	}
}

func TestAtomicWriteNoTempLeftover(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg := &Config{DefaultVoice: "af_heart"}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}

	// No .tmp files should remain
	dir := filepath.Join(tmp, ".ttsbuddy")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" && e.Name() != "config.json" {
			t.Errorf("unexpected file in config dir: %s", e.Name())
		}
	}
}

func TestLoadMalformedJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir := filepath.Join(tmp, ".ttsbuddy")
	_ = os.MkdirAll(dir, 0700)
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json}"), 0600)

	_, err := Load()
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestLoadDoesNotCreateDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Load should NOT create ~/.ttsbuddy
	_, _ = Load()

	dir := filepath.Join(tmp, ".ttsbuddy")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("Load should not create config directory")
	}
}

func TestWarnInsecureURL(t *testing.T) {
	cases := []struct {
		url     string
		wantMsg bool
	}{
		{"https://api.example.com", false},
		{"http://localhost:54321/api", false},
		{"http://127.0.0.1:8080/api", false},
		{"http://[::1]:8080/api", false},
		{"http://evil.com/api", true},
		{"http://internal.corp/api", true},
		{"", false},
		{"not-a-url", false},
	}
	for _, tc := range cases {
		w := WarnInsecureURL(tc.url)
		if tc.wantMsg && w == "" {
			t.Errorf("WarnInsecureURL(%q) should warn", tc.url)
		}
		if !tc.wantMsg && w != "" {
			t.Errorf("WarnInsecureURL(%q) should not warn, got: %q", tc.url, w)
		}
	}
}

func TestFormatSpeed(t *testing.T) {
	cases := []struct {
		input float64
		want  string
	}{
		{1.0, "1"},
		{1.25, "1.25"},
		{0.8, "0.8"},
		{0.5, "0.5"},
		{1.5, "1.5"},
		{1.125, "1.125"},
	}
	for _, tc := range cases {
		got := FormatSpeed(tc.input)
		if got != tc.want {
			t.Errorf("FormatSpeed(%v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestResolveEnvSpeedWarning(t *testing.T) {
	t.Setenv("TTSBUDDY_SPEED", "notanumber")
	_, warnings := Resolve(&Config{}, FlagValues{})
	found := false
	for _, w := range warnings {
		if len(w) > 0 {
			found = true
		}
	}
	if !found {
		t.Error("expected warning for invalid TTSBUDDY_SPEED")
	}
}

func TestResolveEnvTimeoutWarning(t *testing.T) {
	t.Setenv("TTSBUDDY_TIMEOUT", "notaduration")
	_, warnings := Resolve(&Config{}, FlagValues{})
	found := false
	for _, w := range warnings {
		if len(w) > 0 {
			found = true
		}
	}
	if !found {
		t.Error("expected warning for invalid TTSBUDDY_TIMEOUT")
	}
}
