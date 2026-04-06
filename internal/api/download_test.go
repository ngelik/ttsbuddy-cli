package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutoFilenameFormat(t *testing.T) {
	dir := t.TempDir()
	path := AutoFilename("af_heart", dir)

	base := filepath.Base(path)
	if !strings.HasPrefix(base, "ttsbuddy-") {
		t.Errorf("should start with ttsbuddy-, got %q", base)
	}
	if !strings.HasSuffix(base, "-af_heart.mp3") {
		t.Errorf("should end with -af_heart.mp3, got %q", base)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("should be in %s, got %s", dir, filepath.Dir(path))
	}
}

func TestAutoFilenameCollision(t *testing.T) {
	dir := t.TempDir()

	// Create first file
	first := AutoFilename("af_heart", dir)
	if err := os.WriteFile(first, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	// Second call should get -2 suffix
	second := AutoFilename("af_heart", dir)
	if second == first {
		t.Error("collision: second file should have different name")
	}
	if !strings.Contains(filepath.Base(second), "-2.mp3") {
		t.Errorf("expected -2 suffix, got %q", filepath.Base(second))
	}
}

func TestSanitizeVoice(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"af_heart", "af_heart"},
		{"st_m1", "st_m1"},
		{"../../../pwn", "pwn"},
		{"x/../../../pwn", "xpwn"},
		{"", "unknown"},
		{"UPPER", "unknown"}, // uppercase stripped
		{"a/b", "ab"},
		{"a b", "ab"},
	}
	for _, tc := range cases {
		got := sanitizeVoice(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeVoice(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestSanitizeVoicePathTraversal(t *testing.T) {
	dir := t.TempDir()
	path := AutoFilename("../../../etc/passwd", dir)

	// Path must be within dir
	abs, _ := filepath.Abs(path)
	absDir, _ := filepath.Abs(dir)
	if !strings.HasPrefix(abs, absDir) {
		t.Errorf("path %s escapes directory %s", abs, absDir)
	}
}
