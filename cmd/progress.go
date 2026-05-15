package cmd

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
)

func renderProgress(resp *api.TTSResponse, elapsed time.Duration) string {
	if resp != nil && resp.Progress != nil {
		label := resp.Progress.Message
		if label == "" {
			switch resp.Progress.Phase {
			case "queued":
				label = "Queued"
			case "finalizing":
				label = "Finalizing audio"
			default:
				label = "Processing audio"
			}
		}
		if resp.Progress.Percent != nil {
			return fmt.Sprintf("Processing %d%%... (%s)", *resp.Progress.Percent, elapsed.Round(time.Second))
		}
		if strings.EqualFold(resp.Progress.Phase, "queued") || strings.EqualFold(label, "Queued") {
			return fmt.Sprintf("Queued... (%s)", elapsed.Round(time.Second))
		}
		return fmt.Sprintf("%s... (%s)", label, elapsed.Round(time.Second))
	}
	return fmt.Sprintf("Processing... (%s)", elapsed.Round(time.Second))
}

func renderTranslationMeta(resp *api.TTSResponse) {
	if flagJSON || flagQuiet || resp == nil || resp.Meta == nil || resp.Meta.Source != "webpage" {
		return
	}
	if resp.Meta.Translated != nil && *resp.Meta.Translated {
		source := resp.Meta.SourceLanguage
		if source == "" {
			source = "source"
		}
		target := resp.Meta.TargetLanguage
		if target == "" {
			target = "target"
		}
		stderrMsg("Translated %s -> %s\n", source, target)
		return
	}
	if resp.Meta.Translated != nil && !*resp.Meta.Translated {
		stderrMsg("No translation needed")
		if resp.Meta.TargetLanguage != "" {
			stderrMsg(" (%s)", resp.Meta.TargetLanguage)
		}
		stderrMsg("\n")
	}
}

func renderCompletionSummary(resp *api.TTSResponse, downloadedBytes int64) {
	if flagJSON || flagQuiet || resp == nil {
		return
	}
	if resp.JobID != "" {
		stderrMsg("Job ID: %s\n", resp.JobID)
	}
	if seconds, ok := speechLengthSeconds(resp); ok {
		stderrMsg("Speech length: %s\n", formatDuration(seconds))
	}
	if size, ok := mp3SizeBytes(resp, downloadedBytes); ok {
		stderrMsg("MP3 size: %s\n", formatBytes(size))
	}
	if cps, ok := generationCharsPerSecond(resp); ok {
		stderrMsg("Generation speed: %.0f chars/sec\n", cps)
	}
}

func speechLengthSeconds(resp *api.TTSResponse) (float64, bool) {
	if resp.Stats != nil && resp.Stats.SpeechLengthSeconds != nil {
		return *resp.Stats.SpeechLengthSeconds, true
	}
	if resp.Audio != nil && resp.Audio.DurationSeconds != nil {
		return *resp.Audio.DurationSeconds, true
	}
	return 0, false
}

func mp3SizeBytes(resp *api.TTSResponse, downloadedBytes int64) (int64, bool) {
	if downloadedBytes > 0 {
		return downloadedBytes, true
	}
	if resp.Stats != nil && resp.Stats.FileSizeBytes != nil && *resp.Stats.FileSizeBytes > 0 {
		return *resp.Stats.FileSizeBytes, true
	}
	if resp.Audio != nil && resp.Audio.FileSizeBytes != nil && *resp.Audio.FileSizeBytes > 0 {
		return *resp.Audio.FileSizeBytes, true
	}
	return 0, false
}

func generationCharsPerSecond(resp *api.TTSResponse) (float64, bool) {
	if resp.Stats != nil && resp.Stats.GenerationCharsPerSecond != nil &&
		*resp.Stats.GenerationCharsPerSecond > 0 {
		return *resp.Stats.GenerationCharsPerSecond, true
	}
	return 0, false
}

func formatDuration(seconds float64) string {
	total := int(math.Round(seconds))
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	minutes := total / 60
	remaining := total % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm%ds", minutes, remaining)
	}
	hours := minutes / 60
	minutes = minutes % 60
	return fmt.Sprintf("%dh%dm%ds", hours, minutes, remaining)
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	for _, suffix := range []string{"KB", "MB", "GB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f TB", value/unit)
}
