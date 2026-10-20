package sites

import (
	"context"
	"errors"
	"path/filepath"
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
// NTNX-01, then 0.6 s again): only FailuresToUnreachable failed probes in a
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
