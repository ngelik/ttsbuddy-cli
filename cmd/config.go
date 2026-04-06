package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ngelik/ttsbuddy-cli/internal/config"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show or set configuration",
	Long: `Show all configuration values, or get/set individual values.

Usage:
  ttsbuddy config                    Show all config values
  ttsbuddy config get <key>          Show a single value
  ttsbuddy config set <key> <value>  Set a value

Valid keys: key, voice, speed, timeout, output_dir, api_url, tts_api_base_url`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Show resolved values (file + env + defaults) for consistency
		// across plain, --json, and config get output.
		resolved := resolvedCfg
		if resolved == nil {
			return &exitError{code: 1, msg: "config not loaded"}
		}

		if flagJSON {
			out := *resolved
			out.APIKey = config.RedactKey(out.APIKey)
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Fprintf(os.Stdout, "%-20s %s\n", "key:", config.RedactKey(resolved.APIKey))
		fmt.Fprintf(os.Stdout, "%-20s %s\n", "voice:", resolved.Voice)
		fmt.Fprintf(os.Stdout, "%-20s %.1f\n", "speed:", resolved.Speed)
		fmt.Fprintf(os.Stdout, "%-20s %s\n", "timeout:", resolved.PollTimeout)
		fmt.Fprintf(os.Stdout, "%-20s %s\n", "output_dir:", resolved.OutputDir)
		fmt.Fprintf(os.Stdout, "%-20s %s\n", "api_url:", resolved.APIURL)
		fmt.Fprintf(os.Stdout, "%-20s %s\n", "tts_api_base_url:", resolved.TTSAPIBaseURL)
		return nil
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Show a config value",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		if !config.IsValidKey(key) {
			return &exitError{code: 2, msg: fmt.Sprintf("unknown config key: %s\nValid keys: key, voice, speed, timeout, output_dir, api_url, tts_api_base_url", key)}
		}
		resolved := resolvedCfg
		if resolved == nil {
			return &exitError{code: 1, msg: "config not loaded"}
		}
		val := getResolvedValue(resolved, key)
		fmt.Println(val)
		return nil
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a config value",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key, value := args[0], args[1]

		if !config.IsValidKey(key) {
			return &exitError{code: 2, msg: fmt.Sprintf("unknown config key: %s\nValid keys: key, voice, speed, timeout, output_dir, api_url, tts_api_base_url", key)}
		}

		if (key == "key" || key == "api_key") && !strings.HasPrefix(value, "ttsb_") {
			return &exitError{code: 2, msg: "API key must start with 'ttsb_'"}
		}

		if err := config.Set(key, value); err != nil {
			code := 1 // runtime/IO error
			if _, ok := err.(*config.ValidationError); ok {
				code = 2 // user input error
			}
			return &exitError{code: code, msg: fmt.Sprintf("setting config: %v", err)}
		}

		switch key {
		case "key", "api_key":
			fmt.Fprintf(os.Stderr, "API key set: %s\n", config.RedactKey(value))
		default:
			fmt.Fprintf(os.Stderr, "%s set: %s\n", key, value)
		}
		return nil
	},
}

func getResolvedValue(r *config.ResolvedConfig, key string) string {
	switch key {
	case "key", "api_key":
		return config.RedactKey(r.APIKey)
	case "voice":
		return r.Voice
	case "speed":
		return fmt.Sprintf("%.1f", r.Speed)
	case "timeout":
		return r.PollTimeout
	case "output_dir":
		return r.OutputDir
	case "api_url":
		return r.APIURL
	case "tts_api_base_url":
		return r.TTSAPIBaseURL
	default:
		return ""
	}
}

func init() {
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configSetCmd)
	rootCmd.AddCommand(configCmd)
}
