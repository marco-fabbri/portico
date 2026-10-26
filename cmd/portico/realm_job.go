package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/marco-fabbri/portico/internal/api"
)

// The phases of the realm job, shown by the Identity card.
const (
	phaseWaitingNames    = "waiting-names"
	phaseWaitingKeycloak = "waiting-keycloak"
	phaseCreating        = "creating"
	phaseReady           = "ready"
	phaseFailed          = "failed"
)

// realmJob creates the Keycloak realm in the background once the public
// names are saved (settings.Bootstrapper.KeycloakAuto), so nobody copies
// Keycloak's credentials from the console. At the first boot Keycloak
// may take minutes to pull its image: the job waits for it and retries,
// and the card shows where it is. Ready, it waits for a kick (a name
// change) and runs once more.
type realmJob struct {
	attempt   func(ctx context.Context, j *realmJob) (phase, msg string)
	waitRetry time.Duration // while the names or Keycloak are missing
	failRetry time.Duration // after a failure
	kick      chan struct{}
	observe   func(phase string) // tests: every phase set

	mu         sync.Mutex
	phase, msg string
}

func newRealmJob(attempt func(ctx context.Context, j *realmJob) (string, string)) *realmJob {
	return &realmJob{attempt: attempt, waitRetry: 10 * time.Second, failRetry: 30 * time.Second, kick: make(chan struct{}, 1), phase: phaseWaitingNames}
}

func (j *realmJob) set(phase, msg string) {
	j.mu.Lock()
	j.phase, j.msg = phase, msg
	obs := j.observe
	j.mu.Unlock()
	if obs != nil {
		obs(phase)
	}
}

// State is the phase and its message.
func (j *realmJob) State() (string, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.phase, j.msg
}

// Kick runs an attempt now: the public names were saved, or the
// administrator pressed Retry.
func (j *realmJob) Kick() {
	// Retry shows the new attempt at once, not the failure it retries.
	j.mu.Lock()
	if j.phase == phaseFailed {
		j.phase, j.msg = phaseCreating, "Trying again…"
	}
	j.mu.Unlock()
	select {
	case j.kick <- struct{}{}:
	default:
	}
}

// Start runs attempts until ctx is done.
func (j *realmJob) Start(ctx context.Context) {
	for {
		phase, msg := j.attempt(ctx, j)
		j.set(phase, msg)
		var after <-chan time.Time
		switch phase {
		case phaseReady:
		case phaseFailed:
			after = time.After(j.failRetry)
		default:
			after = time.After(j.waitRetry)
		}
		select {
		case <-ctx.Done():
			return
		case <-j.kick:
		case <-after:
		}
	}
}

// realmAttempt is one run of the job: the settings as they stand, a
// look at Keycloak, then KeycloakAuto with the installer's copy of the
// temporary administrator in bootstrapFile.
func realmAttempt(setup *api.SettingsOptions, keycloakURL, bootstrapFile string) func(context.Context, *realmJob) (string, string) {
	return func(ctx context.Context, j *realmJob) (string, string) {
		s, _, err := setup.Current(ctx)
		if err != nil {
			return phaseFailed, "the settings cannot be read: " + err.Error()
		}
		if s.PortalHost == "" || s.IDHost == "" {
			return phaseWaitingNames, "Save the public names: Portico then creates the realm and its clients."
		}
		const starting = "Keycloak is starting: at the first boot it downloads its image, which can take a few minutes."
		if !keycloakAnswers(ctx, keycloakURL) {
			return phaseWaitingKeycloak, starting
		}
		j.set(phaseCreating, "Creating the realm and Portico's clients…")
		res, _, err := setup.Bootstrapper.KeycloakAuto(ctx, keycloakURL, s, bootstrapFile)
		switch {
		case err != nil: // a *settings.StateError says what the operator does instead
			return phaseFailed, err.Error()
		case !res.OK && (strings.Contains(res.Message, "not answering") || strings.Contains(res.Message, "did not answer")):
			return phaseWaitingKeycloak, starting
		case !res.OK:
			return phaseFailed, res.Message
		}
		return phaseReady, res.Message
	}
}

// keycloakAnswers is one look at Keycloak's master realm endpoint: any
// answer below 500 means it is up.
func keycloakAnswers(ctx context.Context, keycloakURL string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(keycloakURL, "/")+"/realms/master", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}
