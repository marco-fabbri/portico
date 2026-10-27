package branding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/marco-fabbri/portico/internal/store"
)

// ApplyError means the settings were saved but applying them elsewhere
// (the Keycloak theme) failed; the caller shows it as a warning.
type ApplyError struct{ Err error }

func (e *ApplyError) Error() string { return "saved, but applying failed: " + e.Err.Error() }
func (e *ApplyError) Unwrap() error { return e.Err }

// Service reads and writes the branding with a cache and a change hook.
type Service struct {
	st  store.Store
	log *slog.Logger

	mu       sync.RWMutex
	cached   *Branding
	onChange func(ctx context.Context, b Branding) error
	// icons are the tenant icons of the branding whose IconVersion is
	// iconsFor: scaling an uploaded icon is done once, not per request.
	icons    Icons
	iconsFor string
}

// NewService returns a service over the store.
func NewService(st store.Store, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{st: st, log: log}
}

// OnChange registers the function run after every successful save (and
// by Apply): it writes the theme and tells Keycloak.
func (s *Service) OnChange(fn func(ctx context.Context, b Branding) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = fn
}

// Get returns the current branding, Default() when none is stored or the
// stored value cannot be read (logged).
func (s *Service) Get(ctx context.Context) Branding {
	s.mu.RLock()
	if s.cached != nil {
		b := *s.cached
		s.mu.RUnlock()
		return b
	}
	s.mu.RUnlock()
	b, ok := s.load(ctx)
	if ok {
		s.mu.Lock()
		s.cached = &b
		s.mu.Unlock()
	}
	return b
}

// load reads the stored branding; ok is false when the read failed, so
// the defaults it returns then are not cached and the next call tries
// again.
func (s *Service) load(ctx context.Context) (Branding, bool) {
	raw, err := s.st.GetSetting(ctx, SettingKey)
	if errors.Is(err, store.ErrNotFound) {
		return Default(), true
	}
	if err != nil {
		s.log.Warn("branding: read failed, using defaults", "err", err.Error())
		return Default(), false
	}
	b, err := decodeStored(raw)
	if err != nil {
		s.log.Warn("branding: stored value unreadable, using defaults", "err", err.Error())
		return Default(), true
	}
	return b, true
}

// Icons returns the tenant icons of the current branding. An icon that
// cannot be drawn (it was validated at save) falls back to the arch.
func (s *Service) Icons(ctx context.Context) Icons {
	b := s.Get(ctx)
	v := b.IconVersion()
	s.mu.RLock()
	if s.iconsFor == v {
		ic := s.icons
		s.mu.RUnlock()
		return ic
	}
	s.mu.RUnlock()
	ic, err := TenantIcons(b)
	if err != nil {
		s.log.Warn("branding: the icon could not be drawn, using the arch", "err", err.Error())
		b.IconData = nil
		ic, _ = TenantIcons(b)
	}
	s.mu.Lock()
	s.icons, s.iconsFor = ic, v
	s.mu.Unlock()
	return ic
}

// LogoFile returns the current branding's logo file (Branding.LogoFile).
func (s *Service) LogoFile(ctx context.Context) ([]byte, string, bool) {
	return s.Get(ctx).LogoFile()
}

// Set validates and stores b, updates the cache, then runs the change
// hook. A hook error comes back as *ApplyError: the save stands.
func (s *Service) Set(ctx context.Context, b Branding) error {
	if err := b.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if err := s.st.SetSetting(ctx, SettingKey, raw); err != nil {
		return fmt.Errorf("branding: store: %w", err)
	}
	s.mu.Lock()
	s.cached = &b
	hook := s.onChange
	s.mu.Unlock()
	s.log.Info("branding saved", "msp_name", b.MSPName, "logo", b.HasLogo(), "min_display_ms", b.MinDisplayMs)
	return s.Apply(ctx, hook, b)
}

// Apply runs the hook for b; used by Set and once at start.
func (s *Service) Apply(ctx context.Context, hook func(ctx context.Context, b Branding) error, b Branding) error {
	if hook == nil {
		return nil
	}
	if err := hook(ctx, b); err != nil {
		s.log.Warn("branding: applying failed", "err", err.Error())
		return &ApplyError{Err: err}
	}
	return nil
}

// Hook returns the registered change hook (nil when none).
func (s *Service) Hook() func(ctx context.Context, b Branding) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.onChange
}
