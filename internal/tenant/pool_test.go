package tenant

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/marco-fabbri/portico/internal/store"
)

// The pool is fixed only once a pool /24 is in a built network (Marco,
// 2026-10-30): before that, a new pool moves the tenants' unused /24s.
func TestPoolChangeMovesUnusedSubnets(t *testing.T) {
	e := newEnv(t)
	e.create(t, "acme")
	e.create(t, "beta")
	e.svc.Wait()
	if used, err := e.svc.PoolInUse(context.Background()); err != nil || used {
		t.Fatalf("PoolInUse = %v, %v before any network", used, err)
	}
	e.svc.SetAddressPool(netip.MustParsePrefix("10.200.0.0/16"))
	if _, err := e.svc.AssignSubnets(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a, b := e.get(t, "acme"), e.get(t, "beta"); a.Subnet != "10.200.0.0/24" || b.Subnet != "10.200.1.0/24" {
		t.Fatalf("acme %q, beta %q", a.Subnet, b.Subnet)
	}
}

func TestPoolInUseOnceBuilt(t *testing.T) {
	e := readyNet(t)
	if used, err := e.svc.PoolInUse(context.Background()); err != nil || !used {
		t.Fatalf("PoolInUse = %v, %v with a ready network", used, err)
	}
	// An exception's network does not hold the pool.
	e2 := newEnv(t)
	if _, err := e2.svc.Create(context.Background(), CreateRequest{Slug: "acme", DisplayName: "A", Subnet: "192.168.5.0/24"}); err != nil {
		t.Fatal(err)
	}
	e2.svc.Wait()
	if used, _ := e2.svc.PoolInUse(context.Background()); used {
		t.Fatal("an exception holds the pool")
	}
}

// The exception is changed on the tenant's page until its first ready
// network's subnet exists (spec 4.2).
func TestSetSubnet(t *testing.T) {
	e := newEnv(t)
	e.create(t, "acme")
	e.create(t, "beta")
	e.svc.Wait()
	ctx := context.Background()
	if err := e.svc.SetSubnet(ctx, "acme", "192.168.7.0/24"); err != nil {
		t.Fatal(err)
	}
	if a := e.get(t, "acme"); a.Subnet != "192.168.7.0/24" || !a.SubnetException {
		t.Fatalf("acme = %q %v", a.Subnet, a.SubnetException)
	}
	if err := e.svc.SetSubnet(ctx, "beta", "192.168.7.0/24"); !errors.Is(err, ErrInvalidSubnet) {
		t.Fatalf("an overlapping exception: %v", err)
	}
	// Back to the pool.
	if err := e.svc.SetSubnet(ctx, "acme", ""); err != nil {
		t.Fatal(err)
	}
	if a := e.get(t, "acme"); a.SubnetException || a.Subnet == "192.168.7.0/24" {
		t.Fatalf("acme = %q %v", a.Subnet, a.SubnetException)
	}
	p := e.place(t, "acme")
	p.SubnetExtID, p.NetState = "net-1", store.NetReady
	if err := e.st.UpdatePlacement(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetSubnet(ctx, "acme", "192.168.8.0/24"); !errors.Is(err, ErrState) {
		t.Fatalf("with a network: %v", err)
	}
}
