package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
	"github.com/spf13/cobra"
)

var voicesAll bool

var voicesCmd = &cobra.Command{
	Use:   "voices",
	Short: "List available TTS voices",
	Long: `List available TTS voices.

By default, shows a curated offline-friendly list of 24 voices.
Use --all to fetch the full live catalog from the upstream TTS API.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		var voices []api.Voice

		if voicesAll {
			// Try live catalog — resolve env directly (no disk config needed)
			client := api.NewClient("", "", Version)
			ttsBaseURL := os.Getenv("TTSBUDDY_TTS_API_BASE_URL")
			if ttsBaseURL == "" {
				ttsBaseURL = config.DefaultTTSAPIBaseURL
			}

			stderrMsg("Fetching voice catalog...\n")
			live, err := client.FetchVoices(context.Background(), ttsBaseURL)
			if err != nil {
				stderrMsg("Live voice catalog unavailable, showing curated list\n")
				voices = api.CuratedVoices()
			} else {
				voices = live
			}
		} else {
			voices = api.CuratedVoices()
		}

		if flagJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(voices)
		}

		// Table output
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tGENDER\tLANGUAGE")
		fmt.Fprintln(w, "──\t────\t──────\t────────")

		for _, v := range voices {
			name := v.Name
			if name == "" {
				name = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", v.ID, name, v.Gender, v.Language)
		}
		return w.Flush()
	},
}

func init() {
	voicesCmd.Flags().BoolVar(&voicesAll, "all", false, "fetch full live voice catalog")
	rootCmd.AddCommand(voicesCmd)
}
