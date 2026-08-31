//go:build darwin || linux

package config

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigMutationsSerializeAcrossProcesses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := Save(&Config{DefaultVoice: "base"}); err != nil {
		t.Fatal(err)
	}

	releasePath := filepath.Join(home, "release-config-mutation")
	passCmd, passLines, passStderr := startConfigMutationHelper(t, home, releasePath, "pass")
	if !waitForConfigMutationLine(passLines, "entered", 2*time.Second) {
		_ = passCmd.Process.Kill()
		t.Fatalf("pass helper did not enter mutation; stderr: %s", passStderr.String())
	}

	voiceCmd, voiceLines, voiceStderr := startConfigMutationHelper(t, home, releasePath, "voice")
	_ = waitForConfigMutationLine(voiceLines, "entered", 200*time.Millisecond)

	if err := os.WriteFile(releasePath, []byte("go"), 0600); err != nil {
		_ = passCmd.Process.Kill()
		_ = voiceCmd.Process.Kill()
		t.Fatal(err)
	}
	waitConfigMutationHelper(t, passCmd, passStderr)
	waitConfigMutationHelper(t, voiceCmd, voiceStderr)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccessPass == nil || cfg.AccessPass.Credential != fixtureAccessPass('p', 'a').Credential || cfg.DefaultVoice != "parallel-voice" {
		t.Fatalf("cross-process config mutation lost an update: voice=%q pass=%#v", cfg.DefaultVoice, cfg.AccessPass)
	}
}

func startConfigMutationHelper(t *testing.T, home, releasePath, mode string) (*exec.Cmd, <-chan string, *bytes.Buffer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConfigMutationLockSubprocessHelper$")
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"TTSBUDDY_CONFIG_LOCK_HELPER=1",
		"TTSBUDDY_CONFIG_LOCK_HELPER_MODE="+mode,
		"TTSBUDDY_CONFIG_LOCK_RELEASE="+releasePath,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	lines := make(chan string, 8)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- strings.TrimSpace(scanner.Text())
		}
	}()
	return cmd, lines, stderr
}

func waitForConfigMutationLine(lines <-chan string, want string, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return false
			}
			if line == want {
				return true
			}
		case <-timer.C:
			return false
		}
	}
}

func waitConfigMutationHelper(t *testing.T, cmd *exec.Cmd, stderr *bytes.Buffer) {
	t.Helper()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("config mutation helper failed: %v; stderr: %s", err, stderr.String())
	}
}

func TestConfigMutationLockSubprocessHelper(t *testing.T) {
	if os.Getenv("TTSBUDDY_CONFIG_LOCK_HELPER") != "1" {
		return
	}

	mode := os.Getenv("TTSBUDDY_CONFIG_LOCK_HELPER_MODE")
	releasePath := os.Getenv("TTSBUDDY_CONFIG_LOCK_RELEASE")
	if releasePath == "" {
		t.Fatal("missing release path")
	}

	err := mutateAndSaveConfig(func(cfg *Config) (bool, error) {
		fmt.Println("entered")
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(releasePath); err == nil {
				switch mode {
				case "pass":
					pass := fixtureAccessPass('p', 'a')
					cfg.AccessPass = &pass
				case "voice":
					cfg.DefaultVoice = "parallel-voice"
				default:
					return false, fmt.Errorf("unknown mode: %s", mode)
				}
				return true, nil
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false, fmt.Errorf("timed out waiting for release file")
	})
	if err != nil {
		t.Fatal(err)
	}
}
