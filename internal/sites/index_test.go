package sites

import (
	"context"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/store"
)

func TestConnectionsListActiveAndSuspended(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	st.CreateTenant(ctx, &store.Tenant{ID: "acme", Status: store.StatusActive})
	for _, p := range []struct {
		site   string
		status store.PlacementStatus
	}{{"a", store.PlacementActive}, {"b", store.PlacementSuspended}, {"c", store.PlacementFailed}, {"d", store.PlacementCreating}, {"e", store.PlacementRemoving}} {
		st.CreatePlacement(ctx, &store.Placement{TenantID: "acme", SiteID: p.site, Status: p.status})
	}
	got, err := NewIndex(st, time.Minute).Connections(ctx, "acme")
	want := []Connection{{SiteID: "a"}, {SiteID: "b", Suspended: true}}
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Connections = %v %v", got, err)
	}
	if got, _ := NewIndex(st, time.Minute).Connections(ctx, ""); len(got) != 0 {
		t.Fatalf("no tenant: %v", got)
	}
}

func TestIndexTTLAndInvalidate(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	st.CreateTenant(ctx, &store.Tenant{ID: "acme", Status: store.StatusActive})
	st.CreatePlacement(ctx, &store.Placement{TenantID: "acme", SiteID: "a", Status: store.PlacementActive})
	x := NewIndex(st, 50*time.Millisecond)
	if got, _ := x.Connections(ctx, "acme"); len(got) != 1 {
		t.Fatalf("first read = %v", got)
	}
	st.DeletePlacement(ctx, "acme", "a")
	if got, _ := x.Connections(ctx, "acme"); len(got) != 1 {
		t.Fatalf("the cache was not used: %v", got)
	}
	x.Invalidate("acme")
	if got, _ := x.Connections(ctx, "acme"); len(got) != 0 {
		t.Fatalf("after Invalidate = %v", got)
	}
	st.CreatePlacement(ctx, &store.Placement{TenantID: "acme", SiteID: "b", Status: store.PlacementActive})
	time.Sleep(60 * time.Millisecond)
	if got, _ := x.Connections(ctx, "acme"); len(got) != 1 || got[0].SiteID != "b" {
		t.Fatalf("after the TTL = %v", got)
	}
}
