package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

func resolveBuildMetadata(
	version, commit, date string,
	info *debug.BuildInfo,
	ok bool,
) (string, string, string) {
	if !ok || info == nil {
		return version, commit, date
	}

	if version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = info.Main.Version
	}

	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if commit == "none" && setting.Value != "" {
				commit = setting.Value
				if len(commit) > 7 {
					commit = commit[:7]
				}
			}
		case "vcs.time":
			if date == "unknown" && setting.Value != "" {
				date = setting.Value
			}
		}
	}

	return version, commit, date
}

func versionString() string {
	return fmt.Sprintf("ttsbuddy %s (%s, %s), %s", Version, Commit, Date, runtime.Version())
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Args:  noArgs,
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
