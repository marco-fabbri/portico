package sites

import (
	"context"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/store"
)

func TestIndexActiveOnly(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	st.CreateTenant(ctx, &store.Tenant{ID: "acme", Status: store.StatusActive})
	for site, status := range map[string]store.PlacementStatus{"a": store.PlacementActive, "b": store.PlacementCreating, "c": store.PlacementFailed, "d": store.PlacementRemoving} {
		st.CreatePlacement(ctx, &store.Placement{TenantID: "acme", SiteID: site, Status: status})
	}
	got, err := NewIndex(st, time.Minute).Sites(ctx, "acme")
	if err != nil || len(got) != 1 || got[0] != "a" {
		t.Fatalf("Sites = %v %v", got, err)
	}
	if got, _ := NewIndex(st, time.Minute).Sites(ctx, ""); len(got) != 0 {
		t.Fatalf("no tenant: %v", got)
	}
}

func TestIndexTTLAndInvalidate(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	st.CreateTenant(ctx, &store.Tenant{ID: "acme", Status: store.StatusActive})
	st.CreatePlacement(ctx, &store.Placement{TenantID: "acme", SiteID: "a", Status: store.PlacementActive})
	x := NewIndex(st, 50*time.Millisecond)
	if got, _ := x.Sites(ctx, "acme"); len(got) != 1 {
		t.Fatalf("first read = %v", got)
	}
	st.DeletePlacement(ctx, "acme", "a")
	if got, _ := x.Sites(ctx, "acme"); len(got) != 1 {
		t.Fatalf("the cache was not used: %v", got)
	}
	x.Invalidate("acme")
	if got, _ := x.Sites(ctx, "acme"); len(got) != 0 {
		t.Fatalf("after Invalidate = %v", got)
	}
	st.CreatePlacement(ctx, &store.Placement{TenantID: "acme", SiteID: "b", Status: store.PlacementActive})
	time.Sleep(60 * time.Millisecond)
	if got, _ := x.Sites(ctx, "acme"); len(got) != 1 || got[0] != "b" {
		t.Fatalf("after the TTL = %v", got)
	}
}
