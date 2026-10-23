package proxy

import (
	"net/http"
	"sort"
	"sync/atomic"
)

// BackendSet holds the sites' backends and is replaced as a whole when a
// site changes (sites usability design 4.9). Readers get the map that was
// current when they asked: a request in flight finishes on the backend it
// started on.
type BackendSet struct {
	m atomic.Pointer[map[string]*SiteBackend]
}

// NewBackendSet returns a set over a copy of m.
func NewBackendSet(m map[string]*SiteBackend) *BackendSet {
	s := &BackendSet{}
	s.Replace(m)
	return s
}

// Replace swaps in a copy of m.
func (s *BackendSet) Replace(m map[string]*SiteBackend) {
	c := make(map[string]*SiteBackend, len(m))
	for k, v := range m {
		c[k] = v
	}
	s.m.Store(&c)
}

// Get returns a site's backend.
func (s *BackendSet) Get(id string) (*SiteBackend, bool) {
	b, ok := (*s.m.Load())[id]
	return b, ok
}

// All returns the backends sorted by order, then code.
func (s *BackendSet) All() []*SiteBackend {
	m := *s.m.Load()
	out := make([]*SiteBackend, 0, len(m))
	for _, b := range m {
		out = append(out, b)
	}
	sortBackends(out)
	return out
}

func sortBackends(bs []*SiteBackend) {
	sort.SliceStable(bs, func(i, j int) bool {
		if bs[i].Order != bs[j].Order {
			return bs[i].Order < bs[j].Order
		}
		return bs[i].Code < bs[j].Code
	})
}

// SwapHandler is a handler that can be replaced while it serves: the
// pass-through follows its site when the site changes.
type SwapHandler struct {
	h atomic.Pointer[http.Handler]
}

// NewSwapHandler returns a handler serving h.
func NewSwapHandler(h http.Handler) *SwapHandler {
	s := &SwapHandler{}
	s.Swap(h)
	return s
}

// Swap replaces the handler; requests already served by the old one finish there.
func (s *SwapHandler) Swap(h http.Handler) { s.h.Store(&h) }

func (s *SwapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*s.h.Load()).ServeHTTP(w, r)
}
