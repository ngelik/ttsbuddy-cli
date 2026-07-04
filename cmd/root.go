package cmd

import (
	"encoding/json"
	"fmt"
	"os"

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

// Global flag values.
var (
	flagAPIKey string
	flagJSON   bool
	flagQuiet  bool
)

// Resolved config available to all commands after PersistentPreRunE.
var resolvedCfg *config.ResolvedConfig

var rootCmd = &cobra.Command{
	Use:   "ttsbuddy",
	Short: "TTSBuddy CLI — convert text to speech",
	Long:  "A command-line tool for converting text to speech using the TTSBuddy API.",

	SilenceUsage:  true,
	SilenceErrors: true,

	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Commands that work without disk config — skip loading to avoid
		// failing on broken HOME/permissions.
		switch {
		case cmd.Name() == "version", cmd.Name() == "help", cmd.Name() == "voices", isCompletionCommand(cmd):
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
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "speak", "web", "status":
			return true
		}
	}
	return false
}

func init() {
	rootCmd.CompletionOptions.DisableDefaultCmd = true
	rootCmd.SetFlagErrorFunc(helpOnFlagError)
	rootCmd.PersistentFlags().StringVarP(&flagAPIKey, "key", "k", "", "API key (overrides config/env)")
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "JSON output to stdout only")
	rootCmd.PersistentFlags().BoolVar(&flagQuiet, "quiet", false, "suppress progress output")

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
			cliErr := api.NewCLIError("CLI_ERROR", err.Error())
			data, _ := json.MarshalIndent(cliErr, "", "  ")
			_, _ = fmt.Fprintln(os.Stdout, string(data))
		} else if !helpShown {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}

		os.Exit(exitCode)
	}
	return nil
}
