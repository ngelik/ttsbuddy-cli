package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"unicode/utf8"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
	"github.com/ngelik/ttsbuddy-cli/internal/display"
	"github.com/ngelik/ttsbuddy-cli/internal/webpage"
	"github.com/spf13/cobra"
)

var webCmd = &cobra.Command{
	Use:   "web <url>",
	Short: "Convert a webpage to speech",
	Long: `Fetch a webpage, extract readable article text, and convert it to speech.

When --voice, --language, or --speed are omitted, the backend applies your
TTSBuddy account preferences before generating audio.`,
	Args:         exactArgs(1),
	SilenceUsage: true,
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if flagJSON && speakOutput == "-" {
			return &exitError{code: 2, msg: "--json and -o - are mutually exclusive (both write to stdout)"}
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWeb(cmd, args[0])
	},
}

func init() {
	webCmd.Flags().StringVarP(&speakVoice, "voice", "v", "", "voice ID (backend preference when omitted)")
	webCmd.Flags().StringVarP(&speakLanguage, "language", "l", "", "target language code (backend preference when omitted)")
	webCmd.Flags().Float64VarP(&speakSpeed, "speed", "s", 0, "speed 0.5-1.5 (backend preference when omitted)")
	webCmd.Flags().StringVarP(&speakOutput, "output", "o", "", "output file (- for stdout)")
	webCmd.Flags().StringVar(&speakOutputDir, "output-dir", "", "directory for auto-named files")
	webCmd.Flags().StringVar(&speakTimeout, "timeout", "", "poll timeout (e.g. 30s, 2m, 10m)")
	webCmd.Flags().BoolVar(&speakNoDownload, "no-download", false, "print audio URL instead of downloading")
	webCmd.Flags().StringVar(&speakIdempotencyKey, "idempotency-key", "", "override auto-generated idempotency key")

	rootCmd.AddCommand(webCmd)
}

func runWeb(cmd *cobra.Command, rawURL string) error {
	resolved := resolvedCfg
	if resolved == nil {
		return &exitError{code: 1, msg: "config not loaded"}
	}
	if resolved.APIKey == "" {
		return &exitError{code: 2, msg: "no API key configured. Run: ttsbuddy config set key <your-key>"}
	}

	if cmd.Flags().Changed("output-dir") {
		resolved.OutputDir = speakOutputDir
	}
	if cmd.Flags().Changed("timeout") {
		resolved.PollTimeout = speakTimeout
	}

	voiceExplicit := cmd.Flags().Changed("voice")
	languageExplicit := cmd.Flags().Changed("language")
	speedExplicit := cmd.Flags().Changed("speed")

	reqVoice := ""
	reqLanguage := ""
	reqSpeed := 0.0

	if voiceExplicit {
		reqVoice = strings.TrimSpace(speakVoice)
		if reqVoice == "" {
			return &exitError{code: 2, msg: "voice cannot be empty"}
		}
	}
	if languageExplicit {
		reqLanguage = strings.ToLower(strings.TrimSpace(speakLanguage))
		if reqLanguage == "" || !config.IsValidLanguageCode(reqLanguage) {
			return &exitError{code: 2, msg: fmt.Sprintf("invalid language code: %s", speakLanguage)}
		}
	}
	if speedExplicit {
		reqSpeed = speakSpeed
		if reqSpeed < 0.5 || reqSpeed > 1.5 {
			return &exitError{code: 2, msg: fmt.Sprintf("speed must be between 0.5 and 1.5 (got %.2f)", reqSpeed)}
		}
	}
	if voiceExplicit && languageExplicit && !strings.HasPrefix(reqVoice, "st_") {
		return &exitError{code: 2, msg: "--language is only supported with Supertonic st_* voices; choose a Kokoro voice for its native language"}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	spin := display.New()
	if !flagJSON && !flagQuiet {
		spin.Start("Fetching webpage...")
	}
	article, err := webpage.FetchArticle(ctx, rawURL, Version)
	if err != nil {
		spin.Stop()
		return &exitError{code: 2, msg: err.Error()}
	}
	spin.Stop()
	if !flagJSON && !flagQuiet {
		title := strings.TrimSpace(article.Title)
		if title == "" {
			title = article.URL
		}
		stderrMsg("Extracted %q (%d chars)\n", title, utf8.RuneCountInString(article.Text))
	}

	if strings.TrimSpace(article.Text) == "" {
		return &exitError{code: 2, msg: "no readable text found on webpage"}
	}
	if charCount := utf8.RuneCountInString(article.Text); charCount > 500_000 {
		return &exitError{code: 2, msg: fmt.Sprintf("webpage text exceeds 500,000 characters (%d characters)", charCount)}
	}

	idemKey := speakIdempotencyKey
	if idemKey == "" {
		if voiceExplicit && languageExplicit && speedExplicit {
			idemKey = api.GenerateFromContent(article.URL+"\n"+article.Text, reqVoice, reqSpeed, reqLanguage)
		} else {
			idemKey = api.GenerateFromStdin()
		}
	}

	req := api.SpeakRequest{
		Text:        article.Text,
		Voice:       reqVoice,
		Speed:       reqSpeed,
		Language:    reqLanguage,
		Source:      "webpage",
		Webpage:     article.URL,
		SourceTitle: article.Title,
		Translate:   "auto",
	}

	client := api.NewClient(resolved.APIURL, resolved.APIKey, Version)

	submitSpin := display.New()
	if !flagJSON && !flagQuiet {
		submitSpin.Start("Submitting webpage TTS request...")
	}

	resp, status, err := api.WithRetry(ctx, api.DefaultRetryConfig(), func(key string) (*api.TTSResponse, int, error) {
		return client.Speak(ctx, req, key)
	}, idemKey)

	if err != nil {
		submitSpin.Stop()
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\nInterrupted.")
			os.Exit(130)
		}
		return handleAPIError(err, status)
	}
	submitSpin.Stop()

	if resp.JobID != "" {
		if err := config.SaveLastJob(resp.JobID); err != nil {
			stderrMsg("Warning: could not save job ID: %v\n", err)
		}
	}

	switch {
	case resp.Status == "completed":
		return handleCompleted(ctx, client, resp, resolved)
	case resp.Status == "expired":
		return &exitError{code: 1, msg: "audio file has expired and been deleted. Submit a new request."}
	case resp.Status == "failed":
		return ttsFailureError(resp, "TTS generation failed")
	case status == 202 || resp.Status == "processing":
		renderTranslationMeta(resp)
		return pollUntilComplete(ctx, client, resp, resolved)
	default:
		if flagJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(resp)
		}
		return &exitError{code: 1, msg: fmt.Sprintf("unexpected response status: %s", resp.Status)}
	}
}
