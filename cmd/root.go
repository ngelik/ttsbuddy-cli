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
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}

		flags := config.FlagValues{}
		if cmd.Flags().Changed("key") {
			flags.APIKey = &flagAPIKey
		}

		resolvedCfg = config.Resolve(cfg, flags)
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&flagAPIKey, "key", "k", "", "API key (overrides config/env)")
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "JSON output to stdout only")
	rootCmd.PersistentFlags().BoolVar(&flagQuiet, "quiet", false, "suppress progress output")

	rootCmd.SetVersionTemplate(versionString() + "\n")
	rootCmd.Version = Version
}

// Execute runs the root command.
func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		if flagJSON {
			// Emit CLI JSON error for --json mode
			exitCode := 1
			if e, ok := err.(*exitError); ok {
				exitCode = e.code
			}
			cliErr := api.NewCLIError("CLI_ERROR", err.Error())
			data, _ := json.MarshalIndent(cliErr, "", "  ")
			fmt.Fprintln(os.Stdout, string(data))
			if exitCode == 2 {
				os.Exit(2)
			}
		} else {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		return err
	}
	return nil
}
