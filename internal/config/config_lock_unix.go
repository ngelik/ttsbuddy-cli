//go:build darwin || linux

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const (
	configMutationLockFile    = "config-mutation.lock"
	configMutationLockTimeout = 30 * time.Second
	configMutationLockPoll    = 25 * time.Millisecond
)

type configMutationLock struct{ file *os.File }

func acquireConfigMutationLock() (*configMutationLock, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, configMutationLockFile), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open config mutation lock: %w", err)
	}
	deadline := time.Now().Add(configMutationLockTimeout)
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &configMutationLock{file: file}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire config mutation lock: %w", err)
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("timed out waiting for config mutation lock")
		}
		time.Sleep(configMutationLockPoll)
	}
}

func (l *configMutationLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return fmt.Errorf("release config mutation lock: %w", err)
	}
	return closeErr
}
