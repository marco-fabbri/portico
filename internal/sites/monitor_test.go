package sites

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/store"
)

func openStore(t *testing.T) *store.SQLite {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMonitorStatus(t *testing.T) {
	st := openStore(t)
	var down atomic.Bool
	m := NewMonitor(st, map[string]Prober{
		"a": func(ctx context.Context) error { return nil },
		"b": func(ctx context.Context) error {
			if down.Load() {
				return errors.New("unreachable")
			}
			return nil
		},
	}, nil)
	if m.Status("a") != Available || m.Status("b") != Available {
		t.Fatal("before any probe a site counts as available")
	}
	down.Store(true)
	for i := 0; i < FailuresToUnreachable; i++ {
		m.ProbeOnce(context.Background(), time.Second)
	}
	if m.Status("a") != Available || m.Status("b") != Unreachable {
		t.Fatalf("after probe: a=%s b=%s", m.Status("a"), m.Status("b"))
	}
	if err := m.SetMaintenance(context.Background(), "a", true); err != nil {
		t.Fatal(err)
	}
	if err := m.SetMaintenance(context.Background(), "b", true); err != nil {
		t.Fatal(err)
	}
	if m.Status("a") != Maintenance || m.Status("b") != Maintenance || !m.Maintenance("b") {
		t.Fatalf("maintenance wins: a=%s b=%s", m.Status("a"), m.Status("b"))
	}
	m.SetMaintenance(context.Background(), "b", false)
	if m.Status("b") != Unreachable {
		t.Fatalf("b after maintenance off = %s", m.Status("b"))
	}
	if m.Status("unknown") != Unreachable {
		t.Fatalf("a site without a prober = %s, want unreachable", m.Status("unknown"))
	}
}

func TestProbeTimeout(t *testing.T) {
	m := NewMonitor(openStore(t), map[string]Prober{"slow": func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}, nil)
	start := time.Now()
	for i := 0; i < FailuresToUnreachable; i++ {
		m.ProbeOnce(context.Background(), 50*time.Millisecond)
	}
	if time.Since(start) > time.Second || m.Status("slow") != Unreachable {
		t.Fatalf("slow probe: %v %s", time.Since(start), m.Status("slow"))
	}
}

func TestMaintenancePersists(t *testing.T) {
	st := openStore(t)
	m := NewMonitor(st, map[string]Prober{"a": func(context.Context) error { return nil }}, nil)
	if err := m.SetMaintenance(context.Background(), "a", true); err != nil {
		t.Fatal(err)
	}
	again := NewMonitor(st, map[string]Prober{"a": func(context.Context) error { return nil }}, nil)
	if err := again.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again.Status("a") != Maintenance {
		t.Fatalf("maintenance lost across monitors: %s", again.Status("a"))
	}
	if raw, _ := st.GetSetting(context.Background(), StateKey); string(raw) != `{"maintenance":{"a":true}}` {
		t.Fatalf("stored state = %s", raw)
	}
}

func TestRunProbesAtOnceAndStops(t *testing.T) {
	var n atomic.Int32
	m := NewMonitor(openStore(t), map[string]Prober{"a": func(context.Context) error { n.Add(1); return nil }}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx, 20*time.Millisecond, time.Second); close(done) }()
	time.Sleep(70 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
	if n.Load() < 2 {
		t.Fatalf("probes = %d", n.Load())
	}
}

// One slow answer from a Prism Central must not lock its tenants out
// (write-up 21: the clusters call took 9 to 30 s for three minutes on
// cluster-1, then 0.6 s again): only FailuresToUnreachable failed probes in a
// row make a site unreachable, and one answer makes it available again.
func TestUnreachableOnlyAfterConsecutiveFailures(t *testing.T) {
	var down atomic.Bool
	m := NewMonitor(openStore(t), map[string]Prober{"a": func(context.Context) error {
		if down.Load() {
			return errors.New("timeout")
		}
		return nil
	}}, nil)
	ctx := context.Background()
	down.Store(true)
	for i := 1; i < FailuresToUnreachable; i++ {
		m.ProbeOnce(ctx, time.Second)
		if m.Status("a") != Available {
			t.Fatalf("after %d failed probe(s) a = %s, want available", i, m.Status("a"))
		}
	}
	m.ProbeOnce(ctx, time.Second)
	if m.Status("a") != Unreachable {
		t.Fatalf("after %d failed probes a = %s, want unreachable", FailuresToUnreachable, m.Status("a"))
	}
	down.Store(false)
	m.ProbeOnce(ctx, time.Second)
	if m.Status("a") != Available {
		t.Fatalf("after one answer a = %s, want available", m.Status("a"))
	}
	down.Store(true)
	m.ProbeOnce(ctx, time.Second)
	if m.Status("a") != Available {
		t.Fatalf("the count restarts after an answer: a = %s, want available", m.Status("a"))
	}
}

func TestSetProbesKeepsFailures(t *testing.T) {
	m := NewMonitor(openStore(t), map[string]Prober{
		"a": func(ctx context.Context) error { return errors.New("down") },
		"c": func(ctx context.Context) error { return nil },
	}, nil)
	for i := 0; i < FailuresToUnreachable-1; i++ {
		m.ProbeOnce(context.Background(), time.Second)
	}
	var bProbed atomic.Int32
	m.SetProbes(map[string]Prober{
		"a": func(ctx context.Context) error { return errors.New("down") },
		"b": func(ctx context.Context) error { bProbed.Add(1); return errors.New("down") },
	})
	m.ProbeOnce(context.Background(), time.Second)
	if m.Status("a") != Unreachable {
		t.Fatal("site a lost its failure count across SetProbes")
	}
	if m.Status("b") != Available || bProbed.Load() != 1 {
		t.Fatalf("site b: %s after %d probe(s), want a fresh count", m.Status("b"), bProbed.Load())
	}
	if m.Status("c") != Unreachable {
		t.Fatal("a dropped site is still reported reachable")
	}
}

// scripted is a prober answering errs in turn, then nil.
func scripted(errs ...error) Prober {
	var i atomic.Int32
	return func(context.Context) error {
		n := int(i.Add(1)) - 1
		if n < len(errs) {
			return errs[n]
		}
		return nil
	}
}

var keyRefused = &IncompleteError{Reason: "the service account's key is refused (401)"}

func TestOneKeyRefusalIsNotIncomplete(t *testing.T) {
	m := NewMonitor(openStore(t), map[string]Prober{"a": scripted(keyRefused)}, nil)
	m.ProbeOnce(context.Background(), time.Second)
	if m.Status("a") != Available {
		t.Fatalf("one 401: %s", m.Status("a"))
	}
	m.ProbeOnce(context.Background(), time.Second)
	if m.Status("a") != Available {
		t.Fatalf("after a success: %s", m.Status("a"))
	}
}

func TestThreeKeyRefusalsMakeIncomplete(t *testing.T) {
	m := NewMonitor(openStore(t), map[string]Prober{"a": scripted(keyRefused, keyRefused, keyRefused, keyRefused)}, nil)
	for i := 0; i < FailuresToUnreachable; i++ {
		m.ProbeOnce(context.Background(), time.Second)
	}
	if d := m.Detail("a"); d.Status != Incomplete || d.Reason != keyRefused.Reason {
		t.Fatalf("three 401s: %+v", d)
	}
}

func TestTrustMissingIsIncompleteAtOnce(t *testing.T) {
	m := NewMonitor(openStore(t), map[string]Prober{"a": scripted()}, nil)
	m.SetTrustChecks(map[string]Prober{"a": scripted(&IncompleteError{Reason: "the identity provider is missing in Prism Central", Immediate: true})})
	m.CheckTrust(context.Background(), time.Second)
	if d := m.Detail("a"); d.Status != Incomplete || !strings.Contains(d.Reason, "identity provider") {
		t.Fatalf("trust missing: %+v", d)
	}
	m.CheckTrust(context.Background(), time.Second)
	if m.Status("a") != Available {
		t.Fatalf("trust back: %s", m.Status("a"))
	}
}

func TestTrustCheckNetworkErrorChangesNothing(t *testing.T) {
	m := NewMonitor(openStore(t), map[string]Prober{"a": scripted()}, nil)
	m.SetTrustChecks(map[string]Prober{"a": scripted(errors.New("504 upstream request timeout"))})
	m.CheckTrust(context.Background(), time.Second)
	if m.Status("a") != Available {
		t.Fatalf("a slow trust read made the site %s", m.Status("a"))
	}
}

func TestCertChangedAtOnce(t *testing.T) {
	m := NewMonitor(openStore(t), map[string]Prober{"a": scripted(&CertChangedError{Fingerprint: "AB:CD"})}, nil)
	m.ProbeOnce(context.Background(), time.Second)
	if d := m.Detail("a"); d.Status != Unreachable || d.Reason != "certificate changed" || d.NewFingerprint != "AB:CD" {
		t.Fatalf("certificate changed: %+v", d)
	}
}

func TestCertChangedSurvivesATimeout(t *testing.T) {
	m := NewMonitor(openStore(t), map[string]Prober{"a": scripted(&CertChangedError{Fingerprint: "AB:CD"}, errors.New("dial tcp: i/o timeout"))}, nil)
	m.ProbeOnce(context.Background(), time.Second)
	m.ProbeOnce(context.Background(), time.Second)
	if d := m.Detail("a"); d.Reason != "certificate changed" || d.NewFingerprint != "AB:CD" {
		t.Fatalf("a timeout cleared the certificate change: %+v", d)
	}
}

func TestMaintenanceWinsOverIncomplete(t *testing.T) {
	m := NewMonitor(openStore(t), map[string]Prober{"a": scripted()}, nil)
	m.SetTrustChecks(map[string]Prober{"a": scripted(&IncompleteError{Reason: "x", Immediate: true})})
	m.CheckTrust(context.Background(), time.Second)
	if err := m.SetMaintenance(context.Background(), "a", true); err != nil {
		t.Fatal(err)
	}
	if m.Status("a") != Maintenance {
		t.Fatalf("status %s", m.Status("a"))
	}
}

func TestReportUpdatesAtOnce(t *testing.T) {
	m := NewMonitor(openStore(t), map[string]Prober{"a": scripted()}, nil)
	m.Report("a", &IncompleteError{Reason: "the Keycloak client is missing", Immediate: true})
	if m.Status("a") != Incomplete {
		t.Fatalf("after Report: %s", m.Status("a"))
	}
	m.Report("a", nil)
	if m.Status("a") != Available {
		t.Fatalf("after a clean Report: %s", m.Status("a"))
	}
}

func TestTrustCheckKeyRefusalIsNotImmediate(t *testing.T) {
	m := NewMonitor(openStore(t), map[string]Prober{"a": scripted()}, nil)
	m.SetTrustChecks(map[string]Prober{"a": scripted(keyRefused)})
	m.CheckTrust(context.Background(), time.Second)
	if m.Status("a") != Available {
		t.Fatalf("one refused key from a trust check: %s", m.Status("a"))
	}
}
