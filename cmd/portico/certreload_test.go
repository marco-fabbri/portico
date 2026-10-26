package main

import (
	"bytes"
	"crypto/tls"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/exposure"
)

func writePair(t *testing.T, dir, name string) []byte {
	t.Helper()
	c, k, err := exposure.SelfSigned([]string{name})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "public.key"), k, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "public.crt"), c, 0o600); err != nil {
		t.Fatal(err)
	}
	pair, _ := tls.X509KeyPair(c, k)
	return pair.Certificate[0]
}

// The files' mtime moves forward so a rewrite within the same clock
// tick is still seen as a change.
func touch(t *testing.T, dir string, d time.Duration) {
	for _, f := range []string{"public.crt", "public.key"} {
		at := time.Now().Add(d)
		if err := os.Chtimes(filepath.Join(dir, f), at, at); err != nil {
			t.Fatal(err)
		}
	}
}

func served(t *testing.T, r *certReloader) []byte {
	t.Helper()
	c, err := r.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	return c.Certificate[0]
}

func TestCertReloaderServesTheNewPair(t *testing.T) {
	dir := t.TempDir()
	a := writePair(t, dir, "a.example")
	r, err := newCertReloader(filepath.Join(dir, "public.crt"), filepath.Join(dir, "public.key"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(served(t, r), a) {
		t.Fatal("first pair not served")
	}
	b := writePair(t, dir, "b.example")
	touch(t, dir, time.Minute)
	// Within the check interval the old pair is still served.
	if !bytes.Equal(served(t, r), a) {
		t.Fatal("reloaded before the check interval")
	}
	r.checked = time.Time{}
	if !bytes.Equal(served(t, r), b) {
		t.Fatal("new pair not served after the check interval")
	}
}

func TestCertReloaderKeepsTheOldPairOnABadWrite(t *testing.T) {
	dir := t.TempDir()
	a := writePair(t, dir, "a.example")
	var logs bytes.Buffer
	r, err := newCertReloader(filepath.Join(dir, "public.crt"), filepath.Join(dir, "public.key"), slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := os.ReadFile(filepath.Join(dir, "public.key"))
	// A certificate whose key has not been written yet.
	_, k2, _ := exposure.SelfSigned([]string{"c.example"})
	os.WriteFile(filepath.Join(dir, "public.key"), k2, 0o600)
	touch(t, dir, time.Minute)
	for i := 0; i < 3; i++ {
		r.checked = time.Time{}
		if !bytes.Equal(served(t, r), a) {
			t.Fatal("bad pair served")
		}
	}
	if n := strings.Count(logs.String(), "public certificate not reloaded"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, logs.String())
	}
	if strings.Contains(logs.String(), "PRIVATE KEY") || strings.Contains(logs.String(), string(key[40:60])) {
		t.Fatal("key material in the log")
	}
	// The key catches up: the next check serves the new pair.
	b := writePair(t, dir, "b.example")
	touch(t, dir, 2*time.Minute)
	r.checked = time.Time{}
	if !bytes.Equal(served(t, r), b) {
		t.Fatal("not reloaded once the pair is whole")
	}
}

func TestCertReloaderRefusesAMissingPairAtStart(t *testing.T) {
	dir := t.TempDir()
	if _, err := newCertReloader(filepath.Join(dir, "public.crt"), filepath.Join(dir, "public.key"), slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("no error for a missing pair")
	}
}
