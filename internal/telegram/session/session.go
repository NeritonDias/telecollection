// Package session provides an encrypted-at-rest implementation of the gotd
// session.Storage interface. The Telegram MTProto session (which includes the
// authorization key that grants full account access) is sealed with
// AES-256-GCM via internal/crypto before it ever touches the disk, and the
// backing file is written with owner-only permissions.
//
// This closes the HIGH-severity finding of the original project, where the
// Telegram session was persisted in cleartext.
package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	gotdsession "github.com/gotd/td/session"

	"github.com/telecollection/telecollection/internal/crypto"
)

// filePerm is the permission of the on-disk session file: owner read/write
// only. The session bytes are secret material and must never be group- or
// world-readable.
const filePerm os.FileMode = 0o600

// EncryptedStorage is a gotd session.Storage that encrypts the session bytes at
// rest using AES-256-GCM. It is safe for concurrent use only insofar as the
// underlying filesystem operations are; gotd serialises session access itself.
type EncryptedStorage struct {
	path string
	key  []byte
}

// Compile-time assertion that EncryptedStorage satisfies the gotd interface.
var _ gotdsession.Storage = (*EncryptedStorage)(nil)

// NewEncryptedStorage returns a session.Storage that persists the session at
// path, encrypted with key. key must be a valid AES key length (32 bytes for
// AES-256, as produced by crypto.DeriveKey); an invalid length surfaces as an
// error from Store/LoadSession rather than here.
func NewEncryptedStorage(path string, key []byte) gotdsession.Storage {
	return &EncryptedStorage{path: path, key: key}
}

// LoadSession reads and decrypts the session from disk. If the file does not
// exist it returns session.ErrNotFound, which is the signal gotd uses to start
// the authentication flow. The decrypted bytes are never logged.
func (s *EncryptedStorage) LoadSession(_ context.Context) ([]byte, error) {
	blob, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, gotdsession.ErrNotFound
		}
		return nil, fmt.Errorf("session: reading %q: %w", s.path, err)
	}

	data, err := crypto.Decrypt(s.key, blob)
	if err != nil {
		return nil, fmt.Errorf("session: decrypting %q: %w", s.path, err)
	}
	return data, nil
}

// StoreSession encrypts data and writes it to disk with owner-only permissions.
// The write is atomic: the ciphertext is written to a temporary file in the
// same directory and then renamed over the target, so a crash mid-write never
// leaves a truncated session. The plaintext bytes are never logged.
func (s *EncryptedStorage) StoreSession(_ context.Context, data []byte) error {
	blob, err := crypto.Encrypt(s.key, data)
	if err != nil {
		return fmt.Errorf("session: encrypting: %w", err)
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("session: creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we fail before the rename succeeds.
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(filePerm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("session: setting permissions: %w", err)
	}
	if _, err := tmp.Write(blob); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("session: writing temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("session: syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("session: closing temp file: %w", err)
	}

	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("session: renaming temp file into place: %w", err)
	}
	// Ensure final permissions even if the umask/OS altered the temp file's
	// mode across the rename.
	if err := os.Chmod(s.path, filePerm); err != nil {
		return fmt.Errorf("session: enforcing final permissions: %w", err)
	}
	return nil
}
