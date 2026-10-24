package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/marco-fabbri/portico/internal/idp/keycloak"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/proxy"
	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/sites"
	"github.com/marco-fabbri/portico/internal/tenant"
)

// sitesReload is how often the running document is read again, so the
// other node of a pair (design 4.11) picks up a site change made on this
// one within that time; on this node a save applies at once.
const sitesReload = 30 * time.Second

// siteDeps is what the runtime needs to build a site's parts.
type siteDeps struct {
	prismHost string
	strip     []string
	errorPage func(w http.ResponseWriter, r *http.Request, status int, title, text, retry string)
	settings  *settings.Service // backfills a site's identity provider id
	kc        *keycloak.Client  // the trust check reads the site's Keycloak client
	log       *slog.Logger
}

// siteParts is one served site, built from its settings.
type siteParts struct {
	fp      string
	pc      *nutanix.Client
	tenant  tenant.Site
	probe   sites.Prober
	trust   sites.Prober
	backend *proxy.SiteBackend
	idle    []func() // close the idle connections of the site's clients
}

// siteRuntime builds each served site's Prism Central client, upstream,
// probe and tenant entry from the settings document and swaps them in
// when the document changes (sites usability design 4.9). Requests in
// flight finish on the parts they started on.
type siteRuntime struct {
	deps        siteDeps
	backends    *proxy.BackendSet
	passthrough *proxy.SwapHandler
	monitor     *sites.Monitor
	tenants     *tenant.Service

	mu     sync.Mutex
	parts  map[string]*siteParts
	ptFP   string
	ptIdle func()
	filled map[string]bool // ids whose identity provider id is being backfilled
	warned map[string]bool // ids whose failed lookup was logged as a warning
}

func newSiteRuntime(deps siteDeps, monitor *sites.Monitor, tenants *tenant.Service) *siteRuntime {
	if deps.log == nil {
		deps.log = slog.Default()
	}
	unset := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.Refuse(w, http.StatusServiceUnavailable, "no-site", "no Prism Central configured")
	})
	r := &siteRuntime{deps: deps, backends: proxy.NewBackendSet(nil), passthrough: proxy.NewSwapHandler(unset),
		monitor: monitor, tenants: tenants, parts: map[string]*siteParts{}, filled: map[string]bool{}, warned: map[string]bool{}}
	tenants.SetSiteGate(r.gate)
	return r
}

// gate refuses a new tenant entry on an incomplete site (sites usability
// design 4.2); the tenants already there keep working.
func (r *siteRuntime) gate(id string) error {
	if r.monitor.Status(id) != sites.Incomplete {
		return nil
	}
	code := id
	if b, ok := r.backends.Get(id); ok {
		code = b.Code
	}
	return fmt.Errorf("%w: finish the setup of %s first", tenant.ErrSiteIncomplete, code)
}

// probeError is a probe's error as the monitor counts it: a certificate
// other than the pinned one stops the site at once; a refused key counts
// towards incomplete; anything else is a failed probe.
func probeError(err error) error {
	var pin *proxy.PinMismatchError
	var apiErr *nutanix.APIError
	switch {
	case errors.As(err, &pin):
		return &sites.CertChangedError{Fingerprint: pin.Got}
	case errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden):
		return &sites.IncompleteError{Reason: fmt.Sprintf("Prism Central refuses the service account's key (%d)", apiErr.Status)}
	}
	return err
}

// siteFingerprint changes when anything the parts are built from changes.
func siteFingerprint(s settings.Site) string {
	b, _ := json.Marshal([]any{s.URL, string(s.APIKey), s.TLSVerify, s.TLSPin, s.CAPEM, s.SAMLIdPName, s.SAMLIdPExtID, s.SAMLEntityID,
		s.ClusterExtID, s.StorageContainerExtID, s.Code, s.Name, s.Order})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Apply serves the document's sites: a site whose settings did not change
// keeps its parts; the others are built again; then backends, probes,
// tenant sites and the pass-through are swapped in, in that order. A site
// that cannot be built is logged and not served.
func (r *siteRuntime) Apply(ctx context.Context, doc settings.Settings) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := map[string]*siteParts{}
	var replaced []*siteParts
	var backfill []settings.Site
	for _, site := range doc.ServedSites() {
		fp := siteFingerprint(site)
		if old, ok := r.parts[site.ID]; ok && old.fp == fp {
			next[site.ID] = old
			// A lookup that failed is tried again at every reload.
			if r.needsBackfill(site) {
				backfill = append(backfill, site)
			}
			continue
		}
		p, err := r.build(site)
		if err != nil {
			r.deps.log.Error("site not served: its settings cannot be used", "site", site.ID, "err", err.Error())
			continue
		}
		if old, ok := r.parts[site.ID]; ok {
			replaced = append(replaced, old)
			r.deps.log.Info("site rebuilt", "site", site.ID)
		} else {
			r.deps.log.Info("site served", "site", site.ID)
		}
		next[site.ID] = p
		if r.needsBackfill(site) {
			backfill = append(backfill, site)
		}
	}
	for id, old := range r.parts {
		if _, ok := next[id]; !ok {
			replaced = append(replaced, old)
			r.deps.log.Info("site no longer served", "site", id)
		}
	}
	for _, inc := range doc.IncompleteSites() {
		if _, was := r.parts[inc]; was || len(r.parts) == 0 {
			r.deps.log.Warn("site not served until it is complete", "site", inc)
		}
	}

	backends := map[string]*proxy.SiteBackend{}
	probes, trust := map[string]sites.Prober{}, map[string]sites.Prober{}
	var tenantSites []tenant.Site
	for _, site := range doc.OrderedSites() {
		p, ok := next[site.ID]
		if !ok {
			continue
		}
		backends[site.ID], probes[site.ID], trust[site.ID] = p.backend, p.probe, p.trust
		tenantSites = append(tenantSites, p.tenant)
	}
	r.backends.Replace(backends)
	r.monitor.SetProbes(probes)
	r.monitor.SetTrustChecks(trust)
	r.tenants.SetSites(tenantSites)
	r.parts = next
	r.applyPassthrough(doc)
	for _, old := range replaced {
		for _, f := range old.idle {
			f()
		}
	}
	for _, site := range backfill {
		r.filled[site.ID] = true
		go r.backfillIdP(site)
	}
	return nil
}

// build makes a site's parts from its settings.
func (r *siteRuntime) build(site settings.Site) (*siteParts, error) {
	if site.TLSPin == "" && !site.TLSVerify {
		r.deps.log.Warn("tls_verify is false: the certificate of this Prism Central is not checked; pin it on the site page", "site", site.ID)
	}
	siteURL, siteTLS, err := siteTarget(site)
	if err != nil {
		return nil, err
	}
	c, err := nutanix.New(site.URL, string(site.APIKey), siteTLS, r.deps.log)
	if err != nil {
		return nil, err
	}
	up := proxy.NewUpstream(proxy.UpstreamOptions{Target: siteURL, HostHeader: r.deps.prismHost, TLS: siteTLS, StripCookies: r.deps.strip, Log: r.deps.log, ErrorPage: r.deps.errorPage})
	landingTr := &http.Transport{TLSClientConfig: siteTLS, ForceAttemptHTTP2: false}
	p := &siteParts{
		fp: siteFingerprint(site),
		pc: c,
		tenant: tenant.Site{ID: site.ID, Code: site.Code, Name: site.Name, URL: site.URL, PC: c, SAMLIdPName: site.SAMLIdPName, SAMLIdPExtID: site.SAMLIdPExtID,
			ClusterExtID: site.ClusterExtID, StorageContainerExtID: site.StorageContainerExtID},
		probe: func(ctx context.Context) error {
			_, err := c.PrismCentralVersion(ctx)
			return probeError(err)
		},
		// The trust check is the status card's check (design 4.2): what
		// Prism Central and Keycloak hold now, never Portico's records.
		trust: func(ctx context.Context) error {
			return settings.SiteStatus(ctx, r.deps.kc, site, r.deps.log).MonitorError()
		},
		backend: &proxy.SiteBackend{ID: site.ID, Code: site.Code, Name: site.Name, Order: site.Order,
			Upstream: up,
			PrismHTTP: &http.Client{
				Transport:     landingTr,
				Timeout:       30 * time.Second,
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			},
			PrismBase: &url.URL{Scheme: siteURL.Scheme, Host: siteURL.Host},
			IdPExtID:  idpLookup(c, site),
		},
	}
	p.idle = []func(){c.CloseIdleConnections, landingTr.CloseIdleConnections, closeIdle(up.Transport)}
	return p, nil
}

// applyPassthrough points the internal pass-through at the document's
// pass-through site (design sites §4.6) when its address or trust changed.
func (r *siteRuntime) applyPassthrough(doc settings.Settings) {
	site, ok := doc.PassthroughSiteOrFirst()
	if !ok {
		return
	}
	b, _ := json.Marshal([]any{site.ID, site.URL, site.TLSVerify, site.TLSPin, site.CAPEM})
	fp := string(b)
	if fp == r.ptFP {
		return
	}
	u, t, err := siteTarget(site)
	if err != nil {
		r.deps.log.Error("pass-through not changed: the site's settings cannot be used", "site", site.ID, "err", err.Error())
		return
	}
	up := proxy.NewUpstream(proxy.UpstreamOptions{Target: u, HostHeader: "", TLS: t, Log: r.deps.log})
	r.passthrough.Swap(proxy.NewPassthrough(up, r.deps.log))
	if r.ptIdle != nil {
		r.ptIdle()
	}
	r.ptFP, r.ptIdle = fp, closeIdle(up.Transport)
}

// backfillIdP stores the identity provider id of a site set up before
// it was stored (sites usability design 4.8). It runs outside the save
// that triggered Apply: settings hooks hold the settings lock.
func (r *siteRuntime) backfillIdP(site settings.Site) {
	r.mu.Lock()
	p := r.parts[site.ID]
	r.mu.Unlock()
	if p == nil || r.deps.settings == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, err := idpLookup(p.pc, site)(ctx)
	if err != nil {
		r.mu.Lock()
		delete(r.filled, site.ID)
		first := !r.warned[site.ID]
		r.warned[site.ID] = true
		r.mu.Unlock()
		if first {
			r.deps.log.Warn("identity provider id not stored: lookup failed, retried at every reload", "site", site.ID, "err", err.Error())
		} else {
			r.deps.log.Debug("identity provider id not stored: lookup failed again", "site", site.ID, "err", err.Error())
		}
		return
	}
	r.mu.Lock()
	delete(r.warned, site.ID)
	r.mu.Unlock()
	err = r.deps.settings.Patch(ctx, func(d *settings.Settings) {
		for i := range d.Sites {
			if d.Sites[i].ID == site.ID && d.Sites[i].SAMLIdPExtID == "" && d.Sites[i].SAMLIdPName == site.SAMLIdPName {
				d.Sites[i].SAMLIdPExtID = id
			}
		}
	})
	if err != nil {
		r.deps.log.Warn("identity provider id not stored", "site", site.ID, "err", err.Error())
		return
	}
	r.deps.log.Info("identity provider id stored", "site", site.ID)
}

// needsBackfill reports whether a site set up before its identity
// provider id was stored needs the lookup, and none runs; under r.mu.
func (r *siteRuntime) needsBackfill(site settings.Site) bool {
	return site.SAMLIdPExtID == "" && site.SAMLIdPName != "" && !r.filled[site.ID]
}

// reload reads the stored document every interval and applies it, for a
// change made on the pair's other node.
func (r *siteRuntime) reload(ctx context.Context, svc *settings.Service, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !svc.Stored(ctx) {
				continue
			}
			doc, err := svc.Get(ctx)
			if err != nil {
				r.deps.log.Warn("sites not reloaded", "err", err.Error())
				continue
			}
			r.Apply(ctx, doc)
		}
	}
}

func closeIdle(rt http.RoundTripper) func() {
	return func() {
		if c, ok := rt.(interface{ CloseIdleConnections() }); ok {
			c.CloseIdleConnections()
		}
	}
}
