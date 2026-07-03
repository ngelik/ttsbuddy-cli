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
		return &exitError{code: 1, msg: "config not loaded"}
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
		return &exitError{code: 2, msg: "no API key configured. Run: ttsbuddy config set key <your-key>"}
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

	resp, status, err := api.WithRetry(ctx, api.DefaultRetryConfig(), func(key string) (*api.TTSResponse, int, error) {
		return client.Speak(ctx, req, key)
	}, idemKey)

	if err != nil {
		spin.Stop()
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\nInterrupted.")
			os.Exit(130)
		}
		return handleAPIError(err, status)
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
		return &exitError{code: 1, msg: "audio file has expired and been deleted. Submit a new request."}

	case resp.Status == "failed":
		msg := "TTS generation failed"
		if resp.Error != nil {
			msg = resp.Error.Message
		}
		if resp.Error != nil && api.NeedsNewIdempotencyKey(resp.Error) {
			return &exitError{code: 1, msg: msg + "\nUse --idempotency-key with a new value to retry."}
		}
		return &exitError{code: 1, msg: msg}

	case status == 202 || resp.Status == "processing":
		renderTranslationMeta(resp)
		return pollUntilComplete(ctx, client, resp, resolved, func(done *api.TTSResponse) error {
			return handleCompletedWithFreshRetry(ctx, client, req, done, resolved, allowFreshDownloadRetry)
		})

	default:
		return &exitError{code: 1, msg: fmt.Sprintf("unexpected response status: %s", resp.Status)}
	}
}

func pollUntilComplete(ctx context.Context, client *api.Client, initial *api.TTSResponse, resolved *config.ResolvedConfig, onCompleted func(*api.TTSResponse) error) error {
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
		return &exitError{code: 2, msg: fmt.Sprintf("invalid timeout value: %s (use Go duration syntax like 30s, 2m, 10m)", resolved.PollTimeout)}
	}

	deadline := time.Now().Add(timeout)

	// Initial delay from server hint or 3s default
	delay := 3 * time.Second
	if initial.RetryAfterSeconds != nil {
		delay = time.Duration(*initial.RetryAfterSeconds) * time.Second
	}

	for {
		// Check timeout
		if time.Now().After(deadline) {
			return &exitError{code: 1, msg: fmt.Sprintf("polling timed out after %s. Resume with: ttsbuddy status %s", resolved.PollTimeout, jobID)}
		}

		// Wait
		select {
		case <-ctx.Done():
			// SIGINT: print resume info
			fmt.Fprintf(os.Stderr, "\nInterrupted. Resume with: ttsbuddy status %s\n", jobID)
			os.Exit(130)
		case <-time.After(delay):
		}

		// Poll
		resp, pollStatus, err := client.GetStatus(ctx, jobID)
		if err != nil {
			if isPermanentError(err, pollStatus) {
				return handleAPIError(err, pollStatus)
			}
			stderrMsg("Status check error: %v, retrying...\n", err)
			delay = minDuration(delay*3/2, 15*time.Second)
			continue
		}

		switch resp.Status {
		case "completed":
			return onCompleted(resp)
		case "expired":
			return &exitError{code: 1, msg: "audio file has expired. Submit a new request."}
		case "failed":
			msg := "TTS generation failed"
			if resp.Error != nil {
				msg = resp.Error.Message
			}
			return &exitError{code: 1, msg: msg}
		case "processing":
			if resp.RetryAfterSeconds != nil {
				delay = time.Duration(*resp.RetryAfterSeconds) * time.Second
			} else {
				delay = minDuration(delay*3/2, 15*time.Second)
			}
			elapsed := time.Since(deadline.Add(-timeout))
			spin.Update(renderProgress(resp, elapsed))
		default:
			return &exitError{code: 1, msg: fmt.Sprintf("unexpected job status %q from API. Job ID: %s", resp.Status, jobID)}
		}
	}
}

func handleCompleted(ctx context.Context, client *api.Client, resp *api.TTSResponse, resolved *config.ResolvedConfig) error {
	// Validate audio_url BEFORE emitting JSON — ensures --json never returns
	// exit 0 with an unusable payload. Execute() handles JSON error output.
	if err := validateCompletedAudioURL(resp, resolved); err != nil {
		return err
	}

	// --json mode: emit raw API response
	if flagJSON {
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

	// Determine voice for filename
	voice := resolved.Voice
	if resp.Audio != nil && resp.Audio.Voice != "" {
		voice = resp.Audio.Voice
	}

	// -o - : raw MP3 to stdout
	if speakOutput == "-" {
		if err := downloadToStdout(ctx, resp.AudioURL, resolved.APIURL); err != nil {
			return &exitError{code: 1, msg: err.Error(), err: &downloadFailure{err: err, audioURL: resp.AudioURL}}
		}
		return nil
	}

	// Determine output path
	var destPath string
	if speakOutput != "" {
		destPath = speakOutput
	} else {
		destPath = api.AutoFilename(voice, resolved.OutputDir)

		// Verify auto-generated path stays within the output directory (defense in depth)
		absPath, _ := filepath.Abs(destPath)
		absDir, _ := filepath.Abs(resolved.OutputDir)
		if !strings.HasPrefix(absPath, absDir+string(filepath.Separator)) && absPath != absDir {
			return &exitError{code: 2, msg: fmt.Sprintf("output path %s escapes output directory %s", destPath, resolved.OutputDir)}
		}
	}

	// Verify output directory exists
	dir := filepath.Dir(destPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return &exitError{code: 2, msg: fmt.Sprintf("output directory does not exist: %s", dir)}
	}

	dlSpin := display.New()
	if !flagJSON && !flagQuiet {
		dlSpin.Start("Downloading audio...")
	}

	downloadedBytes, err := client.DownloadAudioWithSize(ctx, resp.AudioURL, destPath)
	if err != nil {
		dlSpin.Stop()
		return &exitError{code: 1, msg: "download failed", err: &downloadFailure{err: err, audioURL: resp.AudioURL}}
	}

	dlSpin.Stop()
	stderrMsg("Saved to %s\n", destPath)
	renderTranslationMeta(resp)
	renderCompletionSummary(resp, downloadedBytes)

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
	freshResp, status, freshErr := api.WithRetry(ctx, api.DefaultRetryConfig(), func(key string) (*api.TTSResponse, int, error) {
		return client.Speak(ctx, req, key)
	}, api.GenerateNew())
	if freshErr != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\nInterrupted.")
			os.Exit(130)
		}
		return handleAPIError(freshErr, status)
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
	fmt.Fprintf(os.Stderr, "Download failed: %v\nAudio URL: %s\n", dl.err, dl.audioURL)
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
			fmt.Fprintln(os.Stderr, "\nInterrupted.")
			os.Exit(130)
		}
		return &exitError{code: 1, msg: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()

	_, err = api.CopyBounded(os.Stdout, resp.Body, 500*1024*1024)
	if err != nil && ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "\nInterrupted.")
		os.Exit(130)
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
	code int
	msg  string
	err  error
}

func (e *exitError) Error() string { return e.msg }

func (e *exitError) Unwrap() error { return e.err }

func handleAPIError(err error, status int) error {
	var apiErr *api.APIResponseError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		switch code {
		case api.ErrInvalidKey:
			return &exitError{code: 1, msg: "invalid API key. Run: ttsbuddy config set key <your-key>"}
		case api.ErrInactiveSubscription:
			return &exitError{code: 1, msg: "subscription inactive. Reactivate at https://ttsbuddy.com/billing"}
		case api.ErrNoAPIAccess:
			return &exitError{code: 1, msg: "your plan does not include API access. Check your plan or contact support."}
		case api.ErrUsageLimitExceeded:
			msg := "monthly TTS minutes exhausted."
			if apiErr.Response.Error != nil && apiErr.Response.Error.Details != nil {
				if details, ok := apiErr.Response.Error.Details.(map[string]interface{}); ok {
					if upgradeURL, ok := details["upgrade_url"].(string); ok {
						msg += fmt.Sprintf(" Upgrade at %s", upgradeURL)
					}
				}
			}
			return &exitError{code: 1, msg: msg}
		case api.ErrTextTooLong:
			return &exitError{code: 2, msg: "input exceeds 500,000 characters. Split into smaller chunks."}
		case api.ErrRateLimited:
			return &exitError{code: 1, msg: "rate limited. Please wait and try again."}
		case api.ErrForbidden:
			return &exitError{code: 1, msg: "access denied (HTTP 403). Check your subscription and API access at https://ttsbuddy.com/billing"}
		default:
			return &exitError{code: 1, msg: apiErr.Error()}
		}
	}
	return &exitError{code: 1, msg: fmt.Sprintf("API request failed: %v", err)}
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
