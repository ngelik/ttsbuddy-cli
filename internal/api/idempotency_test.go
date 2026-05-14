package api

import "testing"

func TestGenerateFromContentDeterministic(t *testing.T) {
	k1 := GenerateFromContent("hello world", "af_heart", 1.0)
	k2 := GenerateFromContent("hello world", "af_heart", 1.0)
	if k1 != k2 {
		t.Errorf("same input should produce same key: %q != %q", k1, k2)
	}
}

func TestGenerateFromContentDiffersOnVoice(t *testing.T) {
	k1 := GenerateFromContent("hello world", "af_heart", 1.0)
	k2 := GenerateFromContent("hello world", "bf_emma", 1.0)
	if k1 == k2 {
		t.Error("different voice should produce different key")
	}
}

func TestGenerateFromContentDiffersOnSpeed(t *testing.T) {
	k1 := GenerateFromContent("hello", "af_heart", 1.004)
	k2 := GenerateFromContent("hello", "af_heart", 1.005)
	if k1 == k2 {
		t.Error("different speed (1.004 vs 1.005) should produce different keys")
	}
}

func TestGenerateFromContentDiffersOnLanguage(t *testing.T) {
	k1 := GenerateFromContent("hello", "st_m1", 1.0, "fr")
	k2 := GenerateFromContent("hello", "st_m1", 1.0, "de")
	if k1 == k2 {
		t.Error("different language should produce different keys")
	}
}

func TestGenerateFromStdinUnique(t *testing.T) {
	k1 := GenerateFromStdin()
	k2 := GenerateFromStdin()
	if k1 == k2 {
		t.Error("stdin keys should be unique (UUIDs)")
	}
}

func TestGenerateNewUnique(t *testing.T) {
	k1 := GenerateNew()
	k2 := GenerateNew()
	if k1 == k2 {
		t.Error("new keys should be unique (UUIDs)")
	}
}
