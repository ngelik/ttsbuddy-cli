//go:build darwin || linux

package config

import (
	"errors"
	"testing"
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
