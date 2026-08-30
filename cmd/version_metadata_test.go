package cmd

import (
	"runtime/debug"
	"testing"
)

func TestResolveBuildMetadataUsesGoModuleVersion(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.8.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef"},
			{Key: "vcs.time", Value: "2026-08-30T12:34:56Z"},
		},
	}

	version, commit, date := resolveBuildMetadata("dev", "none", "unknown", info, true)

	if version != "v0.8.0" {
		t.Fatalf("version = %q, want %q", version, "v0.8.0")
	}
	if commit != "0123456" {
		t.Fatalf("commit = %q, want %q", commit, "0123456")
	}
	if date != "2026-08-30T12:34:56Z" {
		t.Fatalf("date = %q, want %q", date, "2026-08-30T12:34:56Z")
	}
}

func TestResolveBuildMetadataPreservesLinkerValues(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.8.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef"},
			{Key: "vcs.time", Value: "2026-08-30T12:34:56Z"},
		},
	}

	version, commit, date := resolveBuildMetadata(
		"v1.2.3",
		"abcdef0",
		"2026-08-31T01:02:03Z",
		info,
		true,
	)

	if version != "v1.2.3" || commit != "abcdef0" || date != "2026-08-31T01:02:03Z" {
		t.Fatalf("metadata = (%q, %q, %q), want linker-provided values", version, commit, date)
	}
}

func TestResolveBuildMetadataIgnoresDevelopmentBuildInfo(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}

	version, commit, date := resolveBuildMetadata("dev", "none", "unknown", info, true)

	if version != "dev" || commit != "none" || date != "unknown" {
		t.Fatalf("metadata = (%q, %q, %q), want development defaults", version, commit, date)
	}
}
