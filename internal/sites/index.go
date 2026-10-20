package sites

import (
	"context"
	"sync"
	"time"

	"github.com/marco-fabbri/portico/internal/store"
)

// Index answers which sites a tenant may be routed to: its active
// placements, read from the store and cached for ttl so a console's
// burst of requests does not read the store each time. The tenant
// service invalidates a tenant when its placements change, so a removal
// takes effect at once in this process (design sites §4.3).
type Index struct {
	st  store.Store
	ttl time.Duration

	mu    sync.Mutex
	cache map[string]entry
}

type entry struct {
	sites []string
	at    time.Time
}

// NewIndex returns an index over the store.
func NewIndex(st store.Store, ttl time.Duration) *Index {
	return &Index{st: st, ttl: ttl, cache: map[string]entry{}}
}

// Sites returns the ids of the sites where the tenant has an active
// placement, in placement order.
func (x *Index) Sites(ctx context.Context, tenantID string) ([]string, error) {
	if tenantID == "" {
		return nil, nil
	}
	x.mu.Lock()
	e, ok := x.cache[tenantID]
	x.mu.Unlock()
	if ok && time.Since(e.at) < x.ttl {
		return e.sites, nil
	}
	ps, err := x.st.ListPlacements(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range ps {
		if p.Status == store.PlacementActive {
			out = append(out, p.SiteID)
		}
	}
	x.mu.Lock()
	x.cache[tenantID] = entry{sites: out, at: time.Now()}
	x.mu.Unlock()
	return out, nil
}

// Invalidate drops the tenant's cached sites.
func (x *Index) Invalidate(tenantID string) {
	x.mu.Lock()
	delete(x.cache, tenantID)
	x.mu.Unlock()
}
