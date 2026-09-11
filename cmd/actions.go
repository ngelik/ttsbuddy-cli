package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
)

// Action types are intentionally a small, stable allowlist. They describe
// what an agent can do next without copying provider prose into JSON.
const (
	actionAuthenticate    = "authenticate"
	actionBrowser         = "browser_authentication"
	actionVerifyCode      = "verify_code"
	actionRetrySubmission = "retry_submission"
	actionStatus          = "status"
	actionDownload        = "download"
	actionAccount         = "account"
	actionInputCorrection = "input_correction"
)

func commandAction(actionType string, argv ...string) *api.CLIAction {
	return &api.CLIAction{Type: actionType, Argv: withExecutionContext(argv...)}
}

func withExecutionContext(argv ...string) []string {
	result := append([]string(nil), argv...)
	configDir := flagConfigDir
	if configDir == "" {
		configDir = os.Getenv(config.ConfigDirEnv)
	}
	if configDir != "" {
		result = append(result, "--config-dir", configDir)
	}
	return result
}

func requiredAction(actionType string, inputs ...string) *api.CLIAction {
	return &api.CLIAction{Type: actionType, RequiredInputs: append([]string(nil), inputs...)}
}

func statusAction(jobID string) *api.CLIAction {
	if err := validateJobID(jobID); err != nil {
		return requiredAction(actionStatus, "job_id")
	}
	argv := []string{"ttsbuddy", "status", jobID}
	if flagJSON {
		argv = append(argv, "--json")
	}
	return commandAction(actionStatus, argv...)
}

func downloadAction(jobID, outputPath string) *api.CLIAction {
	if err := validateJobID(jobID); err != nil {
		return requiredAction(actionDownload, "job_id")
	}
	argv := []string{"ttsbuddy", "download", jobID}
	if outputPath != "" {
		// Preserve '-' as the stdout sentinel. Other explicit paths are
		// absolute so an agent can execute the action from another cwd.
		if outputPath != "-" {
			if absolute, err := filepath.Abs(outputPath); err == nil {
				outputPath = absolute
			}
		}
		argv = append(argv, "--output", outputPath)
	}
	// Binary stdout and JSON cannot share stdout. Keep recovery argv
	// executable even if a caller somehow combines both modes.
	if flagJSON && outputPath != "-" {
		argv = append(argv, "--json")
	}
	return commandAction(actionDownload, argv...)
}

func interruptedDownloadAction(jobID, outputPath string) *api.CLIAction {
	action := downloadAction(jobID, outputPath)
	if action != nil {
		return action
	}
	return requiredAction(actionDownload, "job_id")
}

func submissionRetryAction() *api.CLIAction {
	return requiredAction(actionRetrySubmission, "original_input")
}

func browserAuthAction() *api.CLIAction {
	return commandAction(actionBrowser, "ttsbuddy", "auth", "browser")
}

func authenticateAction() *api.CLIAction {
	return requiredAction(actionAuthenticate, "authentication_method")
}

func verifyCodeAction(challengeID string) *api.CLIAction {
	if challengeID == "" {
		return requiredAction(actionVerifyCode, "challenge_id", "verification_code")
	}
	action := commandAction(actionVerifyCode, "ttsbuddy", "auth", "email", "verify", "--challenge-id", challengeID, "--code-stdin", "--json")
	action.RequiredInputs = []string{"verification_code"}
	return action
}

func accountAction(url string) *api.CLIAction {
	if url == "" {
		return requiredAction(actionAccount, "account_action")
	}
	return &api.CLIAction{Type: actionAccount, URL: url}
}

func inputCorrectionAction() *api.CLIAction {
	return requiredAction(actionInputCorrection, "corrected_input")
}

func validateJobID(jobID string) error {
	if strings.TrimSpace(jobID) == "" {
		return fmt.Errorf("job ID must not be empty")
	}
	if strings.HasPrefix(jobID, "-") {
		return fmt.Errorf("job ID must not start with '-' (use an opaque job ID returned by TTS Buddy)")
	}
	return nil
}
