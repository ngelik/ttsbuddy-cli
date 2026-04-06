package api

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// AutoFilename generates a deterministic output filename.
// Pattern: ttsbuddy-YYYYMMDD-HHMMSS-<voice>.mp3
// Appends -2, -3, etc. on collision.
func AutoFilename(voice, outputDir string) string {
	now := time.Now()
	base := fmt.Sprintf("ttsbuddy-%s-%s", now.Format("20060102-150405"), voice)

	path := filepath.Join(outputDir, base+".mp3")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}

	for i := 2; i <= 999; i++ {
		path = filepath.Join(outputDir, fmt.Sprintf("%s-%d.mp3", base, i))
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
	}

	// Extremely unlikely: fall back to timestamp with nanoseconds
	return filepath.Join(outputDir, fmt.Sprintf("ttsbuddy-%d-%s.mp3", now.UnixNano(), voice))
}
