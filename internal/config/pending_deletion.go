package config

import (
	"encoding/json"
	"errors"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const PendingDeletionVersion = 1
const pendingDeletionFile = "pending_deletion.json"

// PendingDeletion is private continuation state, separate from speech/login credentials.
// Callers serialize operations with AcquireLoginLock. JWTs are never public receipts.
type PendingDeletion struct {
	Version            int       `json:"version"`
	ChallengeID        string    `json:"challenge_id"`
	Email              string    `json:"email"`
	CreatedAt          time.Time `json:"created_at"`
	ExpiresAt          time.Time `json:"expires_at"`
	ClerkOrigin        string    `json:"clerk_origin"`
	Endpoint           string    `json:"endpoint"`
	SignInID           string    `json:"sign_in_id"`
	EmailAddressID     string    `json:"email_address_id"`
	NativeClientToken  string    `json:"native_client_token,omitempty"`
	SessionID          string    `json:"session_id,omitempty"`
	Proof              string    `json:"proof,omitempty"`
	ProofExpiresAt     time.Time `json:"proof_expires_at,omitempty"`
	OwnerID            string    `json:"owner_id,omitempty"`
	RequestID          string    `json:"request_id,omitempty"`
	Submitted          bool      `json:"submitted"`
	State              string    `json:"state"`
	CorrelationID      string    `json:"correlation_id,omitempty"`
	AppleAuthorization string    `json:"apple_authorization,omitempty"`
	CompletedAt        time.Time `json:"completed_at,omitempty"`
}

var deletionUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validatePendingDeletion(s PendingDeletion) error {
	invalid := errors.New("pending deletion state is invalid")
	if s.Version != PendingDeletionVersion || !IsPendingAuthID(s.ChallengeID) {
		return invalid
	}
	address, err := mail.ParseAddress(s.Email)
	if err != nil || address.Address != s.Email || len(s.Email) > 254 {
		return invalid
	}
	if s.CreatedAt.IsZero() || !s.ExpiresAt.After(s.CreatedAt) || s.ExpiresAt.Sub(s.CreatedAt) > PendingAuthTTL || s.CreatedAt.After(time.Now().Add(time.Minute)) {
		return invalid
	}
	if validateOrigin(s.ClerkOrigin) != nil {
		return invalid
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return invalid
	}
	if !pendingAuthIDFields.MatchString(s.SignInID) || !pendingAuthIDFields.MatchString(s.EmailAddressID) {
		return invalid
	}
	for _, token := range []string{s.NativeClientToken, s.Proof} {
		if len(token) > 8192 || strings.IndexFunc(token, func(r rune) bool { return r <= ' ' }) >= 0 {
			return invalid
		}
	}
	if s.OwnerID != "" && !pendingAuthIDFields.MatchString(s.OwnerID) {
		return invalid
	}
	if s.SessionID != "" && !pendingAuthIDFields.MatchString(s.SessionID) {
		return invalid
	}
	if s.RequestID != "" && !deletionUUID.MatchString(s.RequestID) {
		return invalid
	}
	if s.Submitted && (s.OwnerID == "" || s.RequestID == "") {
		return invalid
	}
	switch s.State {
	case "verification_required", "ready", "unknown", "requested", "running", "failed", "none":
		if s.NativeClientToken == "" {
			return invalid
		}
	case "completed":
		if s.OwnerID == "" || s.CompletedAt.IsZero() || s.CorrelationID == "" || s.Proof != "" || s.NativeClientToken != "" || s.SessionID != "" {
			return invalid
		}
		switch s.AppleAuthorization {
		case "not_linked", "revoked", "already_revoked_by_apple", "manual_action_required":
		default:
			return invalid
		}
	default:
		return invalid
	}
	return nil
}

func deletionPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, pendingDeletionFile), nil
}

func LoadPendingDeletion() (*PendingDeletion, error) {
	path, err := deletionPath()
	if err != nil {
		return nil, err
	}
	if err = CheckConfigDirPermissions(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err = checkPendingAuthFile(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > maxPendingAuthSize {
		return nil, errors.New("pending deletion state is too large")
	}
	var s PendingDeletion
	if json.Unmarshal(data, &s) != nil {
		return nil, errors.New("pending deletion state is malformed")
	}
	if err = validatePendingDeletion(s); err != nil {
		return nil, err
	}
	return &s, nil
}

func SavePendingDeletion(s PendingDeletion) error {
	if err := validatePendingDeletion(s); err != nil {
		return err
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err = CheckConfigDirPermissions(dir); err != nil {
		return err
	}
	if _, err = ConfigDir(); err != nil {
		return err
	}
	path := filepath.Join(dir, pendingDeletionFile)
	if err = checkPendingAuthFile(path); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(data) > maxPendingAuthSize {
		return errors.New("pending deletion state is too large")
	}
	return atomicWriteFile(path, append(data, '\n'), 0600)
}

func ClearPendingDeletion(id string) error {
	if !IsPendingAuthID(id) {
		return errors.New("invalid deletion challenge ID")
	}
	s, err := LoadPendingDeletion()
	if err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	if s.ChallengeID != id {
		return errors.New("deletion challenge changed concurrently")
	}
	if s.Submitted {
		return errors.New("a submitted deletion cannot be cancelled")
	}
	path, err := deletionPath()
	if err != nil {
		return err
	}
	return os.Remove(path)
}
