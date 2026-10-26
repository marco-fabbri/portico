package main

import (
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
