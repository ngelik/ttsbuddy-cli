package cmd

import (
	"fmt"
	"os"

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

var rootCmd = &cobra.Command{
	Use:   "ttsbuddy",
	Short: "TTSBuddy CLI — convert text to speech",
	Long:  "A command-line tool for converting text to speech using the TTSBuddy API.",

	SilenceUsage:  true,
	SilenceErrors: true,
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
		if !flagJSON {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		return err
	}
	return nil
}
