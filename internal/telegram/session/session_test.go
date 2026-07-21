package session

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	gotdsession "github.com/gotd/td/session"
)

// testKey is a fixed 32-byte key (AES-256) used across tests.
var testKey = []byte("0123456789abcdef0123456789abcdef")

func TestStoreLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.bin")
	st := NewEncryptedStorage(path, testKey)
	ctx := context.Background()

	want := []byte("some-telegram-session-data-\x00\x01\x02")
	if err := st.StoreSession(ctx, want); err != nil {
		t.Fatalf("StoreSession: %v", err)
	}

	got, err := st.LoadSession(ctx)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, want)
	}
}

func TestStoreSessionConfidentiality(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.bin")
	st := NewEncryptedStorage(path, testKey)
	ctx := context.Background()

	secret := []byte("SECRET-SESSION-BYTES")
	if err := st.StoreSession(ctx, secret); err != nil {
		t.Fatalf("StoreSession: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading raw file: %v", err)
	}
	if bytes.Contains(raw, secret) {
		t.Fatalf("plaintext secret found on disk in cleartext")
	}
}

func TestLoadSessionMissingReturnsErrNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.bin")
	st := NewEncryptedStorage(path, testKey)

	_, err := st.LoadSession(context.Background())
	if !errors.Is(err, gotdsession.ErrNotFound) {
		t.Fatalf("expected session.ErrNotFound, got %v", err)
	}
}

func TestLoadSessionWrongKeyFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.bin")
	ctx := context.Background()

	if err := NewEncryptedStorage(path, testKey).StoreSession(ctx, []byte("SECRET-SESSION-BYTES")); err != nil {
		t.Fatalf("StoreSession: %v", err)
	}

	wrongKey := []byte("ffffffffffffffffffffffffffffffff")
	got, err := NewEncryptedStorage(path, wrongKey).LoadSession(ctx)
	if err == nil {
		t.Fatalf("expected error with wrong key, got plaintext %q", got)
	}
	if bytes.Contains(got, []byte("SECRET-SESSION-BYTES")) {
		t.Fatalf("wrong key must not reveal plaintext")
	}
}

func TestFilePermissions0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows does not implement Unix permission bits; os.Stat always
		// reports rw-rw-rw- regardless of the Chmod request. The 0600 mode is
		// enforced and meaningfully verified on Linux/macOS (CI).
		t.Skip("Unix permission bits not supported on Windows")
	}
	path := filepath.Join(t.TempDir(), "session.bin")
	st := NewEncryptedStorage(path, testKey)

	if err := st.StoreSession(context.Background(), []byte("data")); err != nil {
		t.Fatalf("StoreSession: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("file permissions too open: %v", perm)
	}
}
