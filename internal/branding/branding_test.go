package branding

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/store"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("Default().Validate() = %v", err)
	}
	if Default().MSPName != "Portico" || Default().AccentColor != "#1f5f8b" || Default().HasLogo() {
		t.Fatalf("Default() = %+v", Default())
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Branding)
		want string
	}{
		{"long name", func(b *Branding) { b.MSPName = strings.Repeat("a", MaxNameLen+1) }, "msp_name"},
		{"empty name", func(b *Branding) { b.MSPName = " " }, "msp_name"},
		{"long tagline", func(b *Branding) { b.Tagline = strings.Repeat("a", MaxTaglineLen+1) }, "tagline"},
		{"colour word", func(b *Branding) { b.AccentColor = "red" }, "accent_color"},
		{"colour short", func(b *Branding) { b.AccentColor = "#12345" }, "accent_color"},
		{"colour hex", func(b *Branding) { b.AccentColor = "#GGGGGG" }, "accent_color"},
		{"logo gif", func(b *Branding) { b.LogoType, b.LogoData = "image/gif", []byte("GIF89a") }, "logo_type"},
		{"logo big", func(b *Branding) { b.LogoType, b.LogoData = "image/png", bytes.Repeat([]byte{1}, MaxLogoBytes+1) }, "logo"},
		{"logo type without data", func(b *Branding) { b.LogoType = "image/png" }, "logo"},
		{"display negative", func(b *Branding) { b.MinDisplayMs = -1 }, "min_display_ms"},
		{"display huge", func(b *Branding) { b.MinDisplayMs = MaxDisplayMs + 1 }, "min_display_ms"},
	}
	for _, c := range cases {
		b := Default()
		c.mut(&b)
		err := b.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Validate() = %v, want an error naming %s", c.name, err, c.want)
		}
	}
}

type countingStore struct {
	store.Store
	gets int
}

func (c *countingStore) GetSetting(ctx context.Context, key string) ([]byte, error) {
	c.gets++
	return c.Store.GetSetting(ctx, key)
}

func newStore(t *testing.T) *store.SQLite {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestServiceGetDefaultWhenUnset(t *testing.T) {
	svc := NewService(newStore(t), nil)
	if got := svc.Get(context.Background()); got.MSPName != "Portico" {
		t.Fatalf("Get on an empty store = %+v", got)
	}
}

func TestServiceSetStoresAndCaches(t *testing.T) {
	st := newStore(t)
	cs := &countingStore{Store: st}
	svc := NewService(cs, nil)
	ctx := context.Background()
	b := Default()
	b.MSPName, b.AccentColor, b.LogoType, b.LogoData = "ACME Cloud", "#8b1f5f", "image/png", []byte("\x89PNG\r\n\x1a\n")
	if err := svc.Set(ctx, b); err != nil {
		t.Fatal(err)
	}
	gets := cs.gets
	if got := svc.Get(ctx); got.MSPName != "ACME Cloud" || !bytes.Equal(got.LogoData, b.LogoData) {
		t.Fatalf("Get after Set = %+v", got)
	}
	if cs.gets != gets {
		t.Fatal("Get after Set read the store again instead of the cache")
	}
	other := NewService(st, nil)
	if got := other.Get(ctx); got.MSPName != "ACME Cloud" || got.AccentColor != "#8b1f5f" || got.LogoType != "image/png" {
		t.Fatalf("Get from a second service = %+v", got)
	}
}

func TestServiceSetValidates(t *testing.T) {
	svc := NewService(newStore(t), nil)
	b := Default()
	b.AccentColor = "blue"
	if err := svc.Set(context.Background(), b); err == nil || !strings.Contains(err.Error(), "accent_color") {
		t.Fatalf("Set invalid = %v", err)
	}
	if svc.Get(context.Background()).AccentColor != "#1f5f8b" {
		t.Fatal("an invalid Set changed the value")
	}
}

func TestServiceSetCallsHookAndKeepsSaveOnHookError(t *testing.T) {
	var buf bytes.Buffer
	svc := NewService(newStore(t), slog.New(slog.NewJSONHandler(&buf, nil)))
	called := ""
	svc.OnChange(func(ctx context.Context, b Branding) error {
		called = b.MSPName
		return errors.New("keycloak realm: status 500")
	})
	b := Default()
	b.MSPName = "Hooked"
	err := svc.Set(context.Background(), b)
	var ae *ApplyError
	if !errors.As(err, &ae) || !strings.Contains(ae.Error(), "keycloak realm") {
		t.Fatalf("Set with a failing hook = %v, want *ApplyError", err)
	}
	if called != "Hooked" {
		t.Fatalf("hook saw %q", called)
	}
	if svc.Get(context.Background()).MSPName != "Hooked" {
		t.Fatal("the save was lost because the hook failed")
	}
}

func TestLogoSVGWithScriptIsStoredAsIs(t *testing.T) {
	b := Default()
	b.LogoType, b.LogoData = "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	if err := b.Validate(); err != nil {
		t.Fatalf("Validate SVG = %v", err)
	}
}
