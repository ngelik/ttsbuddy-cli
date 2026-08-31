package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestSetRejectsMalformedAPIKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := Set("key", "ttsb_abcd1234_secret"); err == nil {
		t.Fatal("malformed API key was stored")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "" {
		t.Fatalf("malformed API key should not be persisted: %q", cfg.APIKey)
	}
}

func TestRedactKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ttsb_a1b2c3d4_e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8", "ttsb_a1b2c3d4_..."},
		{"ttsp_a1b2c3d4_e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8", "ttsp_a1b2c3d4_..."},
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

func fixtureCredential(prefix string, public, secret byte) string {
	return prefix + "_" + strings.Repeat(string(public), 8) + "_" + strings.Repeat(string(secret), 48)
}

func fixtureAccessPass(public, secret byte) StoredAccessPass {
	return StoredAccessPass{
		Credential:   fixtureCredential("ttsp", public, secret),
		PurchaseID:   "purchase_" + strings.Repeat(string(public), 8),
		ExpiresAt:    time.Now().Add(24 * time.Hour).UTC(),
		Network:      "eip155:84532",
		Allowance:    500_000,
		RequestLimit: 100_000,
	}
}

func TestAccessPassStoragePreservesConfigUnknownsAndPermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TTSBUDDY_EVM_PRIVATE_KEY", "wallet-secret-must-not-be-written")

	dir := filepath.Join(home, ".ttsbuddy")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	raw := `{
  "api_key": "` + fixtureCredential("ttsb", 'a', 'b') + `",
  "default_voice": "af_heart",
  "future_setting": {"keep": true}
}
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}

	pass := fixtureAccessPass('c', 'd')
	if err := SaveAccessPass(pass); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "wallet-secret-must-not-be-written") {
		t.Fatal("wallet environment value was persisted")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["future_setting"]; !ok {
		t.Fatalf("unknown field was not preserved: %s", data)
	}
	var stored struct {
		Credential   string    `json:"credential"`
		PurchaseID   string    `json:"purchase_id"`
		ExpiresAt    time.Time `json:"expires_at"`
		Network      string    `json:"network"`
		Allowance    int64     `json:"allowance_units"`
		RequestLimit int64     `json:"request_limit_units"`
	}
	if err := json.Unmarshal(doc["access_pass"], &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Credential != pass.Credential || stored.PurchaseID != pass.PurchaseID || stored.Network != pass.Network || stored.Allowance != pass.Allowance || stored.RequestLimit != pass.RequestLimit {
		t.Fatalf("stored access pass mismatch: %#v", stored)
	}

	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().Perm() != 0600 {
		t.Fatalf("config file mode = %o", stat.Mode().Perm())
	}
	dirStat, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirStat.Mode().Perm() != 0700 {
		t.Fatalf("config dir mode = %o", dirStat.Mode().Perm())
	}
}

func TestSaveAccessPassRejectsMalformedCredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pass := fixtureAccessPass('c', 'd')
	pass.Credential = "ttsp_abcd1234_secret"

	if err := SaveAccessPass(pass); err == nil {
		t.Fatal("malformed access pass credential was stored")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccessPass != nil {
		t.Fatalf("malformed pass should not be persisted: %#v", cfg.AccessPass)
	}
}

func TestAccessPassMutationPreservesConcurrentUnrelatedSet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	for attempt := 0; attempt < 200; attempt++ {
		if err := Save(&Config{DefaultVoice: "base"}); err != nil {
			t.Fatal(err)
		}
		voice := fmt.Sprintf("voice-%03d", attempt)
		pass := fixtureAccessPass('c', 'd')

		start := make(chan struct{})
		var wg sync.WaitGroup
		var saveErr error
		var setErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			saveErr = SaveAccessPass(pass)
		}()
		go func() {
			defer wg.Done()
			<-start
			setErr = Set("voice", voice)
		}()
		close(start)
		wg.Wait()
		if saveErr != nil {
			t.Fatalf("SaveAccessPass: %v", saveErr)
		}
		if setErr != nil {
			t.Fatalf("Set voice: %v", setErr)
		}

		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.AccessPass == nil || cfg.AccessPass.Credential != pass.Credential || cfg.DefaultVoice != voice {
			t.Fatalf("attempt %d lost a concurrent update: voice=%q pass=%#v", attempt, cfg.DefaultVoice, cfg.AccessPass)
		}
	}
}

func TestForgetAccessPassRequiresExactCredentialAndPreservesUnrelatedConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	apiKey := fixtureCredential("ttsb", 'a', 'b')
	pass := fixtureAccessPass('c', 'd')
	if err := Save(&Config{APIKey: apiKey, DefaultVoice: "af_heart", AccessPass: &pass}); err != nil {
		t.Fatal(err)
	}

	removed, err := ForgetAccessPass(fixtureCredential("ttsp", 'e', 'f'))
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("mismatched credential removed the pass")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccessPass == nil || cfg.AccessPass.Credential != pass.Credential {
		t.Fatalf("pass should remain after mismatched forget: %#v", cfg.AccessPass)
	}

	removed, err = ForgetAccessPass(pass.Credential)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("exact credential did not remove pass")
	}
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccessPass != nil || cfg.APIKey != apiKey || cfg.DefaultVoice != "af_heart" {
		t.Fatalf("forget changed unrelated config or left pass: %#v", cfg)
	}
}

func TestCLISessionStoragePreservesUnrelatedConfigAndCompares(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	permanent := fixtureCredential("ttsb", 'a', 'b')
	first := fixtureCredential("ttsc", 'c', 'd')
	second := fixtureCredential("ttsc", 'e', 'f')
	if err := Save(&Config{APIKey: permanent, DefaultVoice: "voice", CLIAuthURL: "https://example.test/auth"}); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if err := StoreCLISession("", StoredCLISession{Credential: first, ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	if err := StoreCLISession("", StoredCLISession{Credential: second, ExpiresAt: expires}); err == nil {
		t.Fatal("expected compare conflict")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != permanent || cfg.DefaultVoice != "voice" || cfg.CLISession.Credential != first {
		t.Fatalf("config not preserved: %#v", cfg)
	}
	if err := ClearCLISession(first); err != nil {
		t.Fatal(err)
	}
	cfg, _ = Load()
	if cfg.CLISession != nil || cfg.APIKey != permanent {
		t.Fatalf("clear changed unrelated config: %#v", cfg)
	}
	path, _ := ConfigPath()
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o", stat.Mode().Perm())
	}
}

func TestCLISessionAndAccessPassMutationsPreserveEachOther(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pass := fixtureAccessPass('a', 'b')
	cli := fixtureCredential("ttsc", 'c', 'd')
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	if err := Save(&Config{AccessPass: &pass}); err != nil {
		t.Fatal(err)
	}
	if err := StoreCLISession("", StoredCLISession{Credential: cli, ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}

	updatedPass := fixtureAccessPass('e', 'f')
	if err := SaveAccessPass(updatedPass); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CLISession == nil || cfg.CLISession.Credential != cli {
		t.Fatalf("access-pass save changed CLI session: %#v", cfg.CLISession)
	}
	if cfg.AccessPass == nil || cfg.AccessPass.Credential != updatedPass.Credential {
		t.Fatalf("CLI-session save lost access pass: %#v", cfg.AccessPass)
	}

	removed, err := ForgetAccessPass(updatedPass.Credential)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("exact access pass was not removed")
	}
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccessPass != nil || cfg.CLISession == nil || cfg.CLISession.Credential != cli {
		t.Fatalf("access-pass clear changed CLI session: pass=%#v session=%#v", cfg.AccessPass, cfg.CLISession)
	}

	if err := SaveAccessPass(updatedPass); err != nil {
		t.Fatal(err)
	}

	if err := ClearCLISession(cli); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CLISession != nil || cfg.AccessPass == nil || cfg.AccessPass.Credential != updatedPass.Credential {
		t.Fatalf("CLI-session clear changed access pass: session=%#v pass=%#v", cfg.CLISession, cfg.AccessPass)
	}
}

func TestActiveCLISessionValidation(t *testing.T) {
	now := time.Now().UTC()
	valid := fixtureCredential("ttsc", 'a', 'b')
	for name, cfg := range map[string]*Config{
		"malformed token":  {CLISession: &StoredCLISession{Credential: "bad", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}},
		"malformed expiry": {CLISession: &StoredCLISession{Credential: valid, ExpiresAt: "bad"}},
		"expired":          {CLISession: &StoredCLISession{Credential: valid, ExpiresAt: now.Add(-time.Second).Format(time.RFC3339)}},
	} {
		t.Run(name, func(t *testing.T) {
			session, warning := ActiveCLISession(cfg, now)
			if session != nil || warning == "" {
				t.Fatalf("session=%v warning=%q", session, warning)
			}
		})
	}
	session, warning := ActiveCLISession(&Config{CLISession: &StoredCLISession{Credential: valid, ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}}, now)
	if session == nil || warning != "" {
		t.Fatalf("session=%v warning=%q", session, warning)
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

func TestResolveCredentialPrecedenceIncludesActiveCLISession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	permanent := fixtureCredential("ttsb", 'a', 'b')
	cli := fixtureCredential("ttsc", 'c', 'd')
	environment := fixtureCredential("ttsb", 'e', 'f')
	flag := fixtureCredential("ttsb", '1', '2')
	cfg := &Config{APIKey: permanent, CLISession: &StoredCLISession{Credential: cli, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}}
	resolved, _ := Resolve(cfg, FlagValues{})
	if resolved.APIKey != cli {
		t.Fatalf("CLI = %q", resolved.APIKey)
	}
	t.Setenv("TTSBUDDY_API_KEY", environment)
	resolved, _ = Resolve(cfg, FlagValues{})
	if resolved.APIKey != environment {
		t.Fatalf("env = %q", resolved.APIKey)
	}
	resolved, _ = Resolve(cfg, FlagValues{APIKey: &flag})
	if resolved.APIKey != flag {
		t.Fatalf("flag = %q", resolved.APIKey)
	}
}

func TestResolveCredentialPrecedenceIncludesAccessPass(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	permanent := fixtureCredential("ttsb", 'a', 'b')
	cli := fixtureCredential("ttsc", 'c', 'd')
	storedPass := fixtureAccessPass('e', 'f')
	storedPass.ExpiresAt = time.Now().Add(-time.Hour).UTC()
	envAPI := fixtureCredential("ttsb", '1', '2')
	envPass := fixtureCredential("ttsp", '3', '4')
	flagAPI := fixtureCredential("ttsb", '5', '6')
	flagPass := fixtureCredential("ttsp", '7', '8')
	cfg := &Config{
		APIKey: permanent,
		CLISession: &StoredCLISession{
			Credential: cli,
			ExpiresAt:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
		AccessPass: &storedPass,
	}

	resolved, warnings := Resolve(cfg, FlagValues{})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if resolved.APIKey != storedPass.Credential || resolved.CredentialKind != CredentialKindAccessPass {
		t.Fatalf("stored pass should outrank stored session/key, got %q kind %q", resolved.APIKey, resolved.CredentialKind)
	}

	t.Setenv("TTSBUDDY_API_KEY", envAPI)
	resolved, _ = Resolve(cfg, FlagValues{})
	if resolved.APIKey != envAPI || resolved.CredentialKind != CredentialKindSubscription {
		t.Fatalf("env API key = %q kind %q", resolved.APIKey, resolved.CredentialKind)
	}

	t.Setenv("TTSBUDDY_ACCESS_PASS", envPass)
	resolved, _ = Resolve(cfg, FlagValues{})
	if resolved.APIKey != envPass || resolved.CredentialKind != CredentialKindAccessPass {
		t.Fatalf("env access pass = %q kind %q", resolved.APIKey, resolved.CredentialKind)
	}

	resolved, _ = Resolve(cfg, FlagValues{APIKey: &flagAPI})
	if resolved.APIKey != flagAPI || resolved.CredentialKind != CredentialKindSubscription {
		t.Fatalf("flag API key = %q kind %q", resolved.APIKey, resolved.CredentialKind)
	}

	resolved, _ = Resolve(cfg, FlagValues{APIKey: &flagPass})
	if resolved.APIKey != flagPass || resolved.CredentialKind != CredentialKindAccessPass {
		t.Fatalf("flag access pass = %q kind %q", resolved.APIKey, resolved.CredentialKind)
	}
}

func TestResolveMalformedStoredAccessPassFallsBackWithWarning(t *testing.T) {
	cli := fixtureCredential("ttsc", 'c', 'd')
	cfg := &Config{
		APIKey: fixtureCredential("ttsb", 'a', 'b'),
		CLISession: &StoredCLISession{
			Credential: cli,
			ExpiresAt:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
		AccessPass: &StoredAccessPass{Credential: "ttsp_abcd1234_secret"},
	}
	resolved, warnings := Resolve(cfg, FlagValues{})
	if resolved.APIKey != cli || resolved.CredentialKind != CredentialKindCLISession {
		t.Fatalf("CLI fallback = %q kind %q", resolved.APIKey, resolved.CredentialKind)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "stored access pass is malformed") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestResolveMalformedEnvironmentAccessPassIgnored(t *testing.T) {
	t.Setenv("TTSBUDDY_ACCESS_PASS", "ttsp_abcd1234_secret")
	resolved, warnings := Resolve(&Config{APIKey: fixtureCredential("ttsb", 'a', 'b')}, FlagValues{})
	if resolved.APIKey != fixtureCredential("ttsb", 'a', 'b') || resolved.CredentialKind != CredentialKindSubscription {
		t.Fatalf("malformed env access pass should not win, got %q kind %q", resolved.APIKey, resolved.CredentialKind)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "invalid TTSBUDDY_ACCESS_PASS") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestResolveMalformedEnvironmentAPIKeyIgnored(t *testing.T) {
	t.Setenv("TTSBUDDY_API_KEY", "ttsb_abcd1234_secret")
	storedPass := fixtureAccessPass('c', 'd')
	resolved, warnings := Resolve(&Config{AccessPass: &storedPass}, FlagValues{})
	if resolved.APIKey != storedPass.Credential || resolved.CredentialKind != CredentialKindAccessPass {
		t.Fatalf("malformed env API key should not win, got %q kind %q", resolved.APIKey, resolved.CredentialKind)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "invalid TTSBUDDY_API_KEY") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestResolveMalformedStoredAPIKeyIgnored(t *testing.T) {
	resolved, warnings := Resolve(&Config{APIKey: "ttsb_abcd1234_secret"}, FlagValues{})
	if resolved.APIKey != "" || resolved.CredentialKind != CredentialKindNone {
		t.Fatalf("malformed stored API key should not resolve, got %q kind %q", resolved.APIKey, resolved.CredentialKind)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "stored API key is malformed") {
		t.Fatalf("warnings = %v", warnings)
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
		return
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
		APIKey:        "ttsb_abc_secret",
		DefaultVoice:  "bf_emma",
		DefaultSpeed:  0.8,
		PollTimeout:   "5m",
		OutputDir:     "/tmp",
		APIURL:        "https://example.com",
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

func TestCheckInsecureURL(t *testing.T) {
	cases := []struct {
		url     string
		wantErr bool
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
		err := CheckInsecureURL(tc.url)
		if tc.wantErr && err == nil {
			t.Errorf("CheckInsecureURL(%q) should error", tc.url)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("CheckInsecureURL(%q) should not error, got: %v", tc.url, err)
		}
	}
}

func TestCheckCredentialedAPIURL(t *testing.T) {
	tests := []struct {
		name        string
		rawURL      string
		allowCustom bool
		wantErr     bool
	}{
		{name: "default www host allowed", rawURL: "https://www.ttsbuddy.com/v1/agent-tts", wantErr: false},
		{name: "apex prod host allowed", rawURL: "https://ttsbuddy.com/v1/agent-tts", wantErr: false},
		{name: "localhost http allowed for dev", rawURL: "http://localhost:54321/v1/agent-tts", wantErr: false},
		{name: "loopback http allowed for tests", rawURL: "http://127.0.0.1:8080/v1/agent-tts", wantErr: false},
		{name: "ipv6 loopback http allowed for tests", rawURL: "http://[::1]:8080/v1/agent-tts", wantErr: false},
		{name: "custom https denied by default", rawURL: "https://api.example.com/v1/agent-tts", wantErr: true},
		{name: "custom https allowed with explicit opt in", rawURL: "https://api.example.com/v1/agent-tts", allowCustom: true, wantErr: false},
		{name: "non-local http denied even with opt in", rawURL: "http://api.example.com/v1/agent-tts", allowCustom: true, wantErr: true},
		{name: "unsupported scheme denied", rawURL: "ftp://ttsbuddy.com/v1/agent-tts", allowCustom: true, wantErr: true},
		{name: "unsupported localhost scheme denied", rawURL: "ftp://localhost/v1/agent-tts", allowCustom: true, wantErr: true},
		{name: "hostless unsupported scheme denied", rawURL: "file:///tmp/api", allowCustom: true, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckCredentialedAPIURL(tt.rawURL, tt.allowCustom)
			if tt.wantErr && err == nil {
				t.Fatalf("CheckCredentialedAPIURL(%q, %v) returned nil, want error", tt.rawURL, tt.allowCustom)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("CheckCredentialedAPIURL(%q, %v) returned error: %v", tt.rawURL, tt.allowCustom, err)
			}
		})
	}
}

func TestResolveAllowCustomAPIURL(t *testing.T) {
	t.Setenv("TTSBUDDY_ALLOW_CUSTOM_API_URL", "")

	cfg := &Config{AllowCustomAPIURL: true}
	resolved, warnings := Resolve(cfg, FlagValues{})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if !resolved.AllowCustomAPIURL {
		t.Fatal("config AllowCustomAPIURL should resolve to true")
	}

	t.Setenv("TTSBUDDY_ALLOW_CUSTOM_API_URL", "false")
	resolved, warnings = Resolve(cfg, FlagValues{})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if resolved.AllowCustomAPIURL {
		t.Fatal("env false should override config true")
	}
}

func TestResolveAllowCustomAPIURLWarning(t *testing.T) {
	t.Setenv("TTSBUDDY_ALLOW_CUSTOM_API_URL", "sometimes")
	resolved, warnings := Resolve(&Config{}, FlagValues{})
	if resolved.AllowCustomAPIURL {
		t.Fatal("invalid env value should not enable custom API URLs")
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings: got %d, want 1 (%v)", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "invalid TTSBUDDY_ALLOW_CUSTOM_API_URL") {
		t.Fatalf("warning should mention invalid env var, got %q", warnings[0])
	}
}

func TestResolveClerkFrontendAPIURLUsesEnvironmentOptIn(t *testing.T) {
	t.Setenv("TTSBUDDY_CLERK_FRONTEND_API_URL", "https://clerk.example.test")
	t.Setenv("TTSBUDDY_ALLOW_CUSTOM_API_URL", "true")

	resolved, warnings := Resolve(&Config{}, FlagValues{})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if resolved.ClerkFrontendAPIURL != "https://clerk.example.test" {
		t.Fatalf("ClerkFrontendAPIURL = %q, want environment override", resolved.ClerkFrontendAPIURL)
	}
}

func TestResolveClerkFrontendAPIURLRejectsWithoutEnvironmentOptIn(t *testing.T) {
	t.Setenv("TTSBUDDY_CLERK_FRONTEND_API_URL", "https://clerk.example.test")
	t.Setenv("TTSBUDDY_ALLOW_CUSTOM_API_URL", "false")

	resolved, warnings := Resolve(&Config{}, FlagValues{})
	if resolved.ClerkFrontendAPIURL != DefaultClerkFAPIURL {
		t.Fatalf("ClerkFrontendAPIURL = %q, want shipped default", resolved.ClerkFrontendAPIURL)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "ignoring TTSBUDDY_CLERK_FRONTEND_API_URL") {
		t.Fatalf("warnings = %v, want one opt-in warning", warnings)
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
