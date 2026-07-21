// Package crypto provides authenticated symmetric encryption and
// passphrase-based key derivation used to protect secrets at rest
// (e.g. the encrypted Telegram session store and future E2E material).
//
// Primitives:
//   - Key derivation: Argon2id (golang.org/x/crypto/argon2).
//   - Encryption: AES-256-GCM (authenticated; detects tampering and wrong keys).
//
// All random material (salts, nonces) comes from crypto/rand.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

const (
	// saltLen is the length in bytes of a key-derivation salt.
	saltLen = 16
	// KeyLen is the length in bytes of a derived key (AES-256 => 32 bytes).
	KeyLen = 32
	// nonceLen is the AES-GCM standard nonce length in bytes.
	nonceLen = 12

	// Argon2id parameters. Kept as named constants so the derivation is
	// reproducible across runs and machines.
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
)

// ErrShortBlob is returned when a blob passed to Decrypt is too short to
// contain a nonce.
var ErrShortBlob = errors.New("crypto: blob too short")

// NewSalt returns a fresh 16-byte salt read from a cryptographically secure
// random source.
func NewSalt() ([]byte, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("crypto: generating salt: %w", err)
	}
	return salt, nil
}

// DeriveKey derives a 32-byte key from a passphrase and salt using Argon2id.
// The same (passphrase, salt) pair always yields the same key.
func DeriveKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, KeyLen)
}

// Encrypt seals plaintext with AES-256-GCM using key. A fresh random nonce is
// generated per call. The returned blob is nonce (12 bytes) || ciphertext,
// where ciphertext includes the GCM authentication tag.
func Encrypt(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: generating nonce: %w", err)
	}

	// Seal appends the ciphertext to nonce, yielding nonce||ciphertext.
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt reverses Encrypt. It returns an error if the blob is too short, if
// the key is wrong, or if the ciphertext has been tampered with.
func Decrypt(key, blob []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}

	if len(blob) < gcm.NonceSize() {
		return nil, ErrShortBlob
	}

	nonce, ciphertext := blob[:gcm.NonceSize()], blob[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: decrypting: %w", err)
	}
	return plaintext, nil
}

// newGCM builds an AES-256-GCM AEAD from key.
func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: creating GCM: %w", err)
	}
	return gcm, nil
}
