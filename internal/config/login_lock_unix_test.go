//go:build darwin || linux

package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestLoginLockIsNonblockingAndReleases(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first, err := AcquireLoginLock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLoginLock(); !errors.Is(err, ErrLoginInProgress) {
		t.Fatalf("second lock error = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireLoginLock()
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestLoginLockReleasedWhenHolderTerminates(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLoginLockSubprocessHelper$")
	cmd.Env = append(os.Environ(), "HOME="+home, "TTSBUDDY_LOGIN_LOCK_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "ready" {
		_ = cmd.Process.Kill()
		t.Fatalf("holder did not become ready: %v", scanner.Err())
	}
	t.Setenv("HOME", home)
	if _, err := AcquireLoginLock(); !errors.Is(err, ErrLoginInProgress) {
		_ = cmd.Process.Kill()
		t.Fatalf("overlapping process acquired lock: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	lock, err := AcquireLoginLock()
	if err != nil {
		t.Fatalf("terminated holder left stale lock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestLoginLockSubprocessHelper(t *testing.T) {
	if os.Getenv("TTSBUDDY_LOGIN_LOCK_HELPER") != "1" {
		return
	}
	lock, err := AcquireLoginLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	fmt.Println("ready")
	for {
		time.Sleep(time.Hour)
	}
}
