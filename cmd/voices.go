package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
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
			// Try live catalog
			client := api.NewClient("", "", Version)
			ttsBaseURL := resolvedCfg.TTSAPIBaseURL

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
		fmt.Fprintln(w, "ID\tGENDER\tLANGUAGE")
		fmt.Fprintln(w, "──\t──────\t────────")

		currentLang := ""
		for _, v := range voices {
			if v.Language != currentLang && v.Language != "" {
				currentLang = v.Language
			}
			name := v.ID
			if v.Name != "" {
				name = v.ID
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", name, v.Gender, v.Language)
		}
		return w.Flush()
	},
}

func init() {
	voicesCmd.Flags().BoolVar(&voicesAll, "all", false, "fetch full live voice catalog")
	rootCmd.AddCommand(voicesCmd)
}
