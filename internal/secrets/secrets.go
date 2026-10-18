// Package secrets owns the appliance's master key and seals the secrets
// Portico keeps in its store (design §4.6): AES-256-GCM, one fresh nonce
// per value, the key in a file the service user owns with mode 0600. The
// key never leaves the appliance; a value that does not open is an error,
// never a default.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// KeySize is the master key length in bytes (AES-256).
const KeySize = 32

const prefix = "v1:"

var (
	// ErrKeySize is returned by NewSealer for a key of the wrong length.
	ErrKeySize = errors.New("secrets: the master key must be 32 bytes")
	// ErrNotSealed is returned by Open for a value without the sealed prefix.
	ErrNotSealed = errors.New("secrets: value is not sealed")
	// ErrOpen is returned by Open when the key is wrong or the data was changed.
	ErrOpen = errors.New("secrets: cannot open the sealed value (wrong master key or damaged data)")
)

// LoadKey reads an existing key file; a missing file is an error. Read-only
// commands use it so they never create a key owned by the wrong user.
func LoadKey(path string) ([]byte, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("secrets: key file %s: %w", path, err)
	}
	return LoadOrCreateKey(path)
}

// LoadOrCreateKey reads the key file, or creates it with 32 random bytes
// and mode 0600 when it is missing. A file readable by group or others,
// or of the wrong size, is refused with an error naming the path.
func LoadOrCreateKey(path string) ([]byte, error) {
	info, err := os.Stat(path)
	switch {
	case err == nil:
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			return nil, fmt.Errorf("secrets: key file %s has mode %04o; it must not be readable by group or others", path, mode)
		}
		key, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("secrets: key file %s: %w", path, err)
		}
		if len(key) != KeySize {
			return nil, fmt.Errorf("secrets: key file %s holds %d bytes, want %d", path, len(key), KeySize)
		}
		return key, nil
	case os.IsNotExist(err):
		key := make([]byte, KeySize)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("secrets: random key: %w", err)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("secrets: create key file %s: %w", path, err)
		}
		if _, err := f.Write(key); err != nil {
			f.Close()
			os.Remove(path)
			return nil, fmt.Errorf("secrets: write key file %s: %w", path, err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("secrets: write key file %s: %w", path, err)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("secrets: key file %s: %w", path, err)
	}
}

// Sealer seals and opens values with one master key.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer returns a sealer for a 32-byte key.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, ErrKeySize
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal returns "v1:" + base64(nonce || ciphertext) with a fresh nonce.
func (s *Sealer) Seal(plaintext string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secrets: nonce: %w", err)
	}
	out := s.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open reverses Seal. ErrNotSealed for a value without the prefix, ErrOpen
// when the key is wrong or the data was changed.
func (s *Sealer) Open(sealed string) (string, error) {
	if !IsSealed(sealed) {
		return "", ErrNotSealed
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, prefix))
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", ErrOpen
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", ErrOpen
	}
	return string(plain), nil
}

// IsSealed reports whether v carries the sealed prefix.
func IsSealed(v string) bool { return strings.HasPrefix(v, prefix) }
