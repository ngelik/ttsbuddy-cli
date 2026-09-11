package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
	"github.com/ngelik/ttsbuddy-cli/internal/display"
	"github.com/spf13/cobra"
)

var (
	downloadOutput    string
	downloadOutputDir string
	downloadTimeout   string
)

var downloadCmd = &cobra.Command{
	Use:          "download <job_id>",
	Short:        "Download audio for an existing completed job",
	SilenceUsage: true,
	Args:         exactArgs(1),
	PreRunE: func(cmd *cobra.Command, _ []string) error {
		if flagJSON && downloadOutput == "-" {
			return &exitError{code: 2, msg: "--json and --output - are mutually exclusive (both write to stdout)"}
		}
		return nil
	},
	RunE: runDownload,
}

func init() {
	downloadCmd.Flags().StringVarP(&downloadOutput, "output", "o", "", "output file (- for stdout)")
	downloadCmd.Flags().StringVar(&downloadOutputDir, "output-dir", "", "directory for auto-named files")
	downloadCmd.Flags().StringVar(&downloadTimeout, "timeout", "", "poll timeout (e.g. 30s, 2m, 10m)")
	rootCmd.AddCommand(downloadCmd)
}

func runDownload(cmd *cobra.Command, args []string) error {
	resolved := resolvedCfg
	if resolved == nil {
		return structuredExitError(1, "config not loaded", "CLI_ERROR", "INVALID_CONFIGURATION", "Run ttsbuddy doctor.", false, 0)
	}
	if resolved.APIKey == "" {
		if resolved.CLISessionExpired {
			mapped := structuredExitError(1, "CLI session has expired. "+authMethodSuggestion, "CLI_ERROR", "SESSION_EXPIRED", authMethodSuggestion, false, 0)
			mapped.action = authenticateAction()
			return mapped
		}
		mapped := structuredExitError(2, missingAPIKeyMessage, "CLI_ERROR", "AUTH_REQUIRED", authMethodSuggestion, false, 0)
		mapped.action = authenticateAction()
		return mapped
	}

	if cmd.Flags().Changed("output-dir") {
		resolved.OutputDir = downloadOutputDir
	}
	if cmd.Flags().Changed("timeout") {
		resolved.PollTimeout = downloadTimeout
	}
	if timeout, err := time.ParseDuration(resolved.PollTimeout); err != nil || timeout <= 0 {
		mapped := structuredExitError(2, fmt.Sprintf("invalid timeout value: %s (use Go duration syntax like 30s, 2m, 10m)", resolved.PollTimeout), "CLI_ERROR", "INVALID_TIMEOUT", "Use Go duration syntax such as 30s, 2m, or 10m.", false, 0)
		mapped.action = downloadAction(args[0], downloadOutput)
		return mapped
	}
	jobID := args[0]
	if err := validateJobID(jobID); err != nil {
		return structuredExitError(2, err.Error(), "CLI_ERROR", "INVALID_ARGUMENT", "Use the opaque job ID returned by TTS Buddy.", false, 0)
	}
	client := api.NewClient(resolved.APIURL, resolved.APIKey, Version)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	resp, status, err := client.GetStatus(ctx, jobID)
	if err != nil {
		if ctx.Err() != nil {
			interrupted := structuredExitError(130, fmt.Sprintf("interrupted while checking job %s", jobID), "CLI_ERROR", "DOWNLOAD_INTERRUPTED", fmt.Sprintf("Resume with: ttsbuddy download %s", jobID), true, 0)
			interrupted.action = downloadAction(jobID, downloadOutput)
			return interrupted
		}
		return handleDownloadStatusError(err, status, jobID)
	}
	return handleDownloadStatus(ctx, client, resp, resolved, jobID)
}

func handleDownloadStatus(ctx context.Context, client *api.Client, resp *api.TTSResponse, resolved *config.ResolvedConfig, jobID string) error {
	if resp == nil {
		mapped := structuredExitError(1, "job status response was empty", "CLI_ERROR", "INVALID_RESPONSE", fmt.Sprintf("Retry: ttsbuddy download %s", jobID), true, 0)
		mapped.action = downloadAction(jobID, downloadOutput)
		return mapped
	}
	switch resp.Status {
	case "completed":
		return runCompletedDownload(ctx, client, resp, resolved, jobID)
	case "processing":
		return pollUntilComplete(ctx, client, resp, resolved, func(done *api.TTSResponse) error {
			return runCompletedDownload(ctx, client, done, resolved, jobID)
		}, func(jobID string) *api.CLIAction { return downloadAction(jobID, downloadOutput) })
	case "failed", "expired":
		mapped := classifyTerminalResponse(resp, jobID)
		if mapped.action == nil {
			mapped.action = submissionRetryAction()
		}
		return mapped
	default:
		mapped := structuredExitError(1, fmt.Sprintf("unexpected job status %q from API. Job ID: %s", resp.Status, jobID), "CLI_ERROR", "INVALID_RESPONSE", fmt.Sprintf("Retry: ttsbuddy download %s", jobID), true, 0)
		mapped.action = downloadAction(jobID, downloadOutput)
		return mapped
	}
}

func handleDownloadStatusError(err error, status int, jobID string) error {
	var apiErr *api.APIResponseError
	if errors.As(err, &apiErr) {
		if apiErr.ErrorCode() == api.ErrNotFound {
			mapped := structuredExitError(1, fmt.Sprintf("job not found: %s", jobID), "CLI_ERROR", "JOB_NOT_FOUND", fmt.Sprintf("Check the job ID and retry: ttsbuddy download %s", jobID), false, 0)
			mapped.serverCode = api.ErrNotFound
			mapped.action = downloadAction(jobID, downloadOutput)
			return mapped
		}
		mapped := classifyAPIError(err, status)
		if mapped.action == nil && mapped.retryable {
			mapped.action = downloadAction(jobID, downloadOutput)
		}
		return mapped
	}
	mapped := structuredExitError(1, "checking job status failed", "CLI_ERROR", "TRANSPORT_ERROR", fmt.Sprintf("Retry: ttsbuddy download %s", jobID), true, 0)
	mapped.action = downloadAction(jobID, downloadOutput)
	return mapped
}

func runCompletedDownload(ctx context.Context, client *api.Client, resp *api.TTSResponse, resolved *config.ResolvedConfig, jobID string) error {
	if resp == nil {
		return structuredExitError(1, "job status response was empty", "CLI_ERROR", "INVALID_RESPONSE", fmt.Sprintf("Retry: ttsbuddy download %s", jobID), true, 0)
	}
	if err := validateCompletedAudioURL(resp, resolved); err != nil {
		return err
	}

	dlSpin := display.New()
	if !flagJSON && !flagQuiet && downloadOutput != "-" {
		dlSpin.Start("Downloading audio...")
	}
	result, err := downloadCompletedAudio(ctx, client, resp, resolved, downloadOutput)
	dlSpin.Stop()
	if err != nil {
		return wrapDownloadFailure(ctx, jobID, downloadOutput, resp.AudioURL, err)
	}
	if result == nil {
		return nil
	}

	if flagJSON {
		payload, err := responseWithDownload(resp, result)
		if err != nil {
			return &exitError{code: 1, msg: "encoding download result", err: err}
		}
		return json.NewEncoder(os.Stdout).Encode(payload)
	}
	stderrMsg("Saved to %s\n", result.Path)
	renderTranslationMeta(resp)
	renderCompletionSummary(resp, result.Bytes)
	return nil
}

type downloadResult struct {
	Path  string
	Bytes int64
}

func downloadCompletedAudio(ctx context.Context, client *api.Client, resp *api.TTSResponse, resolved *config.ResolvedConfig, output string) (*downloadResult, error) {
	if output == "-" {
		return nil, downloadToStdout(ctx, resp.AudioURL, resolved.APIURL)
	}
	destPath := output
	if destPath == "" {
		voice := resolved.Voice
		if resp.Audio != nil && resp.Audio.Voice != "" {
			voice = resp.Audio.Voice
		}
		destPath = api.AutoFilename(voice, resolved.OutputDir)
	}
	if output == "" {
		absPath, _ := filepath.Abs(destPath)
		absDir, _ := filepath.Abs(resolved.OutputDir)
		if !strings.HasPrefix(absPath, absDir+string(filepath.Separator)) && absPath != absDir {
			return nil, &exitError{code: 2, msg: fmt.Sprintf("output path %s escapes output directory %s", destPath, resolved.OutputDir)}
		}
	}
	dir := filepath.Dir(destPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, &exitError{code: 2, msg: fmt.Sprintf("output directory does not exist: %s", dir)}
	}
	bytesWritten, err := client.DownloadAudioWithSize(ctx, resp.AudioURL, destPath)
	if err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(destPath)
	if err != nil {
		absPath = destPath
	}
	return &downloadResult{Path: absPath, Bytes: bytesWritten}, nil
}

func responseWithDownload(resp *api.TTSResponse, result *downloadResult) (map[string]any, error) {
	data, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	payload["download"] = map[string]any{"path": result.Path, "bytes": result.Bytes}
	return payload, nil
}

func wrapDownloadFailure(ctx context.Context, jobID, outputPath, audioURL string, err error) error {
	if existing, ok := err.(*exitError); ok && existing.code == 130 {
		existing.action = interruptedDownloadAction(jobID, outputPath)
		existing.nextAction = fmt.Sprintf("Resume with: ttsbuddy download %s", jobID)
		return existing
	}
	if errors.Is(err, context.Canceled) || (ctx != nil && ctx.Err() != nil) {
		interrupted := structuredExitError(130, fmt.Sprintf("interrupted while downloading job %s", jobID), "CLI_ERROR", "DOWNLOAD_INTERRUPTED", fmt.Sprintf("Resume with: ttsbuddy download %s", jobID), true, 0)
		interrupted.action = interruptedDownloadAction(jobID, outputPath)
		return interrupted
	}
	if existing, ok := err.(*exitError); ok {
		return existing
	}
	failure := &exitError{
		code:       1,
		msg:        "download failed",
		err:        &downloadFailure{err: err, audioURL: audioURL},
		errorCode:  "CLI_ERROR",
		reason:     "DOWNLOAD_FAILED",
		nextAction: fmt.Sprintf("Retry download for job %s.", jobID),
		retryable:  true,
		action:     downloadAction(jobID, outputPath),
	}
	return failure
}
