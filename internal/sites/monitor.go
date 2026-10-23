// Package sites keeps the runtime state of the sites (design sites §4.1):
// whether each Prism Central answers, which sites the MSP put in
// maintenance, and which sites a tenant may be routed to. None of it
// needs a restart: it changes while Portico runs.
package sites

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/marco-fabbri/portico/internal/store"
)

// Status is what the site page and the admin pages show for a site.
type Status string

const (
	Available   Status = "available"
	Maintenance Status = "maintenance"
	Unreachable Status = "unreachable"
)

// FailuresToUnreachable is how many failed probes in a row make a site
// unreachable; one answer makes it available again (write-up 21: one
// slow answer must not lock a Prism Central's tenants out).
const FailuresToUnreachable = 3

// StateKey is the store setting holding the maintenance flags:
// {"maintenance":{"<site-id>":true}}.
const StateKey = "site_state"

// Prober checks that a site's Prism Central answers.
type Prober func(ctx context.Context) error

type state struct {
	Maintenance map[string]bool `json:"maintenance"`
}

// Monitor probes the sites and holds their maintenance flags.
type Monitor struct {
	st     store.Store
	probes map[string]Prober
	log    *slog.Logger

	mu          sync.RWMutex
	failures    map[string]int // failed probes in a row; absent before the first probe
	maintenance map[string]bool
}

// NewMonitor returns a monitor over the sites' probers.
func NewMonitor(st store.Store, probes map[string]Prober, log *slog.Logger) *Monitor {
	if log == nil {
		log = slog.Default()
	}
	return &Monitor{st: st, probes: probes, log: log, failures: map[string]int{}, maintenance: map[string]bool{}}
}

// Load reads the maintenance flags from the store.
func (m *Monitor) Load(ctx context.Context) error {
	raw, err := m.st.GetSetting(ctx, StateKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var s state
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maintenance = map[string]bool{}
	for id, on := range s.Maintenance {
		if on {
			m.maintenance[id] = true
		}
	}
	return nil
}

// Status says what a site is now: maintenance wins over the probe; a
// site is unreachable after FailuresToUnreachable failed probes in a row
// and counts as available before that; a site without a prober (not
// configured in this process) is unreachable.
func (m *Monitor) Status(siteID string) Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.maintenance[siteID] {
		return Maintenance
	}
	if _, known := m.probes[siteID]; !known {
		return Unreachable
	}
	if m.failures[siteID] >= FailuresToUnreachable {
		return Unreachable
	}
	return Available
}

// SetProbes replaces the probed sites (sites usability design 4.9): a
// site kept keeps its failure count, a new one starts from none, a
// dropped one is no longer probed and reports unreachable.
func (m *Monitor) SetProbes(probes map[string]Prober) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.failures {
		if _, ok := probes[id]; !ok {
			delete(m.failures, id)
		}
	}
	m.probes = probes
}

// Maintenance reports the site's maintenance flag.
func (m *Monitor) Maintenance(siteID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.maintenance[siteID]
}

// SetMaintenance sets or clears the flag and stores it; it takes effect
// at once (design sites §4.1).
func (m *Monitor) SetMaintenance(ctx context.Context, siteID string, on bool) error {
	m.mu.Lock()
	next := map[string]bool{}
	for id, v := range m.maintenance {
		next[id] = v
	}
	if on {
		next[siteID] = true
	} else {
		delete(next, siteID)
	}
	raw, err := json.Marshal(state{Maintenance: next})
	if err == nil {
		err = m.st.SetSetting(ctx, StateKey, raw)
	}
	if err == nil {
		m.maintenance = next
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.log.Warn("site maintenance changed", "site", siteID, "maintenance", on)
	return nil
}

// ProbeOnce probes every site in parallel, each bounded by timeout, and
// logs the first result of each site and every change of its
// reachability.
func (m *Monitor) ProbeOnce(ctx context.Context, timeout time.Duration) {
	m.mu.RLock()
	probes := m.probes
	m.mu.RUnlock()
	var wg sync.WaitGroup
	for id, probe := range probes {
		wg.Add(1)
		go func(id string, probe Prober) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, timeout)
			err := probe(pctx)
			cancel()
			m.mu.Lock()
			if _, kept := m.probes[id]; !kept {
				m.mu.Unlock() // dropped by SetProbes while it was probed
				return
			}
			n, probed := m.failures[id]
			was := n < FailuresToUnreachable
			if err == nil {
				n = 0
			} else {
				n++
			}
			m.failures[id] = n
			now := n < FailuresToUnreachable
			m.mu.Unlock()
			if !probed || was != now {
				attrs := []any{"site", id, "reachable", now}
				if err != nil {
					attrs = append(attrs, "failures", n, "err", err.Error())
				}
				m.log.Info("site probe", attrs...)
			}
		}(id, probe)
	}
	wg.Wait()
}

// Run probes at once, then every interval, until ctx is done.
func (m *Monitor) Run(ctx context.Context, interval, timeout time.Duration) {
	m.ProbeOnce(ctx, timeout)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.ProbeOnce(ctx, timeout)
		}
	}
}
