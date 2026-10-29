package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPlacementNetworkRoundTrip(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if err := s.CreateTenant(ctx, &Tenant{ID: "acme", Status: StatusCreating}); err != nil {
		t.Fatal(err)
	}
	p := samplePlacement("site-a")
	if err := s.CreatePlacement(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.NetState, p.NetError = NetFailed, "create VPC: refused"
	p.TransitPrefix, p.ExtSubnetExtID, p.VPCExtID, p.SubnetExtID, p.SNATIP = "10.23.211.16/28", "oen-1", "vpc-1", "s-1", "10.23.211.29"
	p.FloatingIPs = []FloatingIP{{ExtID: "fip-1", IP: "10.23.211.18"}, {ExtID: "fip-2", IP: "10.23.211.19"}}
	if err := s.UpdatePlacement(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPlacement(ctx, "acme", "site-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.NetState != NetFailed || got.NetError != "create VPC: refused" || got.TransitPrefix != "10.23.211.16/28" ||
		got.ExtSubnetExtID != "oen-1" || got.VPCExtID != "vpc-1" || got.SubnetExtID != "s-1" || got.SNATIP != "10.23.211.29" ||
		len(got.FloatingIPs) != 2 || got.FloatingIPs[0] != (FloatingIP{ExtID: "fip-1", IP: "10.23.211.18"}) || got.FloatingIPs[1].ExtID != "fip-2" {
		t.Fatalf("placement = %+v", got)
	}
	fresh := samplePlacement("site-b")
	if err := s.CreatePlacement(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetPlacement(ctx, "acme", "site-b"); got.NetState != NetNone || got.FloatingIPs != nil {
		t.Fatalf("new placement = %+v, want net state none and no floating IPs", got)
	}
}

func TestTenantSubnetRoundTrip(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	tn := &Tenant{ID: "acme", Status: StatusCreating, Subnet: "192.168.5.0/24", SubnetException: true, FIPAllowance: 3}
	if err := s.CreateTenant(ctx, tn); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTenant(ctx, "acme")
	if got.Subnet != "192.168.5.0/24" || !got.SubnetException || got.FIPAllowance != 3 {
		t.Fatalf("tenant = %+v", got)
	}
	got.Subnet, got.SubnetException, got.FIPAllowance = "10.100.1.0/24", false, 0
	if err := s.UpdateTenant(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetTenant(ctx, "acme"); again.Subnet != "10.100.1.0/24" || again.SubnetException || again.FIPAllowance != 0 {
		t.Fatalf("updated tenant = %+v", again)
	}
}

// A 0.1.0 store opens with no ready network on its connections and one
// floating IP allowed per tenant.
func TestMigration0006Defaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "portico.db")
	db := openAtVersion(t, path, 5)
	now := formatTime(time.Now())
	if _, err := db.Exec(`INSERT INTO tenants (id, status, created_at, updated_at) VALUES ('acme','active',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tenant_placements (tenant_id, site_id, status, created_at, updated_at) VALUES ('acme','site-1','active',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	tn, _ := s.GetTenant(ctx, "acme")
	p, _ := s.GetPlacement(ctx, "acme", "site-1")
	if tn.FIPAllowance != 1 || tn.Subnet != "" || p.NetState != NetNone {
		t.Fatalf("tenant = %+v, placement = %+v", tn, p)
	}
}
