package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
)

func TestConfigDirectoryPermissionDiagnostics(t *testing.T) {
	for _, source := range []string{"env", "flag", "default"} {
		t.Run(source, func(t *testing.T) {
			home := t.TempDir()
			if err := os.Chmod(home, 0700); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(home, "config space ' $(touch sentinel)")
			if source == "default" {
				dir = filepath.Join(home, ".ttsbuddy")
			}
			if err := os.Mkdir(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0755); err != nil {
				t.Fatal(err)
			}
			env := []string{"HOME=" + home}
			flags := []string{}
			if source == "env" {
				env = append(env, "TTSBUDDY_CONFIG_DIR="+dir)
			}
			if source == "flag" {
				env = append(env, "TTSBUDDY_CONFIG_DIR="+filepath.Join(home, "ignored"))
				flags = append(flags, "--config-dir", dir)
			}
			args := append([]string{"doctor", "--json"}, flags...)
			result := runCLI(t, env, args...)
			report := decodeDoctorReport(t, result.Stdout)
			if result.ExitCode != 1 || report.Ready {
				t.Fatalf("unexpected result: %#v", result)
			}
			var found bool
			for _, check := range report.Checks {
				if check.Name != "config_directory" {
					continue
				}
				found = true
				if check.Status != "failed" || check.Reason != configDirPermissionsReason ||
					check.Details["path"] != dir || check.Details["actual_mode"] != "0755" ||
					check.Details["required_mode"] != "0700" || check.Action == nil ||
					check.Action.Type != "local_permission_fix" ||
					!reflect.DeepEqual(check.Action.Argv, []string{"chmod", "700", dir}) {
					t.Fatalf("bad check: %#v", check)
				}
			}
			if !found {
				t.Fatal("missing config directory check")
			}
			text := runCLI(t, env, append([]string{"doctor"}, flags...)...)
			for _, want := range []string{"FAIL config_directory", "Permissions: 0755", "Required: 0700", "chmod 700", "ttsbuddy doctor --json"} {
				if !strings.Contains(text.Stdout, want) {
					t.Fatalf("missing %q: %s", want, text.Stdout)
				}
			}
			auth := runCLI(t, env, append([]string{"auth", "email", "start", "--email", "test@example.com", "--json"}, flags...)...)
			var payload api.CLIError
			if err := json.Unmarshal([]byte(auth.Stdout), &payload); err != nil {
				t.Fatal(err)
			}
			if auth.ExitCode != 1 || payload.Error.Reason != configDirPermissionsReason || payload.Error.Details["path"] != dir || payload.Error.Action == nil || !reflect.DeepEqual(payload.Error.Action.Argv, []string{"chmod", "700", dir}) {
				t.Fatalf("bad auth: %#v", auth)
			}
			if strings.Contains(strings.ToLower(auth.Stdout), "cancel") {
				t.Fatalf("spurious cancellation advice: %s", auth.Stdout)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("commands mutated directory: %v %v", entries, err)
			}
			info, err := os.Stat(dir)
			if err != nil || info.Mode().Perm() != 0755 {
				t.Fatalf("permissions changed: %v %v", info, err)
			}
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			result = runCLI(t, env, args...)
			report = decodeDoctorReport(t, result.Stdout)
			for _, check := range report.Checks {
				if check.Name == "config_directory" && check.Status != "healthy" {
					t.Fatalf("0700 rejected: %#v", check)
				}
			}
			if !strings.Contains(strings.Join(report.NextActions, " "), "auth email") {
				t.Fatal("ordinary auth diagnostics missing")
			}
		})
	}
}

func TestConfigPermissionFixQuotesPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "space ' $HOME `echo bad` $(echo bad)")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	command := configPermissionFix(&config.ConfigDirPermissionsError{Path: dir, ActualMode: 0755})
	if out, err := exec.Command("sh", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("fix: %s: %v", out, err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("fix did not target exact directory: %v %v", info, err)
	}
}
