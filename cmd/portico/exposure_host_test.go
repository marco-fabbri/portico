package main

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/exposure"
)

// Review of the setup-in-the-web-ui branch: the request directory is
// owned by the unprivileged portico user, and apply runs as root. A link
// planted there must not make root write or read anywhere else, and a
// FIFO must not hang it.
func TestExposureApplyIgnoresLinksPlantedInTheRequestDir(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, "shadow")
	os.WriteFile(victim, []byte("root:secret\n"), 0o600)
	cfg := filepath.Join(outside, "portico.yaml")
	os.WriteFile(cfg, []byte("public:\n  listen: \":443\"\n"), 0o640)

	// result.json is a link to a file root must not touch.
	os.Symlink(victim, filepath.Join(dir, exposure.ResultFile))
	os.WriteFile(filepath.Join(dir, exposure.RequestFile), []byte(`{"id":"r1","mode":"bogus"}`), 0o600)
	r := exposure.Apply(&hostSystem{}, dir, cfg)
	if r.ID != "r1" {
		t.Fatalf("result %+v", r)
	}
	if b, _ := os.ReadFile(victim); string(b) != "root:secret\n" {
		t.Fatalf("written through the link: %q", b)
	}
	if fi, err := os.Lstat(filepath.Join(dir, exposure.ResultFile)); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o644 {
		t.Fatalf("result.json: %v %v", fi, err)
	}

	// request.json is a link to a readable file: refused, the link removed.
	other := filepath.Join(outside, "req.json")
	os.WriteFile(other, []byte(`{"id":"r2","mode":"tunnel"}`), 0o600)
	os.Symlink(other, filepath.Join(dir, exposure.RequestFile))
	if r := exposure.Apply(&hostSystem{}, dir, cfg); r.OK || r.ID == "r2" {
		t.Fatalf("request read through a link: %+v", r)
	}
	if _, err := os.Lstat(filepath.Join(dir, exposure.RequestFile)); err == nil {
		t.Fatal("linked request left in place")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("the link's target was removed")
	}

	// A FIFO in place of the key: no hang.
	os.WriteFile(filepath.Join(dir, exposure.RequestFile), []byte(`{"id":"r3","mode":"direct"}`), 0o600)
	os.WriteFile(filepath.Join(dir, exposure.CertFile), []byte("x"), 0o600)
	if err := syscall.Mkfifo(filepath.Join(dir, exposure.KeyFile), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan exposure.Result, 1)
	go func() { done <- exposure.Apply(&hostSystem{}, dir, cfg) }()
	select {
	case r := <-done:
		if r.OK || !strings.Contains(r.Message, "key") {
			t.Fatalf("result %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("apply hangs on a FIFO")
	}
}

// swapHost is the appliance's host with the system side stubbed: its
// first command replaces the request directory by a link, as a
// compromised portico user could while root waits on systemctl.
type swapHost struct {
	*hostSystem
	swap func()
}

func (h *swapHost) Exec(_ io.Reader, argv ...string) (string, error) {
	if h.swap != nil {
		h.swap()
		h.swap = nil
	}
	return "", errors.New("stubbed: " + strings.Join(argv, " "))
}

func (h *swapHost) WriteFile(string, []byte, os.FileMode) error { return nil }

func (h *swapHost) ReadFile(p string) ([]byte, error) {
	if p == exposure.TokenPath {
		return nil, fs.ErrNotExist
	}
	return os.ReadFile(p)
}

func (h *swapHost) Remove(string) error { return nil }

// Review before v0.1.0: the directory itself is portico's to rename, so
// root opens it once and works through that handle; a link in its place,
// before or during apply, never makes root delete or write elsewhere.
func TestExposureApplyKeepsToTheDirectoryItOpened(t *testing.T) {
	base, victim := t.TempDir(), t.TempDir()
	cfg := filepath.Join(base, "portico.yaml")
	os.WriteFile(cfg, []byte("public:\n  listen: \":443\"\n  tls:\n    cert_file: /etc/portico/tls/public.crt\n    key_file: /etc/portico/tls/public.key\n"), 0o640)
	for _, f := range []string{exposure.CertFile, exposure.KeyFile, exposure.TokenFile} {
		os.WriteFile(filepath.Join(victim, f), []byte("keep"), 0o600)
	}
	untouched := func() {
		t.Helper()
		for _, f := range []string{exposure.CertFile, exposure.KeyFile, exposure.TokenFile} {
			if b, err := os.ReadFile(filepath.Join(victim, f)); err != nil || string(b) != "keep" {
				t.Fatalf("%s outside the request directory: %q %v", f, b, err)
			}
		}
		if _, err := os.Lstat(filepath.Join(victim, exposure.ResultFile)); err == nil {
			t.Fatal("result.json written outside the request directory")
		}
	}

	// Swapped while root waits on systemctl.
	dir := filepath.Join(base, "exposure")
	os.Mkdir(dir, 0o700)
	os.WriteFile(filepath.Join(dir, exposure.RequestFile), []byte(`{"id":"r1","mode":"tunnel"}`), 0o600)
	os.WriteFile(filepath.Join(dir, exposure.TokenFile), []byte("t"), 0o600)
	h := &swapHost{hostSystem: &hostSystem{}, swap: func() {
		if err := os.Rename(dir, dir+".away"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, dir); err != nil {
			t.Fatal(err)
		}
	}}
	if r := exposure.Apply(h, dir, cfg); r.ID != "r1" {
		t.Fatalf("result %+v", r)
	}
	if h.swap != nil {
		t.Fatal("apply ran no command")
	}
	untouched()
	if _, err := os.Lstat(filepath.Join(dir+".away", exposure.ResultFile)); err != nil {
		t.Fatalf("the result is not in the directory root opened: %v", err)
	}

	// A link from the start: refused, nothing read or removed there.
	os.WriteFile(filepath.Join(victim, exposure.RequestFile), []byte(`{"id":"r2","mode":"tunnel"}`), 0o600)
	if r := exposure.Apply(h, dir, cfg); r.ID == "r2" || r.OK {
		t.Fatalf("request read through a linked directory: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(victim, exposure.RequestFile)); err != nil {
		t.Fatal("request.json removed through a linked directory")
	}
	os.Remove(filepath.Join(victim, exposure.RequestFile))
	untouched()
}
