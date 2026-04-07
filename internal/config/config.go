package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAPIURL        = "https://ttsbuddy.com/v1/agent-tts"
	DefaultTTSAPIBaseURL = "https://ttsbuddy.com"
	DefaultVoice         = "st_m1"
	DefaultSpeed         = 1.0
	DefaultOutputDir     = "."
	DefaultPollTimeout   = "10m"
)

// Config represents the persisted configuration file.
type Config struct {
	APIKey        string  `json:"api_key,omitempty"`
	APIURL        string  `json:"api_url,omitempty"`
	TTSAPIBaseURL string  `json:"tts_api_base_url,omitempty"`
	DefaultVoice  string  `json:"default_voice,omitempty"`
	DefaultSpeed  float64 `json:"default_speed,omitempty"`
	OutputDir     string  `json:"output_dir,omitempty"`
	PollTimeout   string  `json:"poll_timeout,omitempty"`
}

// validKeys maps user-facing key names to Config field setters.
var validKeys = map[string]bool{
	"key":              true,
	"api_key":          true,
	"voice":            true,
	"speed":            true,
	"timeout":          true,
	"output_dir":       true,
	"api_url":          true,
	"tts_api_base_url": true,
}

// IsValidKey returns true if the key name is recognized.
func IsValidKey(key string) bool {
	return validKeys[key]
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
	case "tts_api_base_url":
		return cfg.TTSAPIBaseURL, nil
	default:
		return "", fmt.Errorf("unknown config key: %s", key)
	}
}

// Set updates a single config value by key name and saves.
func Set(key, value string) error {
	cfg, err := Load()
	if err != nil {
		return err
	}

	switch key {
	case "key", "api_key":
		cfg.APIKey = value
	case "voice":
		cfg.DefaultVoice = value
	case "speed":
		speed, parseErr := strconv.ParseFloat(value, 64)
		if parseErr != nil {
			return &ValidationError{Msg: fmt.Sprintf("invalid speed value: %s", value)}
		}
		if speed < 0.5 || speed > 1.5 {
			return &ValidationError{Msg: "speed must be between 0.5 and 1.5"}
		}
		cfg.DefaultSpeed = speed
	case "timeout":
		if _, parseErr := time.ParseDuration(value); parseErr != nil {
			return &ValidationError{Msg: fmt.Sprintf("invalid timeout value: %s (use Go duration syntax like 30s, 2m, 10m)", value)}
		}
		cfg.PollTimeout = value
	case "output_dir":
		cfg.OutputDir = value
	case "api_url":
		cfg.APIURL = value
	case "tts_api_base_url":
		cfg.TTSAPIBaseURL = value
	default:
		return &ValidationError{Msg: fmt.Sprintf("unknown config key: %s", key)}
	}

	return Save(cfg)
}

// CheckInsecureURL returns an error if the URL would send credentials over
// insecure HTTP to a non-localhost host. Returns nil if safe.
func CheckInsecureURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "http" {
		return nil // HTTPS or parse error (caught elsewhere)
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil // local dev is fine
	}
	return fmt.Errorf("refusing to send API key over insecure HTTP to %s — use HTTPS or localhost", host)
}

// FormatSpeed formats a speed value preserving full precision.
// 1.0→"1", 1.25→"1.25", 0.8→"0.8"
func FormatSpeed(speed float64) string {
	return strconv.FormatFloat(speed, 'f', -1, 64)
}

// RedactKey returns a redacted version of an API key, showing only the public_id.
// ttsb_a1b2c3d4_e5f6... → ttsb_a1b2c3d4_...
func RedactKey(key string) string {
	if key == "" {
		return ""
	}
	if !strings.HasPrefix(key, "ttsb_") {
		return "***"
	}
	// ttsb_<public_id>_<secret>
	parts := strings.SplitN(key, "_", 3)
	if len(parts) < 3 {
		return "***"
	}
	return fmt.Sprintf("ttsb_%s_...", parts[1])
}
