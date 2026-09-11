package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func validPendingAuthForTest(t *testing.T) PendingAuth {
	t.Helper()
	id, err := NewPendingAuthID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return PendingAuth{
		Version:           PendingAuthVersion,
		ChallengeID:       id,
		Mode:              "login",
		CreatedAt:         now,
		ExpiresAt:         now.Add(PendingAuthTTL),
		ClerkOrigin:       "https://clerk.ttsbuddy.com",
		APIOrigin:         "https://www.ttsbuddy.com",
		SignInID:          "s_123",
		EmailAddressID:    "idn_123",
		NativeClientToken: "client-token-opaque",
	}
}

func TestPendingAuthRoundTripUsesSecureFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := SetConfigDirOverride(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetConfigDirOverride("") })
	state := validPendingAuthForTest(t)
	if err := SavePendingAuth(state); err != nil {
		t.Fatalf("SavePendingAuth: %v", err)
	}
	loaded, err := LoadPendingAuth()
	if err != nil {
		t.Fatalf("LoadPendingAuth: %v", err)
	}
	if loaded == nil || loaded.ChallengeID != state.ChallengeID || loaded.NativeClientToken != state.NativeClientToken {
		t.Fatalf("loaded state mismatch: %#v", loaded)
	}
	info, err := os.Stat(filepath.Join(dir, pendingAuthFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("pending auth mode = %o, want 600", info.Mode().Perm())
	}
	if err := ClearPendingAuth(state.ChallengeID); err != nil {
		t.Fatalf("ClearPendingAuth: %v", err)
	}
	if loaded, err := LoadPendingAuth(); err != nil || loaded != nil {
		t.Fatalf("pending auth should be cleared: %#v %v", loaded, err)
	}
}

func TestPendingAuthExpiredStateLoadsForCallerCleanup(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := SetConfigDirOverride(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetConfigDirOverride("") })
	state := validPendingAuthForTest(t)
	state.CreatedAt = time.Now().UTC().Add(-PendingAuthTTL)
	state.ExpiresAt = time.Now().UTC().Add(-time.Second)
	if err := SavePendingAuth(state); err == nil {
		t.Fatal("SavePendingAuth accepted expired state")
	}
	// Write a validly shaped expired state to model a process that crossed the
	// deadline after the atomic save.
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"version":1,"challenge_id":"` + state.ChallengeID + `","mode":"login","created_at":"` + state.CreatedAt.Format(time.RFC3339Nano) + `","expires_at":"` + state.ExpiresAt.Format(time.RFC3339Nano) + `","clerk_origin":"https://clerk.ttsbuddy.com","api_origin":"https://www.ttsbuddy.com","sign_in_id":"s_123","email_address_id":"idn_123","native_client_token":"client-token-opaque"}`)
	if err := os.WriteFile(filepath.Join(dir, pendingAuthFile), data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPendingAuth()
	if err != nil || loaded == nil {
		t.Fatalf("LoadPendingAuth expired = %#v, %v; caller needs it to clear", loaded, err)
	}
}

func TestPendingAuthRejectsUnsafePermissionsAndSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := SetConfigDirOverride(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetConfigDirOverride("") })
	state := validPendingAuthForTest(t)
	if err := SavePendingAuth(state); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, pendingAuthFile)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPendingAuth(); err == nil {
		t.Fatal("LoadPendingAuth accepted world-readable state")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPendingAuth(); err == nil {
		t.Fatal("LoadPendingAuth accepted symlink state")
	}
}

func TestPendingAuthRejectsUnsafeConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := SetConfigDirOverride(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetConfigDirOverride("") })
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := SavePendingAuth(validPendingAuthForTest(t)); err == nil {
		t.Fatal("SavePendingAuth accepted a world-readable config directory")
	}
}
