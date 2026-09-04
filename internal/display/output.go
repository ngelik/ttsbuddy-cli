package display

import (
	"encoding/json"
	"fmt"
	"os"
)

// ErrorGuidance returns an actionable message for each API error code.
func ErrorGuidance(code string) string {
	switch code {
	case "INVALID_KEY":
		return "Invalid credential. Run: ttsbuddy auth browser. For automation, use: ttsbuddy config set key <your-key>"
	case "INACTIVE_SUBSCRIPTION":
		return "Subscription inactive. Reactivate at https://ttsbuddy.com/billing"
	case "NO_API_ACCESS":
		return "Your plan does not include API access. Check your plan or contact support."
	case "RATE_LIMITED":
		return "Rate limited. Please wait and try again."
	case "USAGE_LIMIT_EXCEEDED":
		return "Monthly TTS minutes exhausted. Upgrade at https://ttsbuddy.com/billing"
	case "INVALID_REQUEST":
		return "Invalid request. Check your input."
	case "TEXT_TOO_LONG":
		return "Input exceeds 500,000 characters. Split into smaller chunks."
	case "NOT_FOUND":
		return "Job not found. Check the job ID."
	case "FILE_EXPIRED":
		return "Audio file has expired. Submit a new request."
	case "TTS_PROVIDER_ERROR":
		return "TTS provider error. Try again later."
	case "INTERNAL_ERROR":
		return "Server error. Try again later."
	default:
		return ""
	}
}

// PrintError writes an error message to stderr.
func PrintError(msg string) {
	fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
}

// PrintSuccess writes a success message to stderr.
func PrintSuccess(msg string) {
	fmt.Fprintf(os.Stderr, "%s\n", msg)
}

// PrintJSON writes a value as indented JSON to stdout.
func PrintJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
