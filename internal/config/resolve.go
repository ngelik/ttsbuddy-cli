package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// FlagValues carries values from cobra flags. Pointer fields distinguish "not set" from zero.
type FlagValues struct {
	APIKey    *string
	Voice     *string
	Language  *string
	Speed     *float64
	OutputDir *string
	Timeout   *string
}

// ResolvedConfig contains fully resolved values with all defaults applied.
type ResolvedConfig struct {
	APIKey              string
	CredentialKind      CredentialKind
	AccessPass          *StoredAccessPass
	APIURL              string
	CLIAuthURL          string
	ClerkFrontendAPIURL string
	TTSAPIBaseURL       string
	AllowCustomAPIURL   bool
	Voice               string
	Language            string
	Speed               float64
	OutputDir           string
	PollTimeout         string
}

// Resolve applies credential precedence:
// --key > TTSBUDDY_ACCESS_PASS > TTSBUDDY_API_KEY > stored access pass >
// active CLI session > stored permanent API key.
// Non-credential values keep the existing flags > env vars > config > defaults order.
// Returns the resolved config and any warnings (e.g., invalid env var values).
// Callers should decide whether to print warnings based on output mode.
func Resolve(cfg *Config, flags FlagValues) (*ResolvedConfig, []string) {
	var warnings []string
	r := &ResolvedConfig{
		APIURL:              or(cfg.APIURL, DefaultAPIURL),
		CLIAuthURL:          or(cfg.CLIAuthURL, DefaultCLIAuthURL),
		ClerkFrontendAPIURL: DefaultClerkFAPIURL,
		TTSAPIBaseURL:       or(cfg.TTSAPIBaseURL, DefaultTTSAPIBaseURL),
		AllowCustomAPIURL:   cfg.AllowCustomAPIURL,
		Voice:               or(cfg.DefaultVoice, DefaultVoice),
		Language:            or(cfg.DefaultLanguage, DefaultLanguage),
		Speed:               orFloat(cfg.DefaultSpeed, DefaultSpeed),
		OutputDir:           or(cfg.OutputDir, DefaultOutputDir),
		PollTimeout:         or(cfg.PollTimeout, DefaultPollTimeout),
	}
	if cfg.APIKey != "" {
		if IsSubscriptionCredential(cfg.APIKey) {
			setCredential(r, cfg.APIKey, CredentialKindSubscription)
		} else {
			warnings = append(warnings, "stored API key is malformed; ignoring it")
		}
	}
	if session, warning := ActiveCLISession(cfg, time.Now()); session != nil {
		setCredential(r, session.Credential, CredentialKindCLISession)
	} else if warning != "" {
		warnings = append(warnings, warning)
	}
	if cfg.AccessPass != nil {
		pass := *cfg.AccessPass
		r.AccessPass = &pass
		if IsAccessPassCredential(pass.Credential) {
			setCredential(r, pass.Credential, CredentialKindAccessPass)
		} else {
			warnings = append(warnings, "stored access pass is malformed; ignoring it")
		}
	}

	// Layer 2: environment variables override config file.
	warnings = append(warnings, applyEnv(r)...)

	// Layer 3: flags override everything.
	applyFlags(r, flags)

	return r, warnings
}

func setCredential(r *ResolvedConfig, credential string, kind CredentialKind) {
	if credential == "" {
		return
	}
	r.APIKey = credential
	r.CredentialKind = kind
}

func applyEnv(r *ResolvedConfig) []string {
	var warnings []string
	if v := os.Getenv("TTSBUDDY_ALLOW_CUSTOM_API_URL"); v != "" {
		allow, err := strconv.ParseBool(v)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("invalid TTSBUDDY_ALLOW_CUSTOM_API_URL=%q, using default %v", v, r.AllowCustomAPIURL))
		} else {
			r.AllowCustomAPIURL = allow
		}
	}
	if v := os.Getenv("TTSBUDDY_API_KEY"); v != "" {
		if IsSubscriptionCredential(v) {
			setCredential(r, v, CredentialKindSubscription)
		} else {
			warnings = append(warnings, "invalid TTSBUDDY_API_KEY; ignoring it")
		}
	}
	if v := os.Getenv("TTSBUDDY_ACCESS_PASS"); v != "" {
		if IsAccessPassCredential(v) {
			setCredential(r, v, CredentialKindAccessPass)
		} else {
			warnings = append(warnings, "invalid TTSBUDDY_ACCESS_PASS; ignoring it")
		}
	}
	if v := os.Getenv("TTSBUDDY_API_URL"); v != "" {
		r.APIURL = v
	}
	if v := os.Getenv("TTSBUDDY_CLI_AUTH_URL"); v != "" {
		r.CLIAuthURL = v
	}
	if v := os.Getenv("TTSBUDDY_CLERK_FRONTEND_API_URL"); v != "" {
		if r.AllowCustomAPIURL || os.Getenv("TTSBUDDY_ALLOW_CUSTOM_API_URL") == "true" {
			r.ClerkFrontendAPIURL = v
		} else {
			warnings = append(warnings, "ignoring TTSBUDDY_CLERK_FRONTEND_API_URL without custom API URL opt-in")
		}
	}
	if v := os.Getenv("TTSBUDDY_TTS_API_BASE_URL"); v != "" {
		r.TTSAPIBaseURL = v
	}
	if v := os.Getenv("TTSBUDDY_VOICE"); v != "" {
		r.Voice = v
	}
	if v := os.Getenv("TTSBUDDY_LANGUAGE"); v != "" {
		if IsValidLanguageCode(v) {
			r.Language = v
		} else {
			warnings = append(warnings, fmt.Sprintf("invalid TTSBUDDY_LANGUAGE=%q, using default %s", v, r.Language))
		}
	}
	if v := os.Getenv("TTSBUDDY_SPEED"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("invalid TTSBUDDY_SPEED=%q, using default %.1f", v, r.Speed))
		} else {
			r.Speed = f
		}
	}
	if v := os.Getenv("TTSBUDDY_OUTPUT_DIR"); v != "" {
		r.OutputDir = v
	}
	if v := os.Getenv("TTSBUDDY_TIMEOUT"); v != "" {
		if _, err := time.ParseDuration(v); err != nil {
			warnings = append(warnings, fmt.Sprintf("invalid TTSBUDDY_TIMEOUT=%q, using default %s", v, r.PollTimeout))
		} else {
			r.PollTimeout = v
		}
	}
	return warnings
}

func applyFlags(r *ResolvedConfig, f FlagValues) {
	if f.APIKey != nil {
		if kind := CredentialKindFor(*f.APIKey); kind == CredentialKindSubscription || kind == CredentialKindAccessPass {
			setCredential(r, *f.APIKey, kind)
		} else {
			setCredential(r, *f.APIKey, CredentialKindNone)
		}
	}
	if f.Voice != nil {
		r.Voice = *f.Voice
	}
	if f.Language != nil {
		r.Language = *f.Language
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
