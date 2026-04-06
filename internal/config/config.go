package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultAPIURL        = "https://hndrrtessqblbnkimjdw.supabase.co/functions/v1/agent-tts"
	DefaultTTSAPIBaseURL = "https://tts.api.prod.ttsbuddy.website"
	DefaultVoice         = "af_heart"
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

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("cannot write config file: %w", err)
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
		return fmt.Sprintf("%.1f", cfg.DefaultSpeed), nil
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
