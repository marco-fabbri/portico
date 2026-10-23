package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

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
	log       *slog.Logger
}

// siteParts is one served site, built from its settings.
type siteParts struct {
	fp      string
	pc      *nutanix.Client
	tenant  tenant.Site
	probe   sites.Prober
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
}

func newSiteRuntime(deps siteDeps, monitor *sites.Monitor, tenants *tenant.Service) *siteRuntime {
	if deps.log == nil {
		deps.log = slog.Default()
	}
	unset := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.Refuse(w, http.StatusServiceUnavailable, "no-site", "no Prism Central configured")
	})
	return &siteRuntime{deps: deps, backends: proxy.NewBackendSet(nil), passthrough: proxy.NewSwapHandler(unset),
		monitor: monitor, tenants: tenants, parts: map[string]*siteParts{}, filled: map[string]bool{}}
}

// siteFingerprint changes when anything the parts are built from changes.
func siteFingerprint(s settings.Site) string {
	b, _ := json.Marshal([]any{s.URL, string(s.APIKey), s.TLSVerify, s.TLSPin, s.CAPEM, s.SAMLIdPName, s.SAMLIdPExtID,
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
		if site.SAMLIdPExtID == "" && site.SAMLIdPName != "" && !r.filled[site.ID] {
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
	probes := map[string]sites.Prober{}
	var tenantSites []tenant.Site
	for _, site := range doc.OrderedSites() {
		p, ok := next[site.ID]
		if !ok {
			continue
		}
		backends[site.ID], probes[site.ID] = p.backend, p.probe
		tenantSites = append(tenantSites, p.tenant)
	}
	r.backends.Replace(backends)
	r.monitor.SetProbes(probes)
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
			return err
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
		r.deps.log.Warn("identity provider id not stored: lookup failed, retried at the next reload", "site", site.ID, "err", err.Error())
		r.mu.Lock()
		delete(r.filled, site.ID)
		r.mu.Unlock()
		return
	}
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
