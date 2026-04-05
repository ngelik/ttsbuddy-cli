package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"
)

func versionString() string {
	return fmt.Sprintf("ttsbuddy-cli %s (%s, %s), %s", Version, Commit, Date, runtime.Version())
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagJSON {
			info := map[string]string{
				"version": Version,
				"commit":  Commit,
				"date":    Date,
				"go":      runtime.Version(),
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(info)
		}
		fmt.Println(versionString())
		return nil
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
