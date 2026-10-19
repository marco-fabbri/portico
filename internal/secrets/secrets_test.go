package secrets

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateKeyCreates0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	k1, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 || info.Size() != KeySize {
		t.Fatalf("key file: %v mode %04o size %d", err, info.Mode().Perm(), info.Size())
	}
	if len(k1) != KeySize || bytes.Equal(k1, make([]byte, KeySize)) {
		t.Fatalf("key = %d bytes, zero=%v", len(k1), bytes.Equal(k1, make([]byte, KeySize)))
	}
	k2, err := LoadOrCreateKey(path)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatalf("second load: %v equal=%v", err, bytes.Equal(k1, k2))
	}
}

func TestLoadOrCreateKeyRefusesWideMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(path, make([]byte, KeySize), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadOrCreateKey(path)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("wide mode: %v", err)
	}
}

func TestLoadOrCreateKeyRefusesWrongSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(path, make([]byte, 16), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadOrCreateKey(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("wrong size: %v", err)
	}
}

func newSealer(t *testing.T, seed byte) *Sealer {
	t.Helper()
	key := bytes.Repeat([]byte{seed}, KeySize)
	s, err := NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewSealerKeySize(t *testing.T) {
	if _, err := NewSealer(make([]byte, 16)); !errors.Is(err, ErrKeySize) {
		t.Fatalf("16-byte key: %v", err)
	}
}

func TestSealOpenRoundTripAndFreshNonce(t *testing.T) {
	s := newSealer(t, 1)
	a, err := s.Seal("hunter2-secret")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Seal("hunter2-secret")
	if a == b || !strings.HasPrefix(a, "v1:") || !IsSealed(a) || IsSealed("hunter2-secret") {
		t.Fatalf("sealed = %q / %q", a, b)
	}
	for _, v := range []string{a, b} {
		got, err := s.Open(v)
		if err != nil || got != "hunter2-secret" {
			t.Fatalf("Open(%q) = %q, %v", v, got, err)
		}
	}
}

func TestOpenWrongKeyAndTamper(t *testing.T) {
	s := newSealer(t, 1)
	sealed, _ := s.Seal("x")
	if _, err := newSealer(t, 2).Open(sealed); !errors.Is(err, ErrOpen) {
		t.Fatalf("wrong key: %v", err)
	}
	// Flip a bit of the decoded ciphertext, not of the base64 text: a bit
	// flipped in a base64 character can land in the padding bits and
	// decode to the same bytes (the test failed now and then that way).
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, prefix))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0x01
	tampered := prefix + base64.StdEncoding.EncodeToString(raw)
	if _, err := s.Open(tampered); !errors.Is(err, ErrOpen) {
		t.Fatalf("tampered: %v", err)
	}
	if _, err := s.Open("plain"); !errors.Is(err, ErrNotSealed) {
		t.Fatalf("plain: %v", err)
	}
}

func TestSealedNeverContainsPlaintext(t *testing.T) {
	s := newSealer(t, 1)
	sealed, _ := s.Seal("hunter2-secret")
	if strings.Contains(sealed, "hunter2") {
		t.Fatal("sealed value contains the plaintext")
	}
}
