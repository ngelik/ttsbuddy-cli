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

	resolved := Resolve(cfg, FlagValues{})
	if resolved.Voice != "bf_emma" {
		t.Errorf("env should override config: got %q", resolved.Voice)
	}

	// Flag overrides env
	flagVoice := "am_adam"
	resolved = Resolve(cfg, FlagValues{Voice: &flagVoice})
	if resolved.Voice != "am_adam" {
		t.Errorf("flag should override env: got %q", resolved.Voice)
	}
}

func TestResolveDefaults(t *testing.T) {
	cfg := &Config{}
	resolved := Resolve(cfg, FlagValues{})

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

	lj := LoadLastJob()
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

	lj := LoadLastJob()
	if lj != nil {
		t.Errorf("expected nil for missing last_job.json, got %+v", lj)
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
