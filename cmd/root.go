package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
	"github.com/spf13/cobra"
)

// Build-time variables set via -ldflags.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

const authMethodSuggestion = "Run either: ttsbuddy auth email or ttsbuddy auth browser"

const missingAPIKeyMessage = "no credential configured. " + authMethodSuggestion + ". For CI or automation, create a permanent key at https://ttsbuddy.com/dashboard and run: ttsbuddy config set key <your-key>"

// Global flag values.
var (
	flagAPIKey           string
	flagConfigDir        string
	flagJSON             bool
	flagQuiet            bool
	flagExecutionContext string
	configDirOverrideErr error
)

// Resolved config available to all commands after PersistentPreRunE.
var resolvedCfg *config.ResolvedConfig

// resolvedExecutionContext is the bounded declaration forwarded on synthesis
// requests. Keep the wire values stable while accepting concise CLI values.
var resolvedExecutionContext = "unknown"

func resolveExecutionContext(flagValue, envValue string, flagSet bool) (string, error) {
	value := strings.TrimSpace(envValue)
	if flagSet {
		value = strings.TrimSpace(flagValue)
	}
	if value == "" {
		return "unknown", nil
	}
	switch strings.ToLower(value) {
	case "unknown":
		return "unknown", nil
	case "human", "human_declared":
		return "human_declared", nil
	case "agent", "agent_declared":
		return "agent_declared", nil
	case "automation", "automation_declared":
		return "automation_declared", nil
	default:
		return "", fmt.Errorf("invalid execution context %q; use unknown, human, agent, or automation", value)
	}
}

var rootCmd = &cobra.Command{
	Use:   "ttsbuddy",
	Short: "TTSBuddy CLI — convert text to speech",
	Long: `A command-line tool for converting text to speech using the TTSBuddy API.

Agent quickstart: https://www.ttsbuddy.com/docs/developers/agent-quickstart
Typical first run: doctor --json, authenticate with auth email start/verify or
auth browser, choose a voice with voices --json, then speak --json or
speak --output audio.mp3 --json. Resume a known job with status <job_id> or
download <job_id>; email verification requires an authorized mailbox owner.`,

	SilenceUsage:  true,
	SilenceErrors: true,

	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		configDirOverrideErr = nil
		if commandUsesTTSSubmission(cmd) {
			resolved, err := resolveExecutionContext(flagExecutionContext, os.Getenv("TTSBUDDY_EXECUTION_CONTEXT"), cmd.Flags().Changed("execution-context"))
			if err != nil {
				return structuredExitError(2, err.Error(), "CLI_ERROR", "INVALID_EXECUTION_CONTEXT", "Set --execution-context or TTSBUDDY_EXECUTION_CONTEXT to unknown, human, agent, or automation.", false, 0)
			}
			resolvedExecutionContext = resolved
		} else {
			resolvedExecutionContext = "unknown"
		}
		if cmd.Flags().Changed("config-dir") && strings.TrimSpace(flagConfigDir) == "" {
			_ = config.SetConfigDirOverride("")
			if cmd.Name() == "doctor" {
				configDirOverrideErr = fmt.Errorf("--config-dir requires an absolute path")
				return nil
			}
			return fmt.Errorf("--config-dir requires an absolute path")
		}
		if err := config.SetConfigDirOverride(flagConfigDir); err != nil {
			if cmd.Name() == "doctor" {
				_ = config.SetConfigDirOverride("")
				configDirOverrideErr = err
				return nil
			}
			return err
		}
		// Commands that work without disk config — skip loading to avoid
		// failing on broken HOME/permissions.
		switch {
		case cmd.Name() == "version", cmd.Name() == "help", cmd.Name() == "voices", cmd.Name() == "doctor", isCompletionCommand(cmd):
			resolvedCfg = nil // clear stale state
			return nil
		}

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}

		flags := config.FlagValues{}
		if cmd.Flags().Changed("key") {
			flags.APIKey = &flagAPIKey
		}

		var warnings []string
		resolvedCfg, warnings = config.Resolve(cfg, flags)

		if !flagJSON {
			for _, w := range warnings {
				_, _ = fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
			}
		}

		// Block untrusted API URL destinations only before commands that
		// can send bearer credentials over the network.
		if resolvedCfg.APIKey != "" && commandUsesCredentialedAPI(cmd) {
			if err := config.CheckCredentialedAPIURL(resolvedCfg.APIURL, resolvedCfg.AllowCustomAPIURL); err != nil {
				return err
			}
		}

		return nil
	},
}

func commandUsesCredentialedAPI(cmd *cobra.Command) bool {
	switch cmd.CommandPath() {
	case "ttsbuddy speak", "ttsbuddy web", "ttsbuddy status", "ttsbuddy download":
		return true
	default:
		return false
	}
}

func commandUsesTTSSubmission(cmd *cobra.Command) bool {
	switch cmd.CommandPath() {
	case "ttsbuddy speak", "ttsbuddy web":
		return true
	default:
		return false
	}
}

func init() {
	info, ok := debug.ReadBuildInfo()
	Version, Commit, Date = resolveBuildMetadata(Version, Commit, Date, info, ok)

	rootCmd.CompletionOptions.DisableDefaultCmd = true
	rootCmd.SetFlagErrorFunc(helpOnFlagError)
	rootCmd.PersistentFlags().StringVarP(&flagAPIKey, "key", "k", "", "API key (overrides config/env)")
	rootCmd.PersistentFlags().StringVar(&flagConfigDir, "config-dir", "", "config/session directory (absolute path; env: TTSBUDDY_CONFIG_DIR)")
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "JSON output to stdout only")
	rootCmd.PersistentFlags().BoolVar(&flagQuiet, "quiet", false, "suppress progress output")
	rootCmd.PersistentFlags().StringVar(&flagExecutionContext, "execution-context", "", "declared execution context for synthesis (unknown, human, agent, automation; env: TTSBUDDY_EXECUTION_CONTEXT)")

	rootCmd.SetVersionTemplate(versionString() + "\n")
	rootCmd.Version = Version
}

// Execute runs the root command and exits with the correct code.
func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		exitCode := 1
		helpShown := false
		switch e := err.(type) {
		case *helpShownError:
			exitCode = e.code
			helpShown = true
		case *exitError:
			exitCode = e.code
		}

		if flagJSON {
			var payload any = api.NewCLIError("CLI_ERROR", err.Error())
			if exitErr, ok := err.(*exitError); ok {
				if exitErr.jsonPayload != nil {
					payload = exitErr.jsonPayload
				} else {
					payload = structuredErrorPayload(exitErr)
				}
			}
			data, _ := json.MarshalIndent(payload, "", "  ")
			_, _ = fmt.Fprintln(os.Stdout, string(data))
		} else if !helpShown {
			fmt.Fprintln(os.Stderr, "Error:", err)
			if exitErr, ok := err.(*exitError); ok && exitErr.idempotencyKey != "" && strings.Contains(exitErr.nextAction, "<same-value>") {
				action := strings.ReplaceAll(exitErr.nextAction, "<same-value>", exitErr.idempotencyKey)
				fmt.Fprintf(os.Stderr, "Recovery: %s\n", action)
			}
		}

		os.Exit(exitCode)
	}
	return nil
}
