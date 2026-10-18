package settings

import (
	"log/slog"
	"regexp"
)

// Bootstrapper creates what Portico needs in the systems it fronts
// (design §4.4 and §4.5): the Keycloak realm with its clients, the
// Prism Central service account with its key, the SAML trust between
// the two. Every step is idempotent and reports what it created. The
// one-time credentials a step receives live in the request only: they
// are never stored and never logged.
type Bootstrapper struct {
	log *slog.Logger
}

// NewBootstrapper returns a bootstrapper.
func NewBootstrapper(log *slog.Logger) *Bootstrapper {
	if log == nil {
		log = slog.Default()
	}
	return &Bootstrapper{log: log}
}

// StateError says a precondition of a bootstrap step is not met in the
// settings or in the remote system; the API answers it with 409.
type StateError struct {
	Msg string
}

func (e *StateError) Error() string { return e.Msg }

var reRealm = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
