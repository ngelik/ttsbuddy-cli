package display

import "testing"

func TestErrorGuidanceKnownCodes(t *testing.T) {
	codes := []string{
		"INVALID_KEY",
		"INACTIVE_SUBSCRIPTION",
		"NO_API_ACCESS",
		"RATE_LIMITED",
		"USAGE_LIMIT_EXCEEDED",
		"INVALID_REQUEST",
		"TEXT_TOO_LONG",
		"NOT_FOUND",
		"FILE_EXPIRED",
		"TTS_PROVIDER_ERROR",
		"INTERNAL_ERROR",
	}
	for _, code := range codes {
		guidance := ErrorGuidance(code)
		if guidance == "" {
			t.Errorf("ErrorGuidance(%q) returned empty string", code)
		}
	}
}

func TestErrorGuidanceUnknownCode(t *testing.T) {
	guidance := ErrorGuidance("COMPLETELY_UNKNOWN_CODE")
	if guidance != "" {
		t.Errorf("expected empty for unknown code, got %q", guidance)
	}
}
