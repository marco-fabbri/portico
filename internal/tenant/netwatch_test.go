package tenant

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/store"
)

// readyNet is a newNetEnv with acme's ready network built.
func readyNet(t *testing.T) *env {
	t.Helper()
	e := newNetEnv(t)
	e.create(t, "acme")
	e.svc.Wait()
	if pl := e.place(t, "acme"); pl.NetState != store.NetReady {
		t.Fatalf("placement = %+v", pl)
	}
	return e
}

func TestWatchDeleted(t *testing.T) {
	e := readyNet(t)
	pl := e.place(t, "acme")
	e.pc.VPCs = slices.DeleteFunc(e.pc.VPCs, func(v nutanix.VPC) bool { return v.ExtID == pl.VPCExtID })
	creates := e.pc.Creates["CreateVPC"]
	e.svc.WatchNetworks(context.Background())
	pl = e.place(t, "acme")
	if pl.NetState != store.NetFailed || !strings.Contains(pl.NetError, "portico-acme-vpc deleted in Prism Central") {
		t.Fatalf("placement = %+v", pl)
	}
	if e.pc.Creates["CreateVPC"] != creates {
		t.Fatal("the watch created the VPC again")
	}
	if pl.Status != store.PlacementActive {
		t.Fatalf("the connection is %s", pl.Status)
	}
}

func TestWatchDeletedFloatingIP(t *testing.T) {
	e := readyNet(t)
	pl := e.place(t, "acme")
	e.pc.FloatingIPs = slices.DeleteFunc(e.pc.FloatingIPs, func(f nutanix.FloatingIP) bool { return f.ExtID == pl.FloatingIPs[0].ExtID })
	e.svc.WatchNetworks(context.Background())
	if pl := e.place(t, "acme"); pl.NetState != store.NetFailed || !strings.Contains(pl.NetError, "floating IP "+pl.FloatingIPs[0].IP+" deleted in Prism Central") {
		t.Fatalf("placement = %+v", pl)
	}
}

func TestWatchChanged(t *testing.T) {
	e := readyNet(t)
	pl := e.place(t, "acme")
	for i := range e.pc.Subnets {
		if e.pc.Subnets[i].ExtID == pl.SubnetExtID {
			e.pc.Subnets[i].ProjectExtID = "another-project"
		}
	}
	e.svc.WatchNetworks(context.Background())
	if pl := e.place(t, "acme"); pl.NetState != store.NetFailed || !strings.Contains(pl.NetError, "portico-acme-net changed in Prism Central") {
		t.Fatalf("placement = %+v", pl)
	}
	// The VPC moved to another external subnet.
	e = readyNet(t)
	pl = e.place(t, "acme")
	for i := range e.pc.VPCs {
		if e.pc.VPCs[i].ExtID == pl.VPCExtID {
			e.pc.VPCs[i].External = []nutanix.VPCExternal{{SubnetExtID: "ext-other"}}
		}
	}
	e.svc.WatchNetworks(context.Background())
	if pl := e.place(t, "acme"); pl.NetState != store.NetFailed || !strings.Contains(pl.NetError, "portico-acme-vpc changed in Prism Central") {
		t.Fatalf("placement = %+v", pl)
	}
}

func TestWatchLeavesHealthyAlone(t *testing.T) {
	e := readyNet(t)
	before := e.place(t, "acme")
	e.svc.WatchNetworks(context.Background())
	after := e.place(t, "acme")
	if after.NetState != store.NetReady || after.NetError != "" || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("placement = %+v", after)
	}
	for _, m := range []string{"GetVPC", "GetSubnet", "GetFloatingIP"} {
		if e.pc.CallCount(m) == 0 {
			t.Fatalf("the watch did not call %s", m)
		}
	}
}

func TestWatchSiteUnreachableChangesNothing(t *testing.T) {
	e := readyNet(t)
	pl := e.place(t, "acme")
	e.pc.VPCs = slices.DeleteFunc(e.pc.VPCs, func(v nutanix.VPC) bool { return v.ExtID == pl.VPCExtID })
	e.pc.FailOnce["GetSubnet"] = errors.New("dial tcp: connection refused")
	e.svc.WatchNetworks(context.Background())
	if pl := e.place(t, "acme"); pl.NetState != store.NetReady {
		t.Fatalf("an unreachable site changed the state: %+v", pl)
	}
}

// A build cut off by a restart is failed at the next start, so Retry
// network can run it again (review: it stayed building for good).
func TestRecoverInterruptedNetwork(t *testing.T) {
	e := readyNet(t)
	pl := e.place(t, "acme")
	pl.NetState = store.NetBuilding
	if err := e.st.UpdatePlacement(context.Background(), pl); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RecoverInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pl := e.place(t, "acme"); pl.NetState != store.NetFailed || !strings.Contains(pl.NetError, "interrupted by a restart") || pl.Status != store.PlacementActive {
		t.Fatalf("placement = %+v", pl)
	}
}

// A site that does not answer ends the pass on that site: the watch does
// not hold each of its tenants in turn (review: Busy never cleared).
func TestWatchStopsAtUnansweredSite(t *testing.T) {
	e := readyNet(t)
	e.create(t, "beta")
	e.svc.Wait()
	before := e.pc.CallCount("GetSubnet")
	e.pc.FailOnce["GetSubnet"] = errors.New("dial tcp: i/o timeout")
	e.svc.WatchNetworks(context.Background())
	if n := e.pc.CallCount("GetSubnet") - before; n != 1 {
		t.Fatalf("%d reads after the site stopped answering", n)
	}
}
