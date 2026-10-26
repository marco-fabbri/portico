package settings

import (
	"context"
	"log/slog"
	"regexp"
	"sync"
	"time"
)

// Bootstrapper creates what Portico needs in the systems it fronts
// (design §4.4 and §4.5): the Keycloak realm with its clients, the
// Prism Central service account with its key, the SAML trust between
// the two. Every step is idempotent and reports what it created. The
// one-time credentials a step receives live in the request only: they
// are never stored and never logged.
type Bootstrapper struct {
	log *slog.Logger
	svc *Service
	// kcMu puts the realm job and "Use an existing realm" one after the
	// other: the second finds what the first made instead of both
	// creating the same clients at once.
	kcMu sync.Mutex
}

// NewBootstrapper returns a bootstrapper that stores what a step sets
// through svc (Service.Patch, so a save made meanwhile survives). With a
// nil svc the steps only return the updated document.
func NewBootstrapper(log *slog.Logger, svc *Service) *Bootstrapper {
	if log == nil {
		log = slog.Default()
	}
	return &Bootstrapper{log: log, svc: svc}
}

// store applies patch to the stored document. It runs on a context of
// its own: the caller's may be gone (the browser closed the page during
// a long step) and a result that exists remotely must be stored, or
// undone by the caller.
func (b *Bootstrapper) store(ctx context.Context, patch func(*Settings)) (bool, error) {
	if b.svc == nil {
		return false, nil
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := b.svc.Patch(sctx, patch); err != nil {
		b.log.Error("bootstrap: settings not stored", "err", err.Error())
		return false, err
	}
	return true, nil
}

// StateError says a precondition of a bootstrap step is not met in the
// settings or in the remote system; the API answers it with 409.
type StateError struct {
	Msg string
}

func (e *StateError) Error() string { return e.Msg }

var reRealm = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
