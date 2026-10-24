package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/proxy"
	"github.com/marco-fabbri/portico/internal/secrets"
	"github.com/marco-fabbri/portico/internal/settings"
	"github.com/marco-fabbri/portico/internal/settings/settingstest"
	"github.com/marco-fabbri/portico/internal/sites"
	"github.com/marco-fabbri/portico/internal/store"
	"github.com/marco-fabbri/portico/internal/tenant"
)

type runtimeEnv struct {
	rt       *siteRuntime
	svc      *settings.Service
	tenants  *tenant.Service
	monitor  *sites.Monitor
	pcA, pcB *settingstest.PC
}

func newRuntimeEnv(t *testing.T) *runtimeEnv {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sealer, _ := secrets.NewSealer(bytes.Repeat([]byte{7}, secrets.KeySize))
	e := &runtimeEnv{svc: settings.NewService(st, sealer), pcA: settingstest.NewPC(t), pcB: settingstest.NewPC(t)}
	e.tenants = tenant.NewService(st, nil, nil, tenant.Config{}, nil)
	e.monitor = sites.NewMonitor(st, nil, nil)
	e.rt = newSiteRuntime(siteDeps{prismHost: "prism.test", settings: e.svc}, e.monitor, e.tenants)
	return e
}

func (e *runtimeEnv) site(id, code string, pc *settingstest.PC) settings.Site {
	return settings.Site{ID: id, Code: code, Name: code, URL: pc.Srv.URL, APIKey: settings.Secret(pc.Key), SAMLIdPName: "portico-keycloak", SAMLIdPExtID: "idp-portico-keycloak"}
}

func TestRuntimeApplyAddsSiteWithoutRestart(t *testing.T) {
	e := newRuntimeEnv(t)
	doc := settings.Settings{Sites: []settings.Site{e.site("site-a", "SITE-A", e.pcA)}}
	e.rt.Apply(context.Background(), doc)
	if _, ok := e.rt.backends.Get("site-b"); ok {
		t.Fatal("site-b served before it exists")
	}
	doc.Sites = append(doc.Sites, e.site("site-b", "SITE-B", e.pcB))
	e.rt.Apply(context.Background(), doc)
	b, ok := e.rt.backends.Get("site-b")
	if !ok {
		t.Fatal("site-b not served after Apply")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/clustermgmt/v4.0/config/clusters", nil)
	req.Header.Set("X-Ntnx-Api-Key", e.pcB.Key)
	b.Upstream.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("site-b's upstream: %d %s", rec.Code, rec.Body.String())
	}
	if got := e.tenants.Sites(); len(got) != 2 || got[1].ID != "site-b" {
		t.Fatalf("tenant sites = %+v", got)
	}
	e.monitor.ProbeOnce(context.Background(), time.Second)
	if e.monitor.Status("site-b") != sites.Available {
		t.Fatalf("site-b status %s", e.monitor.Status("site-b"))
	}
	doc.Sites = doc.Sites[:1]
	e.rt.Apply(context.Background(), doc)
	if _, ok := e.rt.backends.Get("site-b"); ok || len(e.tenants.Sites()) != 1 || e.monitor.Status("site-b") != sites.Unreachable {
		t.Fatal("site-b still served after it was removed")
	}
}

func TestRuntimeApplyRebuildsOnlyChanged(t *testing.T) {
	e := newRuntimeEnv(t)
	doc := settings.Settings{Sites: []settings.Site{e.site("site-a", "SITE-A", e.pcA), e.site("site-b", "SITE-B", e.pcB)}}
	e.rt.Apply(context.Background(), doc)
	a1, _ := e.rt.backends.Get("site-a")
	b1, _ := e.rt.backends.Get("site-b")
	doc.Sites[1].Name = "New York"
	e.rt.Apply(context.Background(), doc)
	a2, _ := e.rt.backends.Get("site-a")
	b2, _ := e.rt.backends.Get("site-b")
	if a1 != a2 {
		t.Fatal("an unchanged site was rebuilt")
	}
	if b1 == b2 || b2.Name != "New York" {
		t.Fatal("the renamed site was not rebuilt")
	}
}

func TestRuntimeBackfillsIdPExtID(t *testing.T) {
	e := newRuntimeEnv(t)
	e.pcA.AddIdP("portico-keycloak")
	site := e.site("site-a", "SITE-A", e.pcA)
	site.SAMLIdPExtID = ""
	if err := e.svc.Set(context.Background(), settings.Settings{Sites: []settings.Site{site}}); err != nil {
		t.Fatal(err)
	}
	doc, _ := e.svc.Get(context.Background())
	e.rt.Apply(context.Background(), doc)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := e.svc.Get(context.Background())
		if s, _ := got.Site("site-a"); s.SAMLIdPExtID == "idp-portico-keycloak" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the identity provider id was not stored")
}

func TestRuntimePassthroughFollowsSite(t *testing.T) {
	e := newRuntimeEnv(t)
	doc := settings.Settings{Sites: []settings.Site{e.site("site-a", "SITE-A", e.pcA)}}
	e.rt.Apply(context.Background(), doc)
	call := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/clustermgmt/v4.0/config/clusters", nil)
		req.Header.Set("X-Ntnx-Api-Key", e.pcA.Key)
		e.rt.passthrough.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := call(); c != 200 {
		t.Fatalf("pass-through to site-a: %d", c)
	}
	e.pcB.Key = "another-key"
	doc.Sites[0].URL, doc.Sites[0].APIKey = e.pcB.Srv.URL, settings.Secret(e.pcB.Key)
	e.rt.Apply(context.Background(), doc)
	// The pass-through keeps the caller's credentials: site-a's key now
	// reaches site-b's Prism Central, which refuses it.
	if c := call(); c != http.StatusUnauthorized {
		t.Fatalf("pass-through after the move: %d, want site-b's 401", c)
	}
}

func TestRuntimeProbeCountsRefusedKey(t *testing.T) {
	e := newRuntimeEnv(t)
	s := e.site("site-a", "SITE-A", e.pcA)
	s.APIKey = "revoked-key"
	e.rt.Apply(context.Background(), settings.Settings{Sites: []settings.Site{s}})
	e.monitor.ProbeOnce(context.Background(), time.Second)
	if st := e.monitor.Status("site-a"); st != sites.Available {
		t.Fatalf("one refusal: %s", st)
	}
	e.monitor.ProbeOnce(context.Background(), time.Second)
	e.monitor.ProbeOnce(context.Background(), time.Second)
	if d := e.monitor.Detail("site-a"); d.Status != sites.Incomplete || !strings.Contains(d.Reason, "refuses the service account's key (401)") {
		t.Fatalf("three refusals: %+v", d)
	}
}

func TestRuntimeProbeStopsOnChangedCertificate(t *testing.T) {
	e := newRuntimeEnv(t)
	srv := httptest.NewTLSServer(e.pcA.Srv.Config.Handler)
	t.Cleanup(srv.Close)
	presented := proxy.Fingerprint(srv.Certificate().Raw)
	s := e.site("site-a", "SITE-A", e.pcA)
	s.URL = srv.URL
	s.TLSPin = proxy.Fingerprint(append([]byte{0}, srv.Certificate().Raw...)) // the leaf it had before
	e.rt.Apply(context.Background(), settings.Settings{Sites: []settings.Site{s}})
	e.monitor.ProbeOnce(context.Background(), time.Second)
	if d := e.monitor.Detail("site-a"); d.Status != sites.Unreachable || d.Reason != "certificate changed" || d.NewFingerprint != presented {
		t.Fatalf("after one probe: %+v", d)
	}
}

func TestRuntimeTrustCheckGatesNewEntries(t *testing.T) {
	e := newRuntimeEnv(t)
	s := e.site("site-a", "SITE-A", e.pcA)
	s.SAMLIdPExtID = "idp-gone" // the fake Prism Central has no such provider
	s.ClusterExtID, s.StorageContainerExtID = "cl-1", "sc-1"
	e.rt.Apply(context.Background(), settings.Settings{Sites: []settings.Site{s}})
	e.monitor.ProbeOnce(context.Background(), time.Second)
	e.monitor.CheckTrust(context.Background(), time.Second)
	if d := e.monitor.Detail("site-a"); d.Status != sites.Incomplete || !strings.Contains(d.Reason, "missing in Prism Central") {
		t.Fatalf("trust missing: %+v", d)
	}
	_, err := e.tenants.Create(context.Background(), tenant.CreateRequest{Slug: "acme", DisplayName: "ACME", Sites: []tenant.PlacementRequest{{Site: "site-a"}}})
	if !errors.Is(err, tenant.ErrSiteIncomplete) || !strings.Contains(err.Error(), "finish the setup of SITE-A first") {
		t.Fatalf("create on an incomplete site: %v", err)
	}
}

func TestRuntimeRebuildClosesIdleConnections(t *testing.T) {
	e := newRuntimeEnv(t)
	doc := settings.Settings{Sites: []settings.Site{e.site("site-a", "SITE-A", e.pcA)}}
	e.rt.Apply(context.Background(), doc)
	e.monitor.ProbeOnce(context.Background(), time.Second) // leaves a kept-alive connection
	before := e.pcA.ClosedConns()
	doc.Sites[0].Name = "Milan"
	e.rt.Apply(context.Background(), doc)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e.pcA.ClosedConns() > before {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the rebuilt site's old connections stay open")
}
