package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultAPIURL        = "https://www.ttsbuddy.com/v1/agent-tts"
	DefaultTTSAPIBaseURL = "https://tts.api.prod.ttsbuddy.website"
	DefaultVoice         = "st_m1"
	DefaultLanguage      = "en"
	DefaultSpeed         = 1.2
	DefaultOutputDir     = "."
	DefaultPollTimeout   = "10m"
	DefaultCLIAuthURL    = "https://www.ttsbuddy.com/v1/cli-auth"
	DefaultClerkFAPIURL  = "https://clerk.ttsbuddy.com"
)

// Config represents the persisted configuration file.
type Config struct {
	APIKey            string            `json:"api_key,omitempty"`
	CLISession        *StoredCLISession `json:"cli_session,omitempty"`
	AccessPass        *StoredAccessPass `json:"access_pass,omitempty"`
	CLIAuthURL        string            `json:"cli_auth_url,omitempty"`
	APIURL            string            `json:"api_url,omitempty"`
	TTSAPIBaseURL     string            `json:"tts_api_base_url,omitempty"`
	AllowCustomAPIURL bool              `json:"allow_custom_api_url,omitempty"`
	DefaultVoice      string            `json:"default_voice,omitempty"`
	DefaultLanguage   string            `json:"default_language,omitempty"`
	DefaultSpeed      float64           `json:"default_speed,omitempty"`
	OutputDir         string            `json:"output_dir,omitempty"`
	PollTimeout       string            `json:"poll_timeout,omitempty"`
	extra             map[string]json.RawMessage
}

type StoredCLISession struct {
	Credential string `json:"credential"`
	ExpiresAt  string `json:"expires_at"`
}

type StoredAccessPass struct {
	Credential   string    `json:"credential"`
	PurchaseID   string    `json:"purchase_id"`
	ExpiresAt    time.Time `json:"expires_at"`
	Network      string    `json:"network"`
	Allowance    int64     `json:"allowance_units"`
	RequestLimit int64     `json:"request_limit_units"`
}

var knownConfigJSONFields = map[string]bool{
	"api_key":              true,
	"cli_session":          true,
	"access_pass":          true,
	"cli_auth_url":         true,
	"api_url":              true,
	"tts_api_base_url":     true,
	"allow_custom_api_url": true,
	"default_voice":        true,
	"default_language":     true,
	"default_speed":        true,
	"output_dir":           true,
	"poll_timeout":         true,
}

var configMutationMu sync.Mutex

func (c *Config) UnmarshalJSON(data []byte) error {
	type configJSON Config
	var decoded configJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = Config(decoded)
	c.extra = nil

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for key := range knownConfigJSONFields {
		delete(raw, key)
	}
	if len(raw) > 0 {
		c.extra = raw
	}
	return nil
}

func (c Config) MarshalJSON() ([]byte, error) {
	type configJSON Config
	data, err := json.Marshal(configJSON(c))
	if err != nil {
		return nil, err
	}
	if len(c.extra) == 0 {
		return data, nil
	}

	var merged map[string]json.RawMessage
	if err := json.Unmarshal(data, &merged); err != nil {
		return nil, err
	}
	for key, value := range c.extra {
		if knownConfigJSONFields[key] {
			continue
		}
		merged[key] = value
	}
	return json.Marshal(merged)
}

// validKeys maps user-facing key names to Config field setters.
var validKeys = map[string]bool{
	"key":                  true,
	"api_key":              true,
	"voice":                true,
	"language":             true,
	"default_language":     true,
	"speed":                true,
	"timeout":              true,
	"output_dir":           true,
	"api_url":              true,
	"cli_auth_url":         true,
	"tts_api_base_url":     true,
	"allow_custom_api_url": true,
}

// IsValidKey returns true if the key name is recognized.
func IsValidKey(key string) bool {
	return validKeys[key]
}

func IsValidLanguageCode(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 2 || len(value) > 8 {
		return false
	}
	for i, r := range value {
		if r == '-' && i > 0 && i < len(value)-1 {
			continue
		}
		if r < 'A' || (r > 'Z' && r < 'a') || r > 'z' {
			return false
		}
	}
	return true
}

// ValidationError represents a user input validation failure (exit code 2).
// Distinguished from runtime/IO errors (exit code 1).
type ValidationError struct {
	Msg string
}

func (e *ValidationError) Error() string { return e.Msg }

// configDir returns the path to ~/.ttsbuddy without creating it.
func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".ttsbuddy"), nil
}

// ConfigDir returns the path to ~/.ttsbuddy, creating it with 0700 if needed.
// Use this only for write operations.
func ConfigDir() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("cannot create config directory: %w", err)
	}
	return dir, nil
}

// ConfigPath returns the path to ~/.ttsbuddy/config.json without creating the directory.
func ConfigPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config file. Returns a zero-value Config if the file doesn't exist.
// Returns an error for real filesystem failures (permission denied, HOME unset, etc.).
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("cannot read config file: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("cannot parse config file: %w", err)
	}
	return &cfg, nil
}

// Save writes the config to disk with 0600 permissions.
// Creates ~/.ttsbuddy/ with 0700 if it doesn't exist.
func Save(cfg *Config) error {
	// Ensure directory exists for writes
	if _, err := ConfigDir(); err != nil {
		return err
	}
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot marshal config: %w", err)
	}
	data = append(data, '\n')

	return atomicWriteFile(path, data, 0600)
}

// atomicWriteFile writes data to a unique temp file then renames, preventing
// truncated files from interrupts/crashes and clobbering from concurrent writers.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("cannot create temp file: %w", err)
	}
	tmp := f.Name()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("cannot write file: %w", err)
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("cannot set file permissions: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cannot close temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cannot finalize file: %w", err)
	}
	return nil
}

// Get returns a single config value by key name.
func Get(cfg *Config, key string) (string, error) {
	switch key {
	case "key", "api_key":
		return RedactKey(cfg.APIKey), nil
	case "voice":
		return cfg.DefaultVoice, nil
	case "language", "default_language":
		return cfg.DefaultLanguage, nil
	case "speed":
		if cfg.DefaultSpeed == 0 {
			return "", nil
		}
		return FormatSpeed(cfg.DefaultSpeed), nil
	case "timeout":
		return cfg.PollTimeout, nil
	case "output_dir":
		return cfg.OutputDir, nil
	case "api_url":
		return cfg.APIURL, nil
	case "cli_auth_url":
		return cfg.CLIAuthURL, nil
	case "tts_api_base_url":
		return cfg.TTSAPIBaseURL, nil
	case "allow_custom_api_url":
		return strconv.FormatBool(cfg.AllowCustomAPIURL), nil
	default:
		return "", fmt.Errorf("unknown config key: %s", key)
	}
}

// Set updates a single config value by key name and saves.
func Set(key, value string) error {
	return mutateAndSaveConfig(func(cfg *Config) (bool, error) {
		switch key {
		case "key", "api_key":
			if !IsManualSubscriptionCredential(value) {
				return false, &ValidationError{Msg: "API key must start with 'ttsb_' and match the expected key format"}
			}
			cfg.APIKey = value
		case "voice":
			cfg.DefaultVoice = value
		case "language", "default_language":
			if !IsValidLanguageCode(value) {
				return false, &ValidationError{Msg: fmt.Sprintf("invalid language code: %s", value)}
			}
			cfg.DefaultLanguage = strings.ToLower(value)
		case "speed":
			speed, parseErr := strconv.ParseFloat(value, 64)
			if parseErr != nil {
				return false, &ValidationError{Msg: fmt.Sprintf("invalid speed value: %s", value)}
			}
			if speed < 0.5 || speed > 1.5 {
				return false, &ValidationError{Msg: "speed must be between 0.5 and 1.5"}
			}
			cfg.DefaultSpeed = speed
		case "timeout":
			if _, parseErr := time.ParseDuration(value); parseErr != nil {
				return false, &ValidationError{Msg: fmt.Sprintf("invalid timeout value: %s (use Go duration syntax like 30s, 2m, 10m)", value)}
			}
			cfg.PollTimeout = value
		case "output_dir":
			cfg.OutputDir = value
		case "api_url":
			cfg.APIURL = value
		case "cli_auth_url":
			cfg.CLIAuthURL = value
		case "tts_api_base_url":
			cfg.TTSAPIBaseURL = value
		case "allow_custom_api_url":
			allow, parseErr := strconv.ParseBool(value)
			if parseErr != nil {
				return false, &ValidationError{Msg: fmt.Sprintf("invalid allow_custom_api_url value: %s (use true or false)", value)}
			}
			cfg.AllowCustomAPIURL = allow
		default:
			return false, &ValidationError{Msg: fmt.Sprintf("unknown config key: %s", key)}
		}

		return true, nil
	})
}

// SaveAccessPass stores a validated prepaid access pass without touching
// unrelated configuration values.
func SaveAccessPass(pass StoredAccessPass) error {
	if !IsAccessPassCredential(pass.Credential) {
		return &ValidationError{Msg: "invalid access pass credential"}
	}
	if strings.TrimSpace(pass.PurchaseID) == "" || strings.TrimSpace(pass.Network) == "" {
		return &ValidationError{Msg: "invalid access pass metadata"}
	}
	if pass.Allowance <= 0 || pass.RequestLimit <= 0 || pass.RequestLimit > pass.Allowance {
		return &ValidationError{Msg: "invalid access pass limits"}
	}
	return mutateAndSaveConfig(func(cfg *Config) (bool, error) {
		next := pass
		cfg.AccessPass = &next
		return true, nil
	})
}

// ForgetAccessPass removes the stored access pass only when the caller names
// the exact credential it previously observed.
func ForgetAccessPass(expectedCredential string) (bool, error) {
	removed := false
	err := mutateAndSaveConfig(func(cfg *Config) (bool, error) {
		if cfg.AccessPass == nil {
			return false, nil
		}
		if cfg.AccessPass.Credential != expectedCredential {
			return false, nil
		}
		cfg.AccessPass = nil
		removed = true
		return true, nil
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}

func mutateAndSaveConfig(update func(*Config) (bool, error)) error {
	configMutationMu.Lock()
	defer configMutationMu.Unlock()

	cfg, err := Load()
	if err != nil {
		return err
	}
	changed, err := update(cfg)
	if err != nil || !changed {
		return err
	}
	return Save(cfg)
}

// CheckInsecureURL returns an error if the URL would send credentials over
// insecure HTTP to a non-localhost host. Returns nil if safe.
func CheckInsecureURL(rawURL string) error {
	return CheckCredentialedAPIURL(rawURL, true)
}

// CheckCredentialedAPIURL returns an error if a configured API URL is not a
// trusted destination for bearer credentials.
func CheckCredentialedAPIURL(rawURL string, allowCustom bool) error {
	u, err := url.Parse(rawURL)
	if err != nil || rawURL == "" || u.Scheme == "" {
		return nil
	}

	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())

	switch scheme {
	case "http":
		if host == "" {
			return fmt.Errorf("refusing to send API key to invalid API URL host")
		}
		if isLocalAPIHost(host) {
			return nil
		}
		return fmt.Errorf("refusing to send API key over insecure HTTP to %s; use HTTPS or localhost", host)
	case "https":
		if host == "" {
			return fmt.Errorf("refusing to send API key to invalid API URL host")
		}
		if isLocalAPIHost(host) || isOfficialAPIHost(host) || allowCustom {
			return nil
		}
		return fmt.Errorf("refusing to send API key to custom API URL host %q; set allow_custom_api_url=true or TTSBUDDY_ALLOW_CUSTOM_API_URL=true to opt in", host)
	default:
		return fmt.Errorf("refusing to send API key to unsupported API URL scheme %q", u.Scheme)
	}
}

func isOfficialAPIHost(host string) bool {
	return host == "ttsbuddy.com" || host == "www.ttsbuddy.com" || host == "clerk.ttsbuddy.com"
}

func isLocalAPIHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// FormatSpeed formats a speed value preserving full precision.
// 1.0→"1", 1.25→"1.25", 0.8→"0.8"
func FormatSpeed(speed float64) string {
	return strconv.FormatFloat(speed, 'f', -1, 64)
}

// RedactCredential returns a redacted version of a bearer credential, showing
// only the public_id.
func RedactCredential(credential string) string {
	if credential == "" {
		return ""
	}
	if !strings.HasPrefix(credential, "ttsb_") && !strings.HasPrefix(credential, "ttsc_") && !strings.HasPrefix(credential, "ttsp_") {
		return "***"
	}
	// ttsb_<public_id>_<secret>
	parts := strings.SplitN(credential, "_", 3)
	if len(parts) < 3 {
		return "***"
	}
	return fmt.Sprintf("%s_%s_...", parts[0], parts[1])
}

// RedactKey returns a redacted version of an API key, showing only the public_id.
// ttsb_a1b2c3d4_e5f6... -> ttsb_a1b2c3d4_...
func RedactKey(key string) string { return RedactCredential(key) }

type CredentialKind string

const (
	CredentialKindNone         CredentialKind = ""
	CredentialKindSubscription CredentialKind = "subscription"
	CredentialKindAccessPass   CredentialKind = "access_pass"
	CredentialKindCLISession   CredentialKind = "cli_session"
)

func CredentialKindFor(credential string) CredentialKind {
	switch {
	case validCredential(credential, "ttsb"):
		return CredentialKindSubscription
	case validCredential(credential, "ttsp"):
		return CredentialKindAccessPass
	case validCredential(credential, "ttsc"):
		return CredentialKindCLISession
	default:
		return CredentialKindNone
	}
}

func IsSubscriptionCredential(credential string) bool {
	return validCredential(credential, "ttsb")
}

func IsAccessPassCredential(credential string) bool {
	return validCredential(credential, "ttsp")
}

func IsManualSubscriptionCredential(credential string) bool {
	return validCredential(credential, "ttsb")
}

func IsManualCredential(credential string) bool {
	return validCredential(credential, "ttsb") || validCredential(credential, "ttsp")
}

func validCredential(value, prefix string) bool {
	parts := strings.Split(value, "_")
	if len(parts) != 3 || parts[0] != prefix || len(parts[1]) != 8 || len(parts[2]) != 48 {
		return false
	}
	for _, part := range parts[1:] {
		for _, r := range part {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				return false
			}
		}
	}
	return true
}

func validCLICredential(value string) bool { return validCredential(value, "ttsc") }

// ActiveCLISession validates the stored credential and absolute server expiry.
func ActiveCLISession(cfg *Config, now time.Time) (*StoredCLISession, string) {
	if cfg == nil || cfg.CLISession == nil {
		return nil, ""
	}
	if !validCLICredential(cfg.CLISession.Credential) {
		return nil, "stored CLI session is malformed; ignoring it"
	}
	expires, err := time.Parse(time.RFC3339, cfg.CLISession.ExpiresAt)
	if err != nil {
		return nil, "stored CLI session expiry is malformed; ignoring it"
	}
	if !expires.After(now) {
		return nil, "stored CLI session has expired; run: ttsbuddy auth login"
	}
	copy := *cfg.CLISession
	return &copy, ""
}

func sessionCredential(cfg *Config) string {
	if cfg == nil || cfg.CLISession == nil {
		return ""
	}
	return cfg.CLISession.Credential
}

// StoreCLISession atomically replaces only the expected prior CLI session.
func StoreCLISession(expectedPrior string, next StoredCLISession) error {
	if session, warning := ActiveCLISession(&Config{CLISession: &next}, time.Now()); session == nil || warning != "" {
		return &ValidationError{Msg: "invalid CLI session"}
	}
	return mutateAndSaveConfig(func(cfg *Config) (bool, error) {
		if sessionCredential(cfg) != expectedPrior {
			return false, fmt.Errorf("CLI session changed concurrently")
		}
		cfg.CLISession = &next
		return true, nil
	})
}

// ClearCLISession removes only the exact stored CLI session that was used.
func ClearCLISession(expectedCredential string) error {
	return mutateAndSaveConfig(func(cfg *Config) (bool, error) {
		if sessionCredential(cfg) != expectedCredential {
			return false, fmt.Errorf("CLI session changed concurrently")
		}
		cfg.CLISession = nil
		return true, nil
	})
}
