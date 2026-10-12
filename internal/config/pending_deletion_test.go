package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func deletionConfigFixture(t *testing.T) (string, PendingDeletion) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := SetConfigDirOverride(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetConfigDirOverride("") })
	now := time.Now().UTC()
	return dir, PendingDeletion{Version: PendingDeletionVersion, ChallengeID: strings.Repeat("a", 32), Email: "fixture@example.com", CreatedAt: now, ExpiresAt: now.Add(PendingAuthTTL), ClerkOrigin: "https://clerk.ttsbuddy.com", Endpoint: "https://api.example.com/v1/delete-account", SignInID: "si_fixture", EmailAddressID: "idn_fixture", NativeClientToken: "native-fixture", State: "verification_required"}
}
func TestPendingDeletionSecureRoundTripAndExactClear(t *testing.T) {
	dir, s := deletionConfigFixture(t)
	if err := SavePendingDeletion(s); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPendingDeletion()
	if err != nil || loaded.ChallengeID != s.ChallengeID {
		t.Fatal("roundtrip failed", err)
	}
	info, err := os.Stat(filepath.Join(dir, pendingDeletionFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe state permissions", err)
	}
	if err := ClearPendingDeletion(strings.Repeat("b", 32)); err == nil {
		t.Fatal("cleared a different challenge")
	}
	s.Submitted = true
	s.RequestID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	s.OwnerID = "user_fixture"
	if err := SavePendingDeletion(s); err != nil {
		t.Fatal(err)
	}
	if err := ClearPendingDeletion(s.ChallengeID); err == nil {
		t.Fatal("cancelled submitted operation")
	}
}
func TestPendingDeletionRejectsUnsafeState(t *testing.T) {
	for _, kind := range []string{"directory", "permissions", "symlink", "oversized", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			dir, s := deletionConfigFixture(t)
			if err := SavePendingDeletion(s); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, pendingDeletionFile)
			var err error
			switch kind {
			case "directory":
				err = os.Chmod(dir, 0755)
			case "permissions":
				err = os.Chmod(path, 0644)
			case "symlink":
				err = os.Remove(path)
				if err == nil {
					err = os.Symlink(filepath.Join(dir, "missing"), path)
				}
			case "oversized":
				err = os.WriteFile(path, []byte(strings.Repeat("x", maxPendingAuthSize+1)), 0600)
			case "malformed":
				err = os.WriteFile(path, []byte("{"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := LoadPendingDeletion(); err == nil {
				t.Fatal("accepted unsafe state")
			}
		})
	}
}
func TestPendingDeletionExpiredOperationRemainsReadable(t *testing.T) {
	_, s := deletionConfigFixture(t)
	s.CreatedAt = time.Now().Add(-time.Hour)
	s.ExpiresAt = s.CreatedAt.Add(PendingAuthTTL)
	s.Submitted = true
	s.OwnerID = "user_fixture"
	s.RequestID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	s.State = "unknown"
	if err := SavePendingDeletion(s); err != nil {
		t.Fatal(err)
	}
	if loaded, err := LoadPendingDeletion(); err != nil || loaded.RequestID != s.RequestID {
		t.Fatal("expired recovery lost", err)
	}
}
