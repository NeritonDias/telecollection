package crypto

import (
	"bytes"
	"testing"
)

func TestNewSaltLengthAndUniqueness(t *testing.T) {
	s1, err := NewSalt()
	if err != nil {
		t.Fatalf("NewSalt returned error: %v", err)
	}
	if len(s1) != saltLen {
		t.Fatalf("salt length = %d, want %d", len(s1), saltLen)
	}

	s2, err := NewSalt()
	if err != nil {
		t.Fatalf("NewSalt returned error: %v", err)
	}
	if bytes.Equal(s1, s2) {
		t.Fatal("two salts are equal; expected CSPRNG-unique values")
	}
}

func TestDeriveKeyDeterministicAndLength(t *testing.T) {
	salt, err := NewSalt()
	if err != nil {
		t.Fatalf("NewSalt returned error: %v", err)
	}

	k1 := DeriveKey("correct horse battery staple", salt)
	k2 := DeriveKey("correct horse battery staple", salt)

	if len(k1) != KeyLen {
		t.Fatalf("derived key length = %d, want %d", len(k1), KeyLen)
	}
	if !bytes.Equal(k1, k2) {
		t.Fatal("DeriveKey not deterministic for same passphrase and salt")
	}
}

func TestDeriveKeyDifferentSaltDifferentKey(t *testing.T) {
	salt1, _ := NewSalt()
	salt2, _ := NewSalt()

	k1 := DeriveKey("same passphrase", salt1)
	k2 := DeriveKey("same passphrase", salt2)

	if bytes.Equal(k1, k2) {
		t.Fatal("different salts produced the same key")
	}
}

func TestDeriveKeyDifferentPassphraseDifferentKey(t *testing.T) {
	salt, _ := NewSalt()

	k1 := DeriveKey("passphrase A", salt)
	k2 := DeriveKey("passphrase B", salt)

	if bytes.Equal(k1, k2) {
		t.Fatal("different passphrases produced the same key")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	salt, _ := NewSalt()
	key := DeriveKey("round trip pass", salt)

	msg := []byte("the quick brown fox jumps over the lazy dog")

	blob, err := Encrypt(key, msg)
	if err != nil {
		t.Fatalf("Encrypt returned error: %v", err)
	}

	got, err := Decrypt(key, blob)
	if err != nil {
		t.Fatalf("Decrypt returned error: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("round trip mismatch: got %q, want %q", got, msg)
	}
}

func TestEncryptEmptyPlaintextRoundTrip(t *testing.T) {
	salt, _ := NewSalt()
	key := DeriveKey("empty", salt)

	blob, err := Encrypt(key, []byte{})
	if err != nil {
		t.Fatalf("Encrypt returned error: %v", err)
	}

	got, err := Decrypt(key, blob)
	if err != nil {
		t.Fatalf("Decrypt returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty plaintext, got %d bytes", len(got))
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	salt, _ := NewSalt()
	key := DeriveKey("right key", salt)
	msg := []byte("secret payload")

	blob, err := Encrypt(key, msg)
	if err != nil {
		t.Fatalf("Encrypt returned error: %v", err)
	}

	wrongSalt, _ := NewSalt()
	wrongKey := DeriveKey("wrong key", wrongSalt)

	got, err := Decrypt(wrongKey, blob)
	if err == nil {
		t.Fatalf("Decrypt with wrong key succeeded and returned %q; want error", got)
	}
	if got != nil {
		t.Fatal("Decrypt with wrong key returned non-nil plaintext")
	}
}

func TestDecryptTamperedBlobFails(t *testing.T) {
	salt, _ := NewSalt()
	key := DeriveKey("tamper", salt)
	msg := []byte("integrity matters")

	blob, err := Encrypt(key, msg)
	if err != nil {
		t.Fatalf("Encrypt returned error: %v", err)
	}

	// Flip one bit in the last byte (inside the ciphertext/tag region).
	tampered := make([]byte, len(blob))
	copy(tampered, blob)
	tampered[len(tampered)-1] ^= 0x01

	got, err := Decrypt(key, tampered)
	if err == nil {
		t.Fatalf("Decrypt of tampered blob succeeded and returned %q; want error", got)
	}
	if got != nil {
		t.Fatal("Decrypt of tampered blob returned non-nil plaintext")
	}
}

func TestEncryptNonceUniqueness(t *testing.T) {
	salt, _ := NewSalt()
	key := DeriveKey("nonce", salt)
	msg := []byte("identical plaintext")

	blob1, err := Encrypt(key, msg)
	if err != nil {
		t.Fatalf("Encrypt returned error: %v", err)
	}
	blob2, err := Encrypt(key, msg)
	if err != nil {
		t.Fatalf("Encrypt returned error: %v", err)
	}

	if bytes.Equal(blob1, blob2) {
		t.Fatal("two encryptions of the same plaintext produced identical blobs; nonce not unique")
	}
}

func TestDecryptShortBlobFails(t *testing.T) {
	salt, _ := NewSalt()
	key := DeriveKey("short", salt)

	// Blob shorter than the nonce must error, not panic.
	short := make([]byte, nonceLen-1)

	got, err := Decrypt(key, short)
	if err == nil {
		t.Fatal("Decrypt of short blob succeeded; want error")
	}
	if got != nil {
		t.Fatal("Decrypt of short blob returned non-nil plaintext")
	}

	// Also exercise an empty blob.
	if _, err := Decrypt(key, nil); err == nil {
		t.Fatal("Decrypt of nil blob succeeded; want error")
	}
}
