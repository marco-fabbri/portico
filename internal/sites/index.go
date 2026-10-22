package sites

import (
	"context"
	"sync"
	"time"

	"github.com/marco-fabbri/portico/internal/store"
)

// Index answers which sites a tenant is connected to: its active and
// suspended placements, read from the store and cached for ttl so a
// console's burst of requests does not read the store each time. The
// tenant service invalidates a tenant when its placements change, so a
// suspension or a removal takes effect at once in this process (design
// sites §4.3); the pair's other node sees it within ttl.
type Index struct {
	st  store.Store
	ttl time.Duration

	mu    sync.Mutex
	cache map[string]entry
}

// Connection is one site a tenant is connected to. Only an active one
// routes; a suspended one is a greyed tile and counts for "several sites"
// (design sites usability 4.5).
type Connection struct {
	SiteID    string
	Suspended bool
}

type entry struct {
	conns []Connection
	at    time.Time
}

// NewIndex returns an index over the store.
func NewIndex(st store.Store, ttl time.Duration) *Index {
	return &Index{st: st, ttl: ttl, cache: map[string]entry{}}
}

// Connections returns the tenant's active and suspended placements, in
// placement order.
func (x *Index) Connections(ctx context.Context, tenantID string) ([]Connection, error) {
	if tenantID == "" {
		return nil, nil
	}
	x.mu.Lock()
	e, ok := x.cache[tenantID]
	x.mu.Unlock()
	if ok && time.Since(e.at) < x.ttl {
		return e.conns, nil
	}
	ps, err := x.st.ListPlacements(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	var out []Connection
	for _, p := range ps {
		switch p.Status {
		case store.PlacementActive:
			out = append(out, Connection{SiteID: p.SiteID})
		case store.PlacementSuspended:
			out = append(out, Connection{SiteID: p.SiteID, Suspended: true})
		}
	}
	x.mu.Lock()
	x.cache[tenantID] = entry{conns: out, at: time.Now()}
	x.mu.Unlock()
	return out, nil
}

// Invalidate drops the tenant's cached connections.
func (x *Index) Invalidate(tenantID string) {
	x.mu.Lock()
	delete(x.cache, tenantID)
	x.mu.Unlock()
}
