package config

import (
	"fmt"
	"os"
	"strconv"
)

// FlagValues carries values from cobra flags. Pointer fields distinguish "not set" from zero.
type FlagValues struct {
	APIKey    *string
	Voice     *string
	Speed     *float64
	OutputDir *string
	Timeout   *string
}

// ResolvedConfig contains fully resolved values with all defaults applied.
type ResolvedConfig struct {
	APIKey        string
	APIURL        string
	TTSAPIBaseURL string
	Voice         string
	Speed         float64
	OutputDir     string
	PollTimeout   string
}

// Resolve applies precedence: flags > env vars > config file > defaults.
func Resolve(cfg *Config, flags FlagValues) *ResolvedConfig {
	r := &ResolvedConfig{
		APIKey:        cfg.APIKey,
		APIURL:        or(cfg.APIURL, DefaultAPIURL),
		TTSAPIBaseURL: or(cfg.TTSAPIBaseURL, DefaultTTSAPIBaseURL),
		Voice:         or(cfg.DefaultVoice, DefaultVoice),
		Speed:         orFloat(cfg.DefaultSpeed, DefaultSpeed),
		OutputDir:     or(cfg.OutputDir, DefaultOutputDir),
		PollTimeout:   or(cfg.PollTimeout, DefaultPollTimeout),
	}

	// Layer 2: environment variables override config file.
	applyEnv(r)

	// Layer 3: flags override everything.
	applyFlags(r, flags)

	return r
}

func applyEnv(r *ResolvedConfig) {
	if v := os.Getenv("TTSBUDDY_API_KEY"); v != "" {
		r.APIKey = v
	}
	if v := os.Getenv("TTSBUDDY_API_URL"); v != "" {
		r.APIURL = v
	}
	if v := os.Getenv("TTSBUDDY_TTS_API_BASE_URL"); v != "" {
		r.TTSAPIBaseURL = v
	}
	if v := os.Getenv("TTSBUDDY_VOICE"); v != "" {
		r.Voice = v
	}
	if v := os.Getenv("TTSBUDDY_SPEED"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: invalid TTSBUDDY_SPEED=%q, using default %.1f\n", v, r.Speed)
		} else {
			r.Speed = f
		}
	}
	if v := os.Getenv("TTSBUDDY_OUTPUT_DIR"); v != "" {
		r.OutputDir = v
	}
	if v := os.Getenv("TTSBUDDY_TIMEOUT"); v != "" {
		r.PollTimeout = v
	}
}

func applyFlags(r *ResolvedConfig, f FlagValues) {
	if f.APIKey != nil {
		r.APIKey = *f.APIKey
	}
	if f.Voice != nil {
		r.Voice = *f.Voice
	}
	if f.Speed != nil {
		r.Speed = *f.Speed
	}
	if f.OutputDir != nil {
		r.OutputDir = *f.OutputDir
	}
	if f.Timeout != nil {
		r.PollTimeout = *f.Timeout
	}
}

func or(val, fallback string) string {
	if val != "" {
		return val
	}
	return fallback
}

func orFloat(val, fallback float64) float64 {
	if val != 0 {
		return val
	}
	return fallback
}
