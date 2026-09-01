package api

import "fmt"

// Error code constants matching the agent-tts API.
const (
	ErrInvalidKey           = "INVALID_KEY"
	ErrInactiveSubscription = "INACTIVE_SUBSCRIPTION"
	ErrNoAPIAccess          = "NO_API_ACCESS"
	ErrRateLimited          = "RATE_LIMITED"
	ErrUsageLimitExceeded   = "USAGE_LIMIT_EXCEEDED"
	ErrInvalidRequest       = "INVALID_REQUEST"
	ErrTextTooLong          = "TEXT_TOO_LONG"
	ErrNotFound             = "NOT_FOUND"
	ErrFileExpired          = "FILE_EXPIRED"
	ErrTTSProviderError     = "TTS_PROVIDER_ERROR"
	ErrInternalError        = "INTERNAL_ERROR"
)

// SpeakRequest is the POST body for agent-tts.
type SpeakRequest struct {
	Text            string  `json:"text"`
	Voice           string  `json:"voice,omitempty"`
	Speed           float64 `json:"speed,omitempty"`
	Language        string  `json:"language,omitempty"`
	Source          string  `json:"source,omitempty"`
	Webpage         string  `json:"webpage,omitempty"`
	SourceTitle     string  `json:"source_title,omitempty"`
	ArticleLanguage string  `json:"article_language,omitempty"`
	Translate       string  `json:"translate,omitempty"`
}

// TTSResponse is the unified response shape from agent-tts.
// Pointer fields handle optional data (e.g. ExpiresAt is nil for new non-persisted jobs).
type TTSResponse struct {
	Success           bool       `json:"success"`
	Status            string     `json:"status,omitempty"`
	JobID             string     `json:"job_id,omitempty"`
	AudioURL          string     `json:"audio_url,omitempty"`
	StatusURL         string     `json:"status_url,omitempty"`
	RetryAfterSeconds *int       `json:"retry_after_seconds,omitempty"`
	Progress          *Progress  `json:"progress,omitempty"`
	Audio             *AudioInfo `json:"audio,omitempty"`
	Billing           *Billing   `json:"billing,omitempty"`
	Stats             *Stats     `json:"stats,omitempty"`
	Error             *APIError  `json:"error,omitempty"`
	Meta              *Meta      `json:"meta,omitempty"`
}

// Progress contains provider-backed job progress. Percent is absent when unknown.
type Progress struct {
	Phase   string `json:"phase,omitempty"`
	Percent *int   `json:"percent,omitempty"`
	Message string `json:"message,omitempty"`
}

// AudioInfo contains audio file metadata. All fields except Format may be absent.
type AudioInfo struct {
	DurationSeconds *float64 `json:"duration_seconds,omitempty"`
	FileSizeBytes   *int64   `json:"file_size_bytes,omitempty"`
	Format          string   `json:"format"`
	Voice           string   `json:"voice"`
	Speed           float64  `json:"speed"`
	ExpiresAt       string   `json:"expires_at,omitempty"`
}

// Stats contains final generation statistics.
type Stats struct {
	CharactersCount          *int     `json:"characters_count,omitempty"`
	SpeechLengthSeconds      *float64 `json:"speech_length_seconds,omitempty"`
	FileSizeBytes            *int64   `json:"file_size_bytes,omitempty"`
	GenerationSeconds        *float64 `json:"generation_seconds,omitempty"`
	GenerationCharsPerSecond *float64 `json:"generation_chars_per_second,omitempty"`
}

// Billing contains subscription usage data. Limit/Remaining are nil for unlimited tiers.
type Billing struct {
	Mode                    string   `json:"mode"`
	EstimatedCostCents      int      `json:"estimated_cost_cents"`
	MonthlyMinutesUsed      *float64 `json:"monthly_minutes_used,omitempty"`
	MonthlyMinutesLimit     *float64 `json:"monthly_minutes_limit"`
	MonthlyMinutesRemaining *float64 `json:"monthly_minutes_remaining"`
}

// APIError is the structured error from the API.
type APIError struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

// Meta contains request metadata.
type Meta struct {
	RequestID        string `json:"request_id"`
	ProcessingTimeMs *int   `json:"processing_time_ms,omitempty"`
	APIVersion       string `json:"api_version"`
	Source           string `json:"source,omitempty"`
	SourceURL        string `json:"source_url,omitempty"`
	SourceLanguage   string `json:"source_language,omitempty"`
	TargetLanguage   string `json:"target_language,omitempty"`
	Translated       *bool  `json:"translated,omitempty"`
}

// APIResponseError wraps an API error response as a Go error.
type APIResponseError struct {
	StatusCode int
	Response   TTSResponse
}

func (e *APIResponseError) Error() string {
	if e.Response.Error != nil {
		return fmt.Sprintf("API error %d: [%s] %s", e.StatusCode, e.Response.Error.Code, e.Response.Error.Message)
	}
	return fmt.Sprintf("API error %d", e.StatusCode)
}

// ErrorCode returns the API error code, or empty string if none.
func (e *APIResponseError) ErrorCode() string {
	if e.Response.Error != nil {
		return e.Response.Error.Code
	}
	return ""
}

// CLIError represents a local CLI failure for --json output.
type CLIError struct {
	Success bool `json:"success"`
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// NewCLIError creates a CLIError for local failures.
func NewCLIError(code, message string) CLIError {
	e := CLIError{Success: false}
	e.Error.Code = code
	e.Error.Message = message
	return e
}

// IsRetryable returns true if the error/status combination should be retried.
func IsRetryable(code string, httpStatus int) bool {
	if httpStatus == 429 {
		return true
	}
	if httpStatus == 502 || httpStatus == 500 {
		return true
	}
	return false
}

// NeedsNewIdempotencyKey returns true if the error message indicates
// the client should generate a fresh idempotency key before retrying.
func NeedsNewIdempotencyKey(err *APIError) bool {
	if err == nil {
		return false
	}
	return contains(err.Message, "new Idempotency-Key") || contains(err.Message, "Use a new Idempotency-Key")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
