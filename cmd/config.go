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
		cfg, err := config.Load()
		if err != nil {
			return &exitError{code: 1, msg: fmt.Sprintf("loading config: %v", err)}
		}

		if flagJSON {
			resolved := config.Resolve(cfg, config.FlagValues{})
			resolved.APIKey = config.RedactKey(resolved.APIKey)
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(resolved)
		}

		keys := []string{"key", "voice", "speed", "timeout", "output_dir", "api_url", "tts_api_base_url"}
		for _, k := range keys {
			val, _ := config.Get(cfg, k)
			if val == "" {
				val = "(not set)"
			}
			fmt.Fprintf(os.Stdout, "%-20s %s\n", k+":", val)
		}
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
		cfg, err := config.Load()
		if err != nil {
			return &exitError{code: 1, msg: fmt.Sprintf("loading config: %v", err)}
		}
		val, _ := config.Get(cfg, key)
		if val == "" {
			val = "(not set)"
		}
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
			return &exitError{code: 1, msg: fmt.Sprintf("setting config: %v", err)}
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

func init() {
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configSetCmd)
	rootCmd.AddCommand(configCmd)
}
