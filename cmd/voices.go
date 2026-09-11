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

var (
	voicesAll         bool
	voicesLanguage    string
	voicesEngine      string
	voicesRecommended bool
)

var voicesCmd = &cobra.Command{
	Use:   "voices",
	Short: "List available TTS voices",
	Long: `List available TTS voices.

By default, shows a curated offline-friendly list of voices.
Use --all to fetch the full live catalog from the upstream TTS API.

Use --language and --engine to filter the catalog. Use --recommended to
return one deterministic voice within those filters.`,
	Args: noArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		var voices []api.Voice

		if voicesAll {
			// Resolve TTS API base URL: env > config file > default.
			// Config loading is best-effort — broken HOME won't block voices.
			client := api.NewClient("", "", Version)
			ttsBaseURL := os.Getenv("TTSBUDDY_TTS_API_BASE_URL")
			if ttsBaseURL == "" {
				if cfg, err := config.Load(); err == nil && cfg.TTSAPIBaseURL != "" {
					ttsBaseURL = cfg.TTSAPIBaseURL
				} else {
					ttsBaseURL = config.DefaultTTSAPIBaseURL
				}
			}

			stderrMsg("Fetching voice catalog...\n")
			live, err := client.FetchVoices(context.Background(), ttsBaseURL)
			if err != nil {
				if !flagQuiet {
					_, _ = fmt.Fprintln(os.Stderr, "Live voice catalog unavailable, showing curated list (catalog may be stale)")
				}
				voices = api.CuratedVoices()
			} else {
				voices = live
			}
		} else {
			voices = api.CuratedVoices()
		}

		voices = api.FilterVoices(voices, voicesLanguage, voicesEngine)
		api.SortVoices(voices)
		if voicesRecommended {
			if recommended, ok := api.RecommendedVoice(voices); ok {
				voices = []api.Voice{recommended}
			}
		}
		if len(voices) == 0 && (voicesLanguage != "" || voicesEngine != "") {
			stderrMsg("No voices matched the requested filters\n")
		}

		if flagJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(voices)
		}

		// Table output
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tNAME\tGENDER\tLANGUAGE\tCODE\tTYPE")
		_, _ = fmt.Fprintln(w, "──\t────\t──────\t────────\t────\t────")

		for _, v := range voices {
			name := v.Name
			if name == "" {
				name = "-"
			}
			quality := v.Quality
			if quality == "" {
				quality = "-"
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", v.ID, name, v.Gender, v.Language, v.LanguageCode, quality)
		}
		return w.Flush()
	},
}

func init() {
	voicesCmd.Flags().BoolVar(&voicesAll, "all", false, "fetch full live voice catalog")
	voicesCmd.Flags().StringVar(&voicesLanguage, "language", "", "filter by language code")
	voicesCmd.Flags().StringVar(&voicesEngine, "engine", "", "filter by TTS engine")
	voicesCmd.Flags().BoolVar(&voicesRecommended, "recommended", false, "return one deterministic recommended voice")
	rootCmd.AddCommand(voicesCmd)
}
