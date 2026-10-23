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
	// Incomplete: Prism Central or Keycloak says the site's setup is broken
	// (key refused, SAML trust or Keycloak client missing). It takes no new
	// entries until Repair (sites usability design 4.2).
	Incomplete Status = "incomplete"
)

// TrustInterval is how often the SAML trust and the Keycloak client of
// every site are checked; the key is checked by every probe.
const TrustInterval = 5 * time.Minute

// IncompleteError is a probe or trust check finding the site's setup
// broken. Immediate (a trust or client missing) counts at once; a refused
// key counts after FailuresToUnreachable in a row (write-up 25: NTNX-01's
// IAM answered isolated 401s).
type IncompleteError struct {
	Reason    string
	Immediate bool
}

func (e *IncompleteError) Error() string { return "site incomplete: " + e.Reason }

// CertChangedError is a pinned site presenting another certificate. It
// counts at once: no retry makes the old certificate come back.
type CertChangedError struct {
	Fingerprint string
}

func (e *CertChangedError) Error() string { return "certificate changed: presented " + e.Fingerprint }

// Detail is a site's status with why.
type Detail struct {
	Status         Status    `json:"status"`
	Reason         string    `json:"reason,omitempty"`
	NewFingerprint string    `json:"new_fingerprint,omitempty"`
	CheckedAt      time.Time `json:"checked_at,omitzero"`
}

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
	st  store.Store
	log *slog.Logger

	mu          sync.RWMutex
	probes      map[string]Prober
	trust       map[string]Prober
	failures    map[string]int    // failed probes in a row; absent before the first probe
	refusals    map[string]int    // refused keys in a row
	lastReason  map[string]string // the last refusal's reason
	untrusted   map[string]string // trust or client missing: the reason
	certChanged map[string]string // the fingerprint now presented
	checkedAt   map[string]time.Time
	maintenance map[string]bool
}

// NewMonitor returns a monitor over the sites' probers.
func NewMonitor(st store.Store, probes map[string]Prober, log *slog.Logger) *Monitor {
	if log == nil {
		log = slog.Default()
	}
	return &Monitor{st: st, probes: probes, log: log, failures: map[string]int{}, refusals: map[string]int{}, lastReason: map[string]string{},
		untrusted: map[string]string{}, certChanged: map[string]string{}, checkedAt: map[string]time.Time{}, maintenance: map[string]bool{}}
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

// Status says what a site is now.
func (m *Monitor) Status(siteID string) Status { return m.Detail(siteID).Status }

// Detail says what a site is now and why, in this order: maintenance; a
// changed certificate (unreachable, at once); not probed in this process
// (unreachable); trust or Keycloak client missing (incomplete, at once);
// the key refused FailuresToUnreachable times in a row (incomplete);
// FailuresToUnreachable failed probes in a row (unreachable); otherwise
// available, also before the first probe.
func (m *Monitor) Detail(siteID string) Detail {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d := Detail{CheckedAt: m.checkedAt[siteID]}
	switch {
	case m.maintenance[siteID]:
		d.Status = Maintenance
	case m.certChanged[siteID] != "":
		d.Status, d.Reason, d.NewFingerprint = Unreachable, "certificate changed", m.certChanged[siteID]
	case m.probes[siteID] == nil:
		d.Status, d.Reason = Unreachable, "not served"
	case m.untrusted[siteID] != "":
		d.Status, d.Reason = Incomplete, m.untrusted[siteID]
	case m.refusals[siteID] >= FailuresToUnreachable:
		d.Status, d.Reason = Incomplete, m.lastReason[siteID]
	case m.failures[siteID] >= FailuresToUnreachable:
		d.Status, d.Reason = Unreachable, m.lastReason[siteID]
	default:
		d.Status = Available
	}
	return d
}

// SetProbes replaces the probed sites (sites usability design 4.9): a
// site kept keeps its failure count, a new one starts from none, a
// dropped one is no longer probed and reports unreachable.
func (m *Monitor) SetProbes(probes map[string]Prober) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, mp := range []map[string]int{m.failures, m.refusals} {
		for id := range mp {
			if _, ok := probes[id]; !ok {
				delete(mp, id)
			}
		}
	}
	for _, mp := range []map[string]string{m.untrusted, m.certChanged, m.lastReason} {
		for id := range mp {
			if _, ok := probes[id]; !ok {
				delete(mp, id)
			}
		}
	}
	m.probes = probes
}

// SetTrustChecks replaces the checks of the sites' SAML trust and
// Keycloak client, run every TrustInterval by Run and by CheckTrust.
func (m *Monitor) SetTrustChecks(checks map[string]Prober) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trust = checks
}

// CheckTrust runs every trust check once, each bounded by timeout. Only
// an IncompleteError marks a site; any other error (a slow answer) leaves
// the last result as it was.
func (m *Monitor) CheckTrust(ctx context.Context, timeout time.Duration) {
	m.mu.RLock()
	checks := m.trust
	m.mu.RUnlock()
	var wg sync.WaitGroup
	for id, check := range checks {
		wg.Add(1)
		go func(id string, check Prober) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, timeout)
			err := check(cctx)
			cancel()
			var inc *IncompleteError
			switch {
			case err == nil:
				m.setUntrusted(id, "")
			case errors.As(err, &inc) && inc.Immediate:
				m.setUntrusted(id, inc.Reason)
			default:
				// A refused key or a slow answer: the probe counts those.
				m.log.Warn("site trust not checked", "site", id, "err", err.Error())
			}
		}(id, check)
	}
	wg.Wait()
}

func (m *Monitor) setUntrusted(id, reason string) {
	m.mu.Lock()
	was := m.untrusted[id]
	if reason == "" {
		delete(m.untrusted, id)
	} else {
		m.untrusted[id] = reason
	}
	m.mu.Unlock()
	if was != reason {
		m.log.Info("site trust", "site", id, "ok", reason == "", "reason", reason)
	}
}

// Report records the outcome of an on-demand check of a site (its page's
// status card): nil clears what it covers; an IncompleteError marks the
// trust or counts a refused key; a CertChangedError marks the certificate;
// any other error counts as a failed probe.
func (m *Monitor) Report(id string, err error) {
	var inc *IncompleteError
	if errors.As(err, &inc) && inc.Immediate {
		m.setUntrusted(id, inc.Reason)
		return
	}
	if err == nil {
		m.setUntrusted(id, "")
	}
	m.record(id, err)
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

// ClearMaintenance drops a removed site's flag, so a site added later
// with the same id does not start in maintenance; nothing is written
// when the flag is not set.
func (m *Monitor) ClearMaintenance(ctx context.Context, siteID string) error {
	if !m.Maintenance(siteID) {
		return nil
	}
	return m.SetMaintenance(ctx, siteID, false)
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
			m.mu.RLock()
			_, kept := m.probes[id]
			m.mu.RUnlock()
			if kept { // not dropped by SetProbes while it was probed
				m.record(id, err)
			}
		}(id, probe)
	}
	wg.Wait()
}

// record counts one probe result and logs the first result of a site and
// every change of its status.
func (m *Monitor) record(id string, err error) {
	before := m.Detail(id)
	m.mu.Lock()
	_, probed := m.checkedAt[id]
	m.checkedAt[id] = time.Now()
	var inc *IncompleteError
	var cc *CertChangedError
	switch {
	case err == nil:
		m.failures[id], m.refusals[id] = 0, 0
		delete(m.certChanged, id)
		delete(m.lastReason, id)
	case errors.As(err, &cc):
		m.certChanged[id] = cc.Fingerprint
	case errors.As(err, &inc):
		m.refusals[id]++
		m.failures[id] = 0
		m.lastReason[id] = inc.Reason
	default:
		m.failures[id]++
		m.refusals[id] = 0
		m.lastReason[id] = err.Error()
	}
	if err != nil && cc == nil {
		delete(m.certChanged, id)
	}
	n := m.failures[id] + m.refusals[id]
	m.mu.Unlock()
	after := m.Detail(id)
	if !probed || before.Status != after.Status {
		attrs := []any{"site", id, "status", string(after.Status), "reachable", after.Status != Unreachable}
		if err != nil {
			attrs = append(attrs, "failures", n, "err", err.Error())
		}
		m.log.Info("site probe", attrs...)
	}
}

// Run probes and checks the trust at once, then probes every interval and
// checks the trust every TrustInterval, until ctx is done.
func (m *Monitor) Run(ctx context.Context, interval, timeout time.Duration) {
	m.ProbeOnce(ctx, timeout)
	m.CheckTrust(ctx, timeout)
	t := time.NewTicker(interval)
	defer t.Stop()
	tt := time.NewTicker(TrustInterval)
	defer tt.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.ProbeOnce(ctx, timeout)
		case <-tt.C:
			m.CheckTrust(ctx, timeout)
		}
	}
}
