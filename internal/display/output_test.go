package display

import (
	"io"
	"os"
	"strings"
	"testing"
)

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

func TestSpinnerUpdateBeforeStartIsSilent(t *testing.T) {
	previousStderr := os.Stderr
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writePipe
	t.Cleanup(func() {
		os.Stderr = previousStderr
		_ = readPipe.Close()
	})

	spinner := &Spinner{isTTY: false}
	spinner.Update("should not be printed")
	_ = writePipe.Close()

	got, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Update before Start wrote %q to stderr", got)
	}
}

func TestInvalidKeyGuidanceOffersBothInteractiveAuthMethods(t *testing.T) {
	guidance := ErrorGuidance("INVALID_KEY")
	if !strings.Contains(guidance, "ttsbuddy auth email") || !strings.Contains(guidance, "ttsbuddy auth browser") {
		t.Fatalf("guidance=%q, want both email and browser login methods", guidance)
	}
	if strings.Contains(guidance, "auth email | auth browser") {
		t.Fatalf("guidance emitted a shell-pipe suggestion: %q", guidance)
	}
}
