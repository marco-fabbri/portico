package tenant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/store"
)

var netRemoveSteps = []string{"net-floating-ips", "net-subnet", "net-vpc", "net-unshare", "net-external-subnet"}

// removalSteps returns the names of the steps recorded after the build,
// that is from the first one named in netRemoveSteps after "net-read-back".
func removalSteps(t *testing.T, e *env, id string) []string {
	t.Helper()
	steps, err := e.st.ListSteps(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	built := false
	for _, s := range steps {
		if built {
			out = append(out, s.Name)
		}
		if s.Name == "net-read-back" {
			built = true
		}
	}
	return out
}

// built creates the tenant on the network site and returns its placement,
// with the project's counts as Prism Central would give them: Portico's
// VPC, subnet and floating IP.
func built(t *testing.T, e *env, id string) *store.Placement {
	t.Helper()
	e.create(t, id)
	e.svc.Wait()
	p := e.place(t, id)
	if p.NetState != store.NetReady {
		t.Fatalf("%s: network %s %s", id, p.NetState, p.NetError)
	}
	e.pc.Contents[p.ProjectExtID] = nutanix.Contents{Counts: map[string]int{"vpcs": 1, "subnets": 1, "floating-ips": len(p.FloatingIPs)}}
	return p
}

func TestDisconnectRemovesNetwork(t *testing.T) {
	e := newNetEnv(t)
	p := built(t, e, "acme")
	if err := e.svc.Disconnect(context.Background(), "acme", "site-a"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	e.svc.Wait()
	got := removalSteps(t, e, "acme")
	want := append(append([]string{}, netRemoveSteps...), "role-membership", "idp-unshare", "saml-group", "resource-group", "project")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	if _, err := e.st.GetPlacement(context.Background(), "acme", "site-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("placement after disconnect: %v", err)
	}
	if e.pc.SubnetNamed("portico-acme-ext") != nil || e.pc.SubnetNamed("portico-acme-net") != nil || e.pc.VPCNamed("portico-acme-vpc") != nil || len(e.pc.FloatingIPs) != 0 {
		t.Fatalf("left: subnets %+v vpcs %+v fips %+v", e.pc.Subnets, e.pc.VPCs, e.pc.FloatingIPs)
	}
	if e.pc.CallCount("UnshareSubnet") != 1 || p.ExtSubnetExtID == "" {
		t.Fatalf("unshare calls %d", e.pc.CallCount("UnshareSubnet"))
	}
}

func TestDisconnectRefusesAssociatedFloatingIP(t *testing.T) {
	e := newNetEnv(t)
	p := built(t, e, "acme")
	e.pc.FloatingIPs[0].Associated, e.pc.FloatingIPs[0].AssociatedVMNIC = true, "nic-1"
	err := e.svc.Disconnect(context.Background(), "acme", "site-a")
	var ne *NotEmptyError
	if !errors.As(err, &ne) || !strings.Contains(err.Error(), "portico-acme-fip-1") || !strings.Contains(err.Error(), p.FloatingIPs[0].IP) {
		t.Fatalf("Disconnect = %v", err)
	}
	e.svc.Wait()
	if len(e.pc.FloatingIPs) != 1 || e.place(t, "acme").Status != store.PlacementActive {
		t.Fatalf("something was removed: fips %+v", e.pc.FloatingIPs)
	}
}

func TestContentsIgnoresPorticoObjects(t *testing.T) {
	e := newNetEnv(t)
	p := built(t, e, "acme")
	e.pc.Contents[p.ProjectExtID] = nutanix.Contents{Counts: map[string]int{"vpcs": 1, "subnets": 1, "floating-ips": 1, "vms": 1}}
	c, err := e.svc.Contents(context.Background(), "acme", "site-a")
	if err != nil || c.String() != "VMs: 1" {
		t.Fatalf("Contents = %q, %v", c.String(), err)
	}
}

func TestContentsCountsTenantFloatingIP(t *testing.T) {
	e := newNetEnv(t)
	p := built(t, e, "acme")
	e.pc.FloatingIPs = append(e.pc.FloatingIPs, nutanix.FloatingIP{ExtID: "fip-own", Name: "mine", ProjectExtID: p.ProjectExtID})
	e.pc.Contents[p.ProjectExtID] = nutanix.Contents{Counts: map[string]int{"vpcs": 1, "subnets": 1, "floating-ips": 2}}
	c, err := e.svc.Contents(context.Background(), "acme", "site-a")
	if err != nil || c.String() != "floating IPs: 1" {
		t.Fatalf("Contents = %q, %v", c.String(), err)
	}
}

func TestDisconnectWithAnotherTenant(t *testing.T) {
	e := newNetEnv(t)
	built(t, e, "acme")
	b := built(t, e, "beta")
	if err := e.svc.Disconnect(context.Background(), "acme", "site-a"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	e.svc.Wait()
	ext, vpc := e.pc.SubnetNamed("portico-beta-ext"), e.pc.VPCNamed("portico-beta-vpc")
	if ext == nil || vpc == nil || e.pc.SubnetNamed("portico-beta-net") == nil || e.pc.FloatingIPNamed("portico-beta-fip-1") == nil || !contains(ext.SharedWith, b.ProjectExtID) {
		t.Fatalf("beta's network touched: subnets %+v vpcs %+v", e.pc.Subnets, e.pc.VPCs)
	}
	if e.pc.SubnetNamed("portico-acme-ext") != nil {
		t.Fatal("acme's external subnet left")
	}
}

// Someone deleted the tenant's network in Prism Central: Disconnect says
// so and goes on.
func TestDisconnectAfterTenantDeleted(t *testing.T) {
	e := newNetEnv(t)
	p := built(t, e, "acme")
	e.pc.FloatingIPs, e.pc.VPCs, e.pc.Subnets = nil, e.pc.VPCs[:1], nil
	e.pc.Contents[p.ProjectExtID] = nutanix.Contents{Counts: map[string]int{}}
	if err := e.svc.Disconnect(context.Background(), "acme", "site-a"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	e.svc.Wait()
	steps, _ := e.st.ListSteps(context.Background(), "acme")
	absent := 0
	for _, s := range steps {
		if strings.HasPrefix(s.Name, "net-") && strings.Contains(s.Detail, "already absent") {
			absent++
		}
		if !s.OK {
			t.Fatalf("step %+v", s)
		}
	}
	if absent != len(netRemoveSteps) {
		t.Fatalf("%d already absent lines in %+v", absent, steps)
	}
	if _, err := e.st.GetPlacement(context.Background(), "acme", "site-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("placement after disconnect: %v", err)
	}
}

// Objects whose extId was never stored (a create that succeeded after
// its call failed) are found by name at Disconnect, not left behind
// (review: the next tenant's /28 overlapped the orphan).
func TestDisconnectFindsUnstoredByName(t *testing.T) {
	e := newNetEnv(t)
	p := built(t, e, "acme")
	p.ExtSubnetExtID, p.VPCExtID, p.SubnetExtID = "", "", ""
	if err := e.st.UpdatePlacement(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Disconnect(context.Background(), "acme", "site-a"); err != nil {
		t.Fatal(err)
	}
	e.svc.Wait()
	for _, n := range []string{"portico-acme-ext", "portico-acme-net"} {
		if e.pc.SubnetNamed(n) != nil {
			t.Fatalf("%s left behind", n)
		}
	}
	if e.pc.VPCNamed("portico-acme-vpc") != nil {
		t.Fatal("the VPC left behind")
	}
}
