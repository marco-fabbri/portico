package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/api"
	"github.com/marco-fabbri/portico/internal/settings"
)

// The first boot: Keycloak is still pulling its image when the names are
// saved. The job waits, never reports a failure while Keycloak does not
// answer, and ends ready once it does.
func TestRealmJobWaitsForKeycloak(t *testing.T) {
	var answering atomic.Bool
	var mu sync.Mutex
	var phases []string
	var calls atomic.Int32
	j := newRealmJob(func(ctx context.Context, j *realmJob) (string, string) {
		calls.Add(1)
		if !answering.Load() {
			return phaseWaitingKeycloak, "Keycloak is starting"
		}
		j.set(phaseCreating, "creating")
		return phaseReady, "realm portico is ready"
	})
	j.waitRetry, j.failRetry = 5*time.Millisecond, time.Hour
	j.observe = func(p string) { mu.Lock(); phases = append(phases, p); mu.Unlock() }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go j.Start(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if p, _ := j.State(); p != phaseWaitingKeycloak {
		t.Fatalf("phase while Keycloak is down: %s", p)
	}
	answering.Store(true)
	for time.Now().Before(deadline) {
		if p, _ := j.State(); p == phaseReady {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if p, msg := j.State(); p != phaseReady || msg != "realm portico is ready" {
		t.Fatalf("final state %s %q", p, msg)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range phases {
		if p == phaseFailed {
			t.Fatalf("failed while waiting: %v", phases)
		}
	}
	// Ready: no more attempts until a kick (a name change).
	n := calls.Load()
	time.Sleep(30 * time.Millisecond)
	if calls.Load() != n {
		t.Fatalf("attempts after ready: %d → %d", n, calls.Load())
	}
}

func TestRealmJobKickRunsAnAttempt(t *testing.T) {
	var calls atomic.Int32
	j := newRealmJob(func(ctx context.Context, j *realmJob) (string, string) {
		calls.Add(1)
		return phaseFailed, "Keycloak refused"
	})
	j.waitRetry, j.failRetry = time.Hour, time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go j.Start(ctx)
	for calls.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	j.Kick()
	deadline := time.Now().Add(time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 2 {
		t.Fatalf("kick did not run an attempt: %d", calls.Load())
	}
}

// The attempt names the phase from the settings and Keycloak's answer.
func TestRealmAttemptPhases(t *testing.T) {
	file := settings.Defaults()
	opts := &api.SettingsOptions{File: &file, Bootstrapper: settings.NewBootstrapper(nil, nil)}
	j := newRealmJob(nil)
	if p, _ := realmAttempt(opts, "http://127.0.0.1:1", "")(context.Background(), j); p != phaseWaitingNames {
		t.Fatalf("no names: %s", p)
	}
	file.PortalHost, file.IDHost = "prism.example.test", "id.example.test"
	if p, msg := realmAttempt(opts, "http://127.0.0.1:1", "")(context.Background(), j); p != phaseWaitingKeycloak || !strings.Contains(msg, "Keycloak") {
		t.Fatalf("Keycloak down: %s %q", p, msg)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer up.Close()
	// Keycloak answers, but there is no temporary administrator and no realm.
	if p, msg := realmAttempt(opts, up.URL, "")(context.Background(), j); p != phaseFailed || !strings.Contains(msg, "existing realm") {
		t.Fatalf("no administrator: %s %q", p, msg)
	}
}
