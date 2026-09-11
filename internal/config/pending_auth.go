package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	PendingAuthVersion = 1
	PendingAuthTTL     = 10 * time.Minute
	maxPendingAuthSize = 16 * 1024
	pendingAuthFile    = "pending_auth.json"
)

var (
	pendingAuthIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
	pendingAuthIDFields  = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,128}$`)
)

// PendingAuth is the local continuation required by the two-step email flow.
// NativeClientToken is deliberately private to this 0600 state file and must
// never be included in a public command result.
type PendingAuth struct {
	Version           int       `json:"version"`
	ChallengeID       string    `json:"challenge_id"`
	Mode              string    `json:"mode"`
	CreatedAt         time.Time `json:"created_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	ClerkOrigin       string    `json:"clerk_origin"`
	APIOrigin         string    `json:"api_origin"`
	SignInID          string    `json:"sign_in_id,omitempty"`
	EmailAddressID    string    `json:"email_address_id,omitempty"`
	SignUpID          string    `json:"sign_up_id,omitempty"`
	NativeClientToken string    `json:"native_client_token"`
}

// NewPendingAuthID returns an opaque, non-secret local identifier.
func NewPendingAuthID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate pending auth id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// IsPendingAuthID validates the opaque identifier accepted by continuation
// commands without exposing any private state.
func IsPendingAuthID(value string) bool { return pendingAuthIDPattern.MatchString(value) }

func pendingAuthPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", fmt.Errorf("config directory: %w", err)
	}
	return filepath.Join(dir, pendingAuthFile), nil
}

func validateOrigin(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("pending auth origin is invalid")
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return errors.New("pending auth origin is invalid")
	}
	return nil
}

func validatePendingAuth(state PendingAuth, now time.Time) error {
	if state.Version != PendingAuthVersion || !pendingAuthIDPattern.MatchString(state.ChallengeID) {
		return errors.New("pending auth state is invalid")
	}
	if state.Mode != "login" && state.Mode != "signup" {
		return errors.New("pending auth state is invalid")
	}
	if state.CreatedAt.IsZero() || state.ExpiresAt.IsZero() || !state.ExpiresAt.After(state.CreatedAt) {
		return errors.New("pending auth state is invalid")
	}
	if state.ExpiresAt.Sub(state.CreatedAt) > PendingAuthTTL {
		return errors.New("pending auth state is invalid")
	}
	if state.CreatedAt.After(now.Add(time.Minute)) {
		return errors.New("pending auth state is invalid")
	}
	if err := validateOrigin(state.ClerkOrigin); err != nil {
		return err
	}
	if err := validateOrigin(state.APIOrigin); err != nil {
		return err
	}
	if len(state.NativeClientToken) < 8 || len(state.NativeClientToken) > 2048 || strings.IndexFunc(state.NativeClientToken, func(r rune) bool { return r <= ' ' }) >= 0 {
		return errors.New("pending auth state is invalid")
	}
	if state.Mode == "login" {
		if !pendingAuthIDFields.MatchString(state.SignInID) || !pendingAuthIDFields.MatchString(state.EmailAddressID) || state.SignUpID != "" {
			return errors.New("pending auth state is invalid")
		}
	} else if !pendingAuthIDFields.MatchString(state.SignUpID) || state.SignInID != "" || state.EmailAddressID != "" {
		return errors.New("pending auth state is invalid")
	}
	return nil
}

func checkPendingAuthFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("pending auth state must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return errors.New("pending auth state is not a regular file")
	}
	if info.Size() > maxPendingAuthSize {
		return errors.New("pending auth state is too large")
	}
	if info.Mode().Perm() != 0600 {
		return errors.New("pending auth state has unsafe permissions")
	}
	return nil
}

func checkPendingAuthDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("pending auth config directory is not a directory")
	}
	if info.Mode().Perm() != 0700 {
		return errors.New("pending auth config directory has unsafe permissions")
	}
	return nil
}

// SavePendingAuth writes one continuation record with secure permissions.
func SavePendingAuth(state PendingAuth) error {
	if err := validatePendingAuth(state, time.Now().UTC()); err != nil {
		return &ValidationError{Msg: err.Error()}
	}
	if !state.ExpiresAt.After(time.Now().UTC()) {
		return &ValidationError{Msg: "pending auth state is expired"}
	}
	dir, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := checkPendingAuthDir(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, pendingAuthFile)
	if err := checkPendingAuthFile(path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot marshal pending auth: %w", err)
	}
	data = append(data, '\n')
	return atomicWriteFile(path, data, 0600)
}

// LoadPendingAuth returns nil for an absent continuation and validates all
// persisted state before exposing it to the command layer.
func LoadPendingAuth() (*PendingAuth, error) {
	path, err := pendingAuthPath()
	if err != nil {
		return nil, err
	}
	if err := checkPendingAuthDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := checkPendingAuthFile(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading pending auth: %w", err)
	}
	if len(data) > maxPendingAuthSize {
		return nil, errors.New("pending auth state is too large")
	}
	var state PendingAuth
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, errors.New("pending auth state is malformed")
	}
	if err := validatePendingAuth(state, time.Now().UTC()); err != nil {
		return nil, err
	}
	return &state, nil
}

// ClearPendingAuth removes the exact continuation ID. An empty expected ID is
// not accepted so callers cannot accidentally clear a replacement challenge.
func ClearPendingAuth(expectedID string) error {
	if !pendingAuthIDPattern.MatchString(expectedID) {
		return &ValidationError{Msg: "invalid pending auth challenge ID"}
	}
	path, err := pendingAuthPath()
	if err != nil {
		return err
	}
	if err := checkPendingAuthDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err := checkPendingAuthFile(path); err != nil {
		return err
	}
	state, err := LoadPendingAuth()
	if err != nil {
		return err
	}
	if state == nil {
		return nil
	}
	if state.ChallengeID != expectedID {
		return errors.New("pending auth challenge changed concurrently")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove pending auth: %w", err)
	}
	return nil
}
