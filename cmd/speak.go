package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	urlPkg "net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
	"github.com/ngelik/ttsbuddy-cli/internal/display"
	"github.com/ngelik/ttsbuddy-cli/internal/markdown"
	"github.com/spf13/cobra"
)

var (
	speakFile           string
	speakVoice          string
	speakLanguage       string
	speakSpeed          float64
	speakOutput         string
	speakOutputDir      string
	speakTimeout        string
	speakRaw            bool
	speakNoDownload     bool
	speakIdempotencyKey string
)

var speakCmd = &cobra.Command{
	Use:   "speak [text]",
	Short: "Convert text to speech",
	Long: `Convert text to speech using the TTSBuddy API.

Input can be provided as:
  ttsbuddy speak "Hello world"     Inline text argument
  ttsbuddy speak -f article.md     Read from file (.md files auto-preprocessed)
  ttsbuddy speak -                 Read from stdin`,

	Args:         maxArgs(1),
	SilenceUsage: true,

	PreRunE: func(cmd *cobra.Command, args []string) error {
		// Validate conflicting flags
		if flagJSON && speakOutput == "-" {
			return &exitError{code: 2, msg: "--json and -o - are mutually exclusive (both write to stdout)"}
		}
		if speakNoDownload && speakOutput != "" {
			return &exitError{code: 2, msg: "--no-download and --output are mutually exclusive"}
		}
		return nil
	},

	RunE: func(cmd *cobra.Command, args []string) error {
		return runSpeak(cmd, args)
	},
}

func init() {
	speakCmd.Flags().StringVarP(&speakFile, "file", "f", "", "read text from file")
	speakCmd.Flags().StringVarP(&speakVoice, "voice", "v", "", "voice ID (default: st_m1)")
	speakCmd.Flags().StringVarP(&speakLanguage, "language", "l", "", "language code for Supertonic voices (e.g. en, fr, de, ja, ko)")
	speakCmd.Flags().Float64VarP(&speakSpeed, "speed", "s", 0, "speed 0.5-1.5 (default: 1.2)")
	speakCmd.Flags().StringVarP(&speakOutput, "output", "o", "", "output file (- for stdout)")
	speakCmd.Flags().StringVar(&speakOutputDir, "output-dir", "", "directory for auto-named files")
	speakCmd.Flags().StringVar(&speakTimeout, "timeout", "", "poll timeout (e.g. 30s, 2m, 10m)")
	speakCmd.Flags().BoolVar(&speakRaw, "raw", false, "skip markdown preprocessing")
	speakCmd.Flags().BoolVar(&speakNoDownload, "no-download", false, "print audio URL instead of downloading")
	speakCmd.Flags().StringVar(&speakIdempotencyKey, "idempotency-key", "", "override auto-generated idempotency key")

	rootCmd.AddCommand(speakCmd)
}

func runSpeak(cmd *cobra.Command, args []string) error {
	// 1. Use resolved config from root PersistentPreRunE, then apply speak-specific flags
	resolved := resolvedCfg
	if resolved == nil {
		return structuredExitError(1, "config not loaded", "CLI_ERROR", "INVALID_CONFIGURATION", "Run ttsbuddy doctor.", false, 0)
	}

	// Apply speak-specific flag overrides
	if cmd.Flags().Changed("voice") {
		resolved.Voice = speakVoice
	}
	languageExplicit := cmd.Flags().Changed("language")
	if languageExplicit {
		resolved.Language = speakLanguage
	}
	if cmd.Flags().Changed("speed") {
		resolved.Speed = speakSpeed
	}
	if cmd.Flags().Changed("output-dir") {
		resolved.OutputDir = speakOutputDir
	}
	if cmd.Flags().Changed("timeout") {
		resolved.PollTimeout = speakTimeout
	}

	if resolved.APIKey == "" {
		if resolved.CLISessionExpired {
			return structuredExitError(1, "CLI session has expired. "+authMethodSuggestion, "CLI_ERROR", "SESSION_EXPIRED", authMethodSuggestion, false, 0)
		}
		return structuredExitError(2, missingAPIKeyMessage, "CLI_ERROR", "AUTH_REQUIRED", authMethodSuggestion, false, 0)
	}

	// 2. Read input text (fromStdin true when input came from pipe or explicit "-")
	text, inputFile, fromStdin, err := readInput(args, speakFile)
	if err != nil {
		return err
	}

	// 3. Markdown preprocessing (before validation so stripped size is checked, not raw)
	if !speakRaw && inputFile != "" && isMarkdownFile(inputFile) {
		text = markdown.Strip(text)
		stderrMsg("Preprocessed markdown from %s\n", filepath.Base(inputFile))
	}

	if strings.TrimSpace(text) == "" {
		return &exitError{code: 2, msg: "no text provided"}
	}

	charCount := utf8.RuneCountInString(text)
	if charCount > 500_000 {
		return &exitError{code: 2, msg: fmt.Sprintf("input exceeds 500,000 characters (%d characters). Split into smaller chunks.", charCount)}
	}

	// 4. Resolve voice and speed
	voice := resolved.Voice
	speed := resolved.Speed
	language := strings.ToLower(strings.TrimSpace(resolved.Language))

	// Speed validation
	if speed < 0.5 || speed > 1.5 {
		return &exitError{code: 2, msg: fmt.Sprintf("speed must be between 0.5 and 1.5 (got %.2f)", speed)}
	}

	if language != "" && !config.IsValidLanguageCode(language) {
		return &exitError{code: 2, msg: fmt.Sprintf("invalid language code: %s", language)}
	}

	if strings.HasPrefix(voice, "st_") {
		if language == "" {
			language = config.DefaultLanguage
		}
	} else if languageExplicit {
		return &exitError{code: 2, msg: "--language is only supported with Supertonic st_* voices; choose a Kokoro voice for its native language"}
	} else {
		language = ""
	}

	// 5. Generate idempotency key
	// Stdin gets a random UUID (content may differ between pipe invocations).
	// File/arg gets a deterministic content hash (safe to retry same input).
	idemKey := speakIdempotencyKey
	if idemKey == "" {
		if fromStdin {
			idemKey = api.GenerateFromStdin()
		} else {
			idemKey = api.GenerateFromContent(text, voice, speed, language)
		}
	}

	// 6. Create API client
	client := api.NewClient(resolved.APIURL, resolved.APIKey, Version)

	// 7. Set up context with SIGINT handling
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// 8. Submit with retry
	req := api.SpeakRequest{
		Text:     text,
		Voice:    voice,
		Speed:    speed,
		Language: language,
	}

	spin := display.New()
	if !flagJSON && !flagQuiet {
		spin.Start("Submitting TTS request...")
	}

	retryResult := api.WithRetryResult(ctx, api.DefaultRetryConfig(), func(key string) (*api.TTSResponse, int, error) {
		return client.Speak(ctx, req, key)
	}, idemKey)
	resp, status, err := retryResult.Response, retryResult.Status, retryResult.Err

	if err != nil {
		spin.Stop()
		if ctx.Err() != nil {
			if !flagJSON {
				fmt.Fprintln(os.Stderr, "\nInterrupted.")
			}
			mapped := structuredExitError(130, "interrupted while submitting the request", "CLI_ERROR", "REQUEST_INTERRUPTED", "Retry the same request with --idempotency-key <same-value>.", true, 0)
			mapped.idempotencyKey = retryResult.EffectiveKey
			mapped.action = submissionRetryAction()
			return mapped
		}
		return classifyAPIErrorWithKey(err, status, retryResult.EffectiveKey)
	}
	spin.Stop()

	// Save last job for any accepted request (both sync and async)
	if resp.JobID != "" {
		if err := config.SaveLastJob(resp.JobID); err != nil {
			stderrMsg("Warning: could not save job ID: %v\n", err)
		}
	}

	allowFreshDownloadRetry := speakIdempotencyKey == "" && !fromStdin && !flagJSON && !speakNoDownload
	return handleSpeakResponse(ctx, client, req, resp, status, resolved, allowFreshDownloadRetry)
}

func handleSpeakResponse(ctx context.Context, client *api.Client, req api.SpeakRequest, resp *api.TTSResponse, status int, resolved *config.ResolvedConfig, allowFreshDownloadRetry bool) error {
	switch {
	case resp.Status == "completed":
		return handleCompletedWithFreshRetry(ctx, client, req, resp, resolved, allowFreshDownloadRetry)

	case resp.Status == "expired":
		return structuredExitError(1, "audio file has expired and been deleted. Submit a new request.", "CLI_ERROR", "AUDIO_EXPIRED", "Submit a new request with a fresh idempotency key.", false, 0)

	case resp.Status == "failed":
		return classifyTerminalResponse(resp, "")

	case status == 202 || resp.Status == "processing":
		renderTranslationMeta(resp)
		return pollUntilComplete(ctx, client, resp, resolved, func(done *api.TTSResponse) error {
			return handleCompletedWithFreshRetry(ctx, client, req, done, resolved, allowFreshDownloadRetry)
		}, statusAction)

	default:
		return &exitError{code: 1, msg: fmt.Sprintf("unexpected response status: %s", resp.Status)}
	}
}

func pollUntilComplete(ctx context.Context, client *api.Client, initial *api.TTSResponse, resolved *config.ResolvedConfig, onCompleted func(*api.TTSResponse) error, recoveryAction func(string) *api.CLIAction) error {
	jobID := initial.JobID
	spin := display.New()
	if !flagJSON && !flagQuiet {
		shortID := jobID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		spin.Start(fmt.Sprintf("Queued job %s...", shortID))
	}
	defer spin.Stop()

	// Parse timeout — fail fast on invalid values instead of silently defaulting
	timeout, err := time.ParseDuration(resolved.PollTimeout)
	if err != nil {
		invalid := structuredExitError(2, fmt.Sprintf("invalid timeout value: %s (use Go duration syntax like 30s, 2m, 10m)", resolved.PollTimeout), "CLI_ERROR", "INVALID_TIMEOUT", "Use Go duration syntax such as 30s, 2m, or 10m.", false, 0)
		invalid.action = recoveryAction(jobID)
		return invalid
	}

	deadline := time.Now().Add(timeout)

	// Initial delay from server hint or 3s default
	delay := 3 * time.Second
	var delayHint *int
	if initial.RetryAfterSeconds != nil {
		delay = time.Duration(*initial.RetryAfterSeconds) * time.Second
		delayHint = initial.RetryAfterSeconds
	}

	for {
		// Check timeout
		if time.Now().After(deadline) {
			timedOut := structuredExitError(1, fmt.Sprintf("polling timed out after %s. Resume with: ttsbuddy status %s", resolved.PollTimeout, jobID), "CLI_ERROR", "POLL_TIMEOUT", fmt.Sprintf("Resume with: ttsbuddy status %s", jobID), true, 0)
			timedOut.action = recoveryAction(jobID)
			return timedOut
		}
		if timeoutErr := pollDelayDeadlineError(jobID, deadline, delay, delayHint); timeoutErr != nil {
			timeoutErr.action = recoveryAction(jobID)
			return timeoutErr
		}

		// Wait
		select {
		case <-ctx.Done():
			// SIGINT: print resume info
			if !flagJSON {
				fmt.Fprintf(os.Stderr, "\nInterrupted. Resume with: ttsbuddy status %s\n", jobID)
			}
			interrupted := structuredExitError(130, fmt.Sprintf("interrupted while polling job %s", jobID), "CLI_ERROR", "POLL_INTERRUPTED", fmt.Sprintf("Resume with: ttsbuddy status %s", jobID), true, 0)
			interrupted.action = recoveryAction(jobID)
			return interrupted
		case <-time.After(delay):
		}

		// Poll
		resp, pollStatus, err := client.GetStatus(ctx, jobID)
		if err != nil {
			if isPermanentError(err, pollStatus) {
				// A permanent response is authoritative. Preserve classifier
				// actions (for example authenticate/account/input correction)
				// rather than replacing them with a known-job recovery command.
				return handleAPIError(err, pollStatus)
			}
			stderrMsg("Status check failed (HTTP %d), retrying...\n", pollStatus)
			if resp != nil && resp.RetryAfterSeconds != nil {
				delay = time.Duration(*resp.RetryAfterSeconds) * time.Second
				delayHint = resp.RetryAfterSeconds
				if timeoutErr := pollDelayDeadlineError(jobID, deadline, delay, resp.RetryAfterSeconds); timeoutErr != nil {
					timeoutErr.action = recoveryAction(jobID)
					return timeoutErr
				}
			} else {
				delay = minDuration(delay*3/2, 15*time.Second)
				delayHint = nil
			}
			continue
		}

		switch resp.Status {
		case "completed":
			return onCompleted(resp)
		case "expired":
			expired := structuredExitError(1, fmt.Sprintf("job %s: audio file has expired. Submit a new request.", jobID), "CLI_ERROR", "AUDIO_EXPIRED", "Submit a new request with a fresh idempotency key.", false, 0)
			expired.action = submissionRetryAction()
			return expired
		case "failed":
			// Terminal failures use their own classifier action (if any),
			// never a download/status recovery for the already-finished job.
			failed := classifyTerminalResponse(resp, jobID)
			if failed.action == nil {
				failed.action = submissionRetryAction()
			}
			return failed
		case "processing":
			if resp.RetryAfterSeconds != nil {
				delay = time.Duration(*resp.RetryAfterSeconds) * time.Second
				delayHint = resp.RetryAfterSeconds
			} else {
				delay = minDuration(delay*3/2, 15*time.Second)
				delayHint = nil
			}
			elapsed := time.Since(deadline.Add(-timeout))
			spin.Update(renderProgress(resp, elapsed))
		default:
			unexpected := &exitError{code: 1, msg: fmt.Sprintf("unexpected job status %q from API. Job ID: %s", resp.Status, jobID)}
			unexpected.action = recoveryAction(jobID)
			return unexpected
		}
	}
}

func pollDelayDeadlineError(jobID string, deadline time.Time, delay time.Duration, retryAfter *int) *exitError {
	remaining := time.Until(deadline)
	if delay <= remaining {
		return nil
	}
	next := fmt.Sprintf("Resume with: ttsbuddy status %s", jobID)
	message := fmt.Sprintf("polling deadline reached before the next status check for job %s; %s", jobID, next)
	retrySeconds := 0
	if retryAfter != nil && *retryAfter >= 0 {
		retrySeconds = *retryAfter
	}
	return structuredExitError(1, message, "CLI_ERROR", "POLL_TIMEOUT", next, true, retrySeconds)
}

func handleCompleted(ctx context.Context, client *api.Client, resp *api.TTSResponse, resolved *config.ResolvedConfig) error {
	// Validate audio_url BEFORE emitting JSON — ensures --json never returns
	// exit 0 with an unusable payload. Execute() handles JSON error output.
	if err := validateCompletedAudioURL(resp, resolved); err != nil {
		return err
	}

	// Bare --json mode remains the raw API response and does not download.
	// An explicit output path opts into download metadata below.
	if flagJSON && speakOutput == "" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}

	// --no-download: just show URL
	if speakNoDownload {
		fmt.Fprintln(os.Stderr, resp.AudioURL)
		renderTranslationMeta(resp)
		renderCompletionSummary(resp, 0)
		return nil
	}

	result, err := downloadCompletedAudio(ctx, client, resp, resolved, speakOutput)
	if err != nil {
		return wrapDownloadFailure(ctx, resp.JobID, speakOutput, resp.AudioURL, err)
	}
	// -o - writes the raw MP3 directly and has no JSON metadata mode.
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

func handleCompletedWithFreshRetry(ctx context.Context, client *api.Client, req api.SpeakRequest, resp *api.TTSResponse, resolved *config.ResolvedConfig, allowFreshRetry bool) error {
	err := handleCompleted(ctx, client, resp, resolved)
	if err == nil {
		return nil
	}

	if !allowFreshRetry || !shouldRetryWithFreshKey(err) {
		printDownloadFailure(err)
		return err
	}

	stderrMsg("Cached audio URL was not downloadable; retrying with a fresh job...\n")
	freshRetryResult := api.WithRetryResult(ctx, api.DefaultRetryConfig(), func(key string) (*api.TTSResponse, int, error) {
		return client.Speak(ctx, req, key)
	}, api.GenerateNew())
	freshResp, status, freshErr := freshRetryResult.Response, freshRetryResult.Status, freshRetryResult.Err
	if freshErr != nil {
		if ctx.Err() != nil {
			if !flagJSON {
				fmt.Fprintln(os.Stderr, "\nInterrupted.")
			}
			mapped := structuredExitError(130, "interrupted while retrying the request", "CLI_ERROR", "REQUEST_INTERRUPTED", "Retry the same request with --idempotency-key <same-value>.", true, 0)
			mapped.idempotencyKey = freshRetryResult.EffectiveKey
			return mapped
		}
		return classifyAPIErrorWithKey(freshErr, status, freshRetryResult.EffectiveKey)
	}

	if freshResp.JobID != "" {
		if err := config.SaveLastJob(freshResp.JobID); err != nil {
			stderrMsg("Warning: could not save job ID: %v\n", err)
		}
	}

	return handleSpeakResponse(ctx, client, req, freshResp, status, resolved, false)
}

func shouldRetryWithFreshKey(err error) bool {
	var dl *downloadFailure
	if !errors.As(err, &dl) {
		return false
	}
	return api.IsRetryableDownloadError(dl.err)
}

func printDownloadFailure(err error) {
	var dl *downloadFailure
	if !errors.As(err, &dl) || dl.audioURL == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "Download failed: %s\nAudio URL: %s\n", redactDownloadErrorForDisplay(dl.err, dl.audioURL), redactURLForDisplay(dl.audioURL))
}

func redactURLForDisplay(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := urlPkg.Parse(rawURL)
	if err != nil {
		return "(invalid URL)"
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	return u.String()
}

func redactDownloadErrorForDisplay(err error, audioURL string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	redacted := redactURLForDisplay(audioURL)
	for _, sensitive := range sensitiveURLVariants(audioURL) {
		if sensitive == "" {
			continue
		}
		msg = strings.ReplaceAll(msg, sensitive, redacted)
	}
	return msg
}

func sensitiveURLVariants(rawURL string) []string {
	if rawURL == "" {
		return nil
	}
	variants := []string{rawURL}
	u, err := urlPkg.Parse(rawURL)
	if err != nil {
		return variants
	}
	withoutFragment := *u
	withoutFragment.Fragment = ""
	if v := withoutFragment.String(); v != rawURL {
		variants = append(variants, v)
	}
	if v := u.Redacted(); v != rawURL {
		variants = append(variants, v)
	}
	withoutFragmentRedacted := withoutFragment.Redacted()
	if withoutFragmentRedacted != rawURL && withoutFragmentRedacted != withoutFragment.String() {
		variants = append(variants, withoutFragmentRedacted)
	}
	if u.User != nil {
		masked := u.Scheme + "://" + u.User.Username() + ":***@" + u.Host + u.RequestURI()
		if u.Fragment != "" {
			variants = append(variants, masked+"#"+u.Fragment)
		}
		variants = append(variants, masked)
	}
	return variants
}

type downloadFailure struct {
	err      error
	audioURL string
}

func (e *downloadFailure) Error() string {
	return "download failed"
}

func (e *downloadFailure) Unwrap() error {
	return e.err
}

func validateCompletedAudioURL(resp *api.TTSResponse, resolved *config.ResolvedConfig) error {
	if resp.AudioURL == "" {
		return &exitError{code: 1, msg: "completed but no audio URL in response"}
	}

	apiHost := apiHostFromURL(resolved.APIURL)
	if err := api.ValidateDownloadURL(resp.AudioURL, apiHost); err != nil {
		if strings.HasPrefix(err.Error(), "invalid download URL:") {
			return &exitError{code: 1, msg: "invalid audio URL in API response"}
		}
		return &exitError{code: 1, msg: err.Error()}
	}

	return nil
}

func downloadToStdout(ctx context.Context, audioURL, apiURL string) error {
	apiHost := apiHostFromURL(apiURL)
	if err := api.ValidateDownloadURL(audioURL, apiHost); err != nil {
		return &exitError{code: 1, msg: err.Error()}
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	resp, err := api.OpenDownload(ctx, audioURL, apiHost)
	if err != nil {
		if ctx.Err() != nil {
			if !flagJSON {
				fmt.Fprintln(os.Stderr, "\nInterrupted.")
			}
			return structuredExitError(130, "interrupted while downloading audio", "CLI_ERROR", "DOWNLOAD_INTERRUPTED", "Retry the same request.", true, 0)
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	_, err = api.CopyBounded(os.Stdout, resp.Body, 500*1024*1024)
	if err != nil && ctx.Err() != nil {
		if !flagJSON {
			fmt.Fprintln(os.Stderr, "\nInterrupted.")
		}
		return structuredExitError(130, "interrupted while downloading audio", "CLI_ERROR", "DOWNLOAD_INTERRUPTED", "Retry the same request.", true, 0)
	}
	return err
}

func apiHostFromURL(rawURL string) string {
	u, _ := urlPkg.Parse(rawURL)
	if u == nil {
		return ""
	}
	return u.Hostname()
}

// --- Input helpers ---

// maxInputSize is a byte-level memory safety cap (~2MB to cover 500k multi-byte chars).
// The actual 500k character limit is enforced via utf8.RuneCountInString after reading.
const maxInputSize = 2*1024*1024 + 1024

func readInput(args []string, filePath string) (text string, inputFile string, fromStdin bool, err error) {
	hasArg := len(args) > 0 && args[0] != "-"
	hasStdin := len(args) > 0 && args[0] == "-"
	hasFile := filePath != ""

	sources := 0
	if hasArg {
		sources++
	}
	if hasStdin {
		sources++
	}
	if hasFile {
		sources++
	}

	if sources == 0 {
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			t, e := readBounded(os.Stdin)
			return t, "", true, e
		}
		return "", "", false, &exitError{code: 2, msg: "no input provided. Use: ttsbuddy speak \"text\", speak -f <file>, or pipe input"}
	}

	if sources > 1 {
		return "", "", false, &exitError{code: 2, msg: "only one input source allowed (text argument, -f file, or stdin -)"}
	}

	if hasArg {
		return args[0], "", false, nil
	}

	if hasStdin {
		t, e := readBounded(os.Stdin)
		return t, "", true, e
	}

	// File input — open first, then validate on the fd to close TOCTOU gap
	f, openErr := os.Open(filePath)
	if openErr != nil {
		if os.IsNotExist(openErr) {
			return "", "", false, &exitError{code: 2, msg: fmt.Sprintf("file not found: %s", filePath)}
		}
		return "", "", false, &exitError{code: 1, msg: fmt.Sprintf("reading file: %v", openErr)}
	}
	defer func() { _ = f.Close() }()

	info, statErr := f.Stat()
	if statErr != nil {
		return "", "", false, &exitError{code: 1, msg: fmt.Sprintf("reading file: %v", statErr)}
	}
	if !info.Mode().IsRegular() {
		return "", "", false, &exitError{code: 2, msg: fmt.Sprintf("not a regular file: %s", filePath)}
	}

	content, readErr := readBounded(f)
	if readErr != nil {
		return "", "", false, readErr
	}
	return content, filePath, false, nil
}

// readBounded reads from r up to maxInputSize, returning an error if exceeded.
func readBounded(r io.Reader) (string, error) {
	limited := io.LimitReader(r, maxInputSize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", &exitError{code: 1, msg: fmt.Sprintf("reading input: %v", err)}
	}
	if len(data) > maxInputSize {
		return "", &exitError{code: 2, msg: "input too large. The limit is 500,000 characters — split into smaller chunks."}
	}
	return string(data), nil
}

func isMarkdownFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".md" || ext == ".markdown"
}

// --- Error helpers ---

type exitError struct {
	code              int
	msg               string
	err               error
	jsonPayload       any
	errorCode         string
	serverCode        string
	reason            string
	retryable         bool
	retryAfterSeconds int
	nextAction        string
	action            *api.CLIAction
	idempotencyKey    string
}

func (e *exitError) Error() string { return e.msg }

func (e *exitError) Unwrap() error { return e.err }

func handleAPIError(err error, status int) error {
	return classifyAPIError(err, status)
}

// classifyTerminalResponse routes provider/job terminal states through the
// same recovery contract as HTTP failures, while never copying provider error
// messages into user-facing output.
func classifyTerminalResponse(resp *api.TTSResponse, jobID string) *exitError {
	if resp == nil {
		return structuredExitError(1, "TTS generation failed", "CLI_ERROR", "SERVICE_ERROR", "Retry the same request with --idempotency-key <same-value>.", true, 0)
	}
	status := 200
	if resp.Status == "expired" {
		message := "audio file has expired. Submit a new request."
		if jobID != "" {
			message = fmt.Sprintf("Job %s: audio file has expired. Submit a new request.", jobID)
		}
		return structuredExitError(1, message, "CLI_ERROR", "AUDIO_EXPIRED", "Submit a new request with a fresh idempotency key.", false, 0)
	}
	if resp.Error == nil {
		message := "TTS generation failed"
		if jobID != "" {
			message = fmt.Sprintf("Job %s: TTS generation failed", jobID)
		}
		return structuredExitError(1, message, "CLI_ERROR", "SERVICE_ERROR", "Submit again with a fresh idempotency key after checking the request.", false, 0)
	}
	mapped := classifyAPIError(&api.APIResponseError{StatusCode: status, Response: *resp}, status)
	if jobID != "" {
		mapped.msg = fmt.Sprintf("Job %s: %s", jobID, mapped.msg)
	}
	// A cached terminal failure is definitive for this job. Reusing its key
	// would replay the same failed result, so require a fresh submission key.
	mapped.nextAction = "Submit again with a fresh idempotency key after checking the request."
	mapped.retryable = false
	if api.NeedsNewIdempotencyKey(resp.Error) {
		mapped.nextAction = "Submit again with a fresh idempotency key (for example: --idempotency-key <new-value>)."
		mapped.retryable = false
	}
	return mapped
}

func stderrMsg(format string, a ...interface{}) {
	if flagJSON || flagQuiet {
		return
	}
	fmt.Fprintf(os.Stderr, format, a...)
}

// isPermanentError returns true if the HTTP status indicates a non-retryable error.
// Covers auth (401), forbidden (403), not found (404), and bad request (400).
func isPermanentError(err error, httpStatus int) bool {
	return httpStatus == 400 || httpStatus == 401 || httpStatus == 403 || httpStatus == 404
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
