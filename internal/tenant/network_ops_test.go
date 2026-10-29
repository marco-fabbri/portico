package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/marco-fabbri/portico/internal/store"
)

func TestRetryNetworkOnlyNetSteps(t *testing.T) {
	e := newNetEnv(t)
	e.pc.FailOnce["CreateVPC"] = errors.New("vpc refused")
	e.create(t, "acme")
	e.svc.Wait()
	before, _ := e.st.ListSteps(context.Background(), "acme")
	if err := e.svc.RetryNetwork(context.Background(), "acme", "site-a"); err != nil {
		t.Fatalf("RetryNetwork: %v", err)
	}
	e.svc.Wait()
	after, _ := e.st.ListSteps(context.Background(), "acme")
	for _, s := range after[len(before):] {
		if s.Name[:4] != "net-" {
			t.Fatalf("retry ran %s", s.Name)
		}
	}
	if pl := e.place(t, "acme"); pl.NetState != store.NetReady || e.pc.Creates["CreateOverlayExternalSubnet"] != 1 {
		t.Fatalf("placement = %+v, external subnets created %d", pl, e.pc.Creates["CreateOverlayExternalSubnet"])
	}
	if err := e.svc.RetryNetwork(context.Background(), "acme", "site-a"); !errors.Is(err, ErrState) {
		t.Fatalf("retry of a ready network: %v", err)
	}
}

// A connection from before 0.2.0, or made while the switch was off.
func TestCreateNetworkForUpgradedConnection(t *testing.T) {
	e := newEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	net := newNetEnv(t).svc.Sites()[0].Network
	sites := e.svc.Sites()
	sites[0].Network = net
	e.svc.SetSites(sites)
	e.pc.VPCs = append(e.pc.VPCs, newNetEnv(t).pc.VPCs...)
	if err := e.svc.CreateNetwork(context.Background(), "acme", "site-a"); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}
	e.svc.Wait()
	if pl := e.place(t, "acme"); pl.NetState != store.NetReady {
		t.Fatalf("placement = %+v", pl)
	}
	if err := e.svc.CreateNetwork(context.Background(), "acme", "site-a"); !errors.Is(err, ErrState) {
		t.Fatalf("a second CreateNetwork: %v", err)
	}
}

func TestCreateNetworkRefusedWhenDisabled(t *testing.T) {
	e := newEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	if err := e.svc.CreateNetwork(context.Background(), "acme", "site-a"); !errors.Is(err, ErrState) {
		t.Fatalf("CreateNetwork with the switch off: %v", err)
	}
}

func TestAllowanceUp(t *testing.T) {
	e := newNetEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	kept, err := e.svc.SetAllowance(context.Background(), "acme", 3)
	if err != nil || kept != 0 {
		t.Fatalf("SetAllowance = %d, %v", kept, err)
	}
	pl := e.place(t, "acme")
	if len(pl.FloatingIPs) != 3 || e.pc.FloatingIPNamed("portico-acme-fip-2") == nil || e.pc.FloatingIPNamed("portico-acme-fip-3") == nil || e.get(t, "acme").FIPAllowance != 3 {
		t.Fatalf("floating IPs = %+v", pl.FloatingIPs)
	}
}

func TestAllowanceDownKeepsAssociated(t *testing.T) {
	e := newNetEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	if _, err := e.svc.SetAllowance(context.Background(), "acme", 3); err != nil {
		t.Fatal(err)
	}
	for i := range e.pc.FloatingIPs {
		if e.pc.FloatingIPs[i].Name == "portico-acme-fip-2" {
			e.pc.FloatingIPs[i].Associated = true
		}
	}
	kept, err := e.svc.SetAllowance(context.Background(), "acme", 0)
	if err != nil || kept != 1 {
		t.Fatalf("SetAllowance = %d, %v", kept, err)
	}
	pl := e.place(t, "acme")
	if len(e.pc.FloatingIPs) != 1 || e.pc.FloatingIPs[0].Name != "portico-acme-fip-2" || len(pl.FloatingIPs) != 1 || pl.FloatingIPs[0].ExtID != e.pc.FloatingIPs[0].ExtID {
		t.Fatalf("left %+v, stored %+v", e.pc.FloatingIPs, pl.FloatingIPs)
	}
	// Going up again fills the free names first.
	if _, err := e.svc.SetAllowance(context.Background(), "acme", 2); err != nil {
		t.Fatal(err)
	}
	if e.pc.FloatingIPNamed("portico-acme-fip-1") == nil || len(e.pc.FloatingIPs) != 2 {
		t.Fatalf("floating IPs = %+v", e.pc.FloatingIPs)
	}
}

func TestAllowanceBounds(t *testing.T) {
	e := newNetEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	for _, n := range []int{-1, 13} {
		if _, err := e.svc.SetAllowance(context.Background(), "acme", n); !errors.Is(err, ErrInvalidAllowance) {
			t.Fatalf("allowance %d: %v", n, err)
		}
	}
	if e.get(t, "acme").FIPAllowance != 1 {
		t.Fatal("a refused allowance was stored")
	}
}
