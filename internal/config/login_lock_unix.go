//go:build darwin || linux

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var ErrLoginInProgress = errors.New("another auth login is already running")

type LoginLock struct{ file *os.File }

func AcquireLoginLock() (*LoginLock, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, "auth-login.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open login lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLoginInProgress
		}
		return nil, fmt.Errorf("acquire login lock: %w", err)
	}
	return &LoginLock{file: file}, nil
}

func (l *LoginLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return fmt.Errorf("release login lock: %w", err)
	}
	return closeErr
}
