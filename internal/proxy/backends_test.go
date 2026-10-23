package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/sites"
)

func TestBackendSetReplaceConcurrent(t *testing.T) {
	a := &SiteBackend{ID: "site-a"}
	set := NewBackendSet(map[string]*SiteBackend{"site-a": a, "site-b": {ID: "site-b"}})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if b, ok := set.Get("site-a"); !ok || b == nil {
					t.Error("site-a missing during a replace that keeps it")
					return
				}
				_ = set.All()
			}
		}()
	}
	for i := 0; i < 200; i++ {
		if i%2 == 0 {
			set.Replace(map[string]*SiteBackend{"site-a": a})
		} else {
			set.Replace(map[string]*SiteBackend{"site-a": a, "site-c": {ID: "site-c"}})
		}
	}
	close(stop)
	wg.Wait()
}

func TestBackendSetAllSorted(t *testing.T) {
	set := NewBackendSet(map[string]*SiteBackend{
		"b": {ID: "b", Code: "B", Order: 2}, "a": {ID: "a", Code: "A", Order: 1}, "c": {ID: "c", Code: "C", Order: 1},
	})
	got := set.All()
	if len(got) != 3 || got[0].ID != "a" || got[1].ID != "c" || got[2].ID != "b" {
		t.Fatalf("order = %v %v %v", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestInFlightRequestFinishesOnOldBackend(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.Write([]byte("old backend"))
	}))
	defer up.Close()
	h := newSitesHarness(t)
	h.routing.Backends.Replace(map[string]*SiteBackend{
		"site-a": {ID: "site-a", Code: "AZ-IT-MI1", Order: 1, Upstream: upstreamFor(t, up.URL, "prism.test")},
	})
	h.session("s1", "solo", "site-a")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- h.do("GET", "/api/x", "s1", nil, nil) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not reach the upstream")
	}
	h.routing.Backends.Replace(map[string]*SiteBackend{})
	close(release)
	rec := <-done
	if rec.Code != 200 || rec.Body.String() != "old backend" {
		t.Fatalf("in-flight request: %d %q", rec.Code, rec.Body.String())
	}
}

func TestSessionOnRemovedSiteGetsNotAvailable(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "solo", "site-a")
	h.mu.Lock()
	h.status["site-a"] = sites.Unreachable // the monitor no longer probes it
	h.mu.Unlock()
	h.routing.Backends.Replace(map[string]*SiteBackend{"site-b": mustGet(t, h, "site-b")})
	rec := h.do("GET", "/console/", "s1", nav, nil)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "unreachable") || len(*h.a) != 0 {
		t.Fatalf("navigation on a removed site: %d upstream=%d\n%s", rec.Code, len(*h.a), rec.Body.String())
	}
	rec = h.do("GET", "/api/x", "s1", nil, nil)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "site unreachable") || len(*h.a) != 0 {
		t.Fatalf("XHR on a removed site: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRemovedSiteEvenIfReportedAvailableRoutesNowhere(t *testing.T) {
	h := newSitesHarness(t)
	h.session("s1", "solo", "site-a")
	h.routing.Backends.Replace(map[string]*SiteBackend{"site-b": mustGet(t, h, "site-b")})
	rec := h.do("GET", "/api/x", "s1", nil, nil)
	if rec.Code != 503 || len(*h.a) != 0 {
		t.Fatalf("a site without a backend was routed: %d %s", rec.Code, rec.Body.String())
	}
}

func mustGet(t *testing.T, h *sitesHarness, id string) *SiteBackend {
	t.Helper()
	b, ok := h.routing.Backends.Get(id)
	if !ok {
		t.Fatalf("no backend %s", id)
	}
	return b
}

func TestSwapHandler(t *testing.T) {
	sw := NewSwapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("one")) }))
	rec := httptest.NewRecorder()
	sw.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	sw.Swap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("two")) }))
	rec2 := httptest.NewRecorder()
	sw.ServeHTTP(rec2, httptest.NewRequest("GET", "/", nil))
	if rec.Body.String() != "one" || rec2.Body.String() != "two" {
		t.Fatalf("got %q then %q", rec.Body.String(), rec2.Body.String())
	}
}
