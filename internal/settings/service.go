package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/marco-fabbri/portico/internal/secrets"
	"github.com/marco-fabbri/portico/internal/store"
)

// ErrNotStored means no settings document exists in the store.
var ErrNotStored = errors.New("settings: nothing stored")

// OpenError names the field whose sealed value did not open.
type OpenError struct {
	Field string
	Err   error
}

func (e *OpenError) Error() string {
	return fmt.Sprintf("settings: %s cannot be opened with this master key: %v", e.Field, e.Err)
}

func (e *OpenError) Unwrap() error { return e.Err }

// ValidationError says the document itself is wrong (format): the
// caller's input, not the appliance, is at fault.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

// Service reads and writes the document, sealing the secrets.
type Service struct {
	st     store.Store
	sealer *secrets.Sealer
	mu     sync.Mutex // serialises the read-modify-write of Update and Patch
	hooks  []func(ctx context.Context, in Settings) error

	legacyLogged atomic.Bool
}

// NewService returns a service over the store with the sealer.
func NewService(st store.Store, sealer *secrets.Sealer) *Service {
	return &Service{st: st, sealer: sealer}
}

// OnChange registers a function run after every successful save with the
// document as saved (secrets unsealed). The appliance uses it to give
// Keycloak its public hostname as soon as id_host is known. A hook error
// is returned by the save; the document is stored regardless.
func (s *Service) OnChange(fn func(ctx context.Context, in Settings) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = append(s.hooks, fn)
}

// Stored reports whether a document exists.
func (s *Service) Stored(ctx context.Context) bool {
	_, err := s.st.GetSetting(ctx, SettingKey)
	return err == nil
}

// secretField is a sealed field with its name for errors.
type secretField struct {
	name string
	ptr  *Secret
}

// secretFields lists the sealed fields: each site's key, then the
// Keycloak admin client secret and the OIDC client secret.
func secretFields(s *Settings) []secretField {
	out := make([]secretField, 0, len(s.Sites)+2)
	for i := range s.Sites {
		out = append(out, secretField{"sites." + s.Sites[i].ID + ".api_key", &s.Sites[i].APIKey})
	}
	return append(out,
		secretField{"keycloak.admin_client_secret", &s.Keycloak.AdminClientSecret},
		secretField{"oidc.client_secret", &s.OIDC.ClientSecret})
}

// Get returns the document with its secrets opened.
func (s *Service) Get(ctx context.Context) (Settings, error) {
	raw, err := s.st.GetSetting(ctx, SettingKey)
	if errors.Is(err, store.ErrNotFound) {
		return Settings{}, ErrNotStored
	}
	if err != nil {
		return Settings{}, fmt.Errorf("settings: store: %w", err)
	}
	var out Settings
	if err := json.Unmarshal(raw, &out); err != nil {
		return Settings{}, fmt.Errorf("settings: stored document unreadable: %w", err)
	}
	// A single-Prism Central document: its connection becomes site-1,
	// the key moved still sealed (design sites §4.7).
	if adoptLegacy(raw, &out) && !s.legacyLogged.Swap(true) {
		slog.Default().Info("settings: the single Prism Central of the stored document is now site " + LegacySiteID)
	}
	for _, f := range secretFields(&out) {
		if *f.ptr == "" {
			continue
		}
		plain, err := s.sealer.Open(string(*f.ptr))
		if err != nil {
			return Settings{}, &OpenError{Field: f.name, Err: err}
		}
		*f.ptr = Secret(plain)
	}
	return out, nil
}

// Set checks the format of the document and stores it, sealing every
// non-empty secret. The document may be incomplete (a draft): Apply is
// what demands completeness.
func (s *Service) Set(ctx context.Context, in Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set(ctx, in)
}

func (s *Service) set(ctx context.Context, in Settings) error {
	if err := in.ValidateFormat(); err != nil {
		return &ValidationError{Err: err}
	}
	// The sites are copied: sealing them in place would seal the
	// caller's document and the one the hooks get.
	sealed := in
	sealed.Sites = slices.Clone(in.Sites)
	for _, f := range secretFields(&sealed) {
		if *f.ptr == "" {
			continue
		}
		v, err := s.sealer.Seal(string(*f.ptr))
		if err != nil {
			return fmt.Errorf("settings: seal %s: %w", f.name, err)
		}
		*f.ptr = Secret(v)
	}
	raw, err := json.Marshal(sealed)
	if err != nil {
		return err
	}
	if err := s.st.SetSetting(ctx, SettingKey, raw); err != nil {
		return fmt.Errorf("settings: store: %w", err)
	}
	for _, h := range s.hooks {
		if err := h(ctx, in); err != nil {
			return fmt.Errorf("settings: after save: %w", err)
		}
	}
	return nil
}

// Update merges in onto the stored document: empty secret fields keep
// the stored value, so a save without a password does not erase it.
func (s *Service) Update(ctx context.Context, in Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.Get(ctx)
	if err != nil && !errors.Is(err, ErrNotStored) {
		return err
	}
	if err == nil {
		keepSecrets(cur, &in)
	}
	return s.set(ctx, in)
}

// keepSecrets fills every empty secret of in with cur's: the Keycloak
// and OIDC secrets by field, the sites' keys by site id.
func keepSecrets(cur Settings, in *Settings) {
	if in.Keycloak.AdminClientSecret == "" {
		in.Keycloak.AdminClientSecret = cur.Keycloak.AdminClientSecret
	}
	if in.OIDC.ClientSecret == "" {
		in.OIDC.ClientSecret = cur.OIDC.ClientSecret
	}
	for i := range in.Sites {
		if in.Sites[i].APIKey != "" {
			continue
		}
		if site, ok := cur.Site(in.Sites[i].ID); ok {
			in.Sites[i].APIKey = site.APIKey
		}
	}
}

// Patch changes the stored document in place: it is read, handed to fn
// and stored again under the service's lock, so a writer that took
// seconds (a bootstrap) never reverts a save made meanwhile. With
// nothing stored fn starts from Defaults().
func (s *Service) Patch(ctx context.Context, fn func(*Settings)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.Get(ctx)
	if errors.Is(err, ErrNotStored) {
		cur, err = Defaults(), nil
	}
	if err != nil {
		return err
	}
	fn(&cur)
	return s.set(ctx, cur)
}
