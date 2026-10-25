package branding

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The theme is read by Keycloak's own user inside its container: every
// directory must be world-readable whatever umask the service runs with
// (portico.service sets 0077, write-up 29: the login page answered 500).
func TestWriteThemeDirsReadableUnderUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	dir := filepath.Join(t.TempDir(), "theme")
	if err := WriteTheme(dir, noLogo()); err != nil {
		t.Fatalf("WriteTheme: %v", err)
	}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		want := os.FileMode(0o644)
		if info.IsDir() {
			want = 0o755
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s: mode %o, want %o", p, got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
