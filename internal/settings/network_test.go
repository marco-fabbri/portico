package settings

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSettingsDefaultPool(t *testing.T) {
	d := Defaults()
	// Not 10.100.0.0/16: Prism Central's microservices use it for their
	// pods (ADR 0009).
	if d.Tenants.AddressPool != "10.128.0.0/16" || d.Tenants.Role != "Portico Tenant" {
		t.Fatalf("defaults = %+v", d.Tenants)
	}
}

func TestSiteNetworkJSON(t *testing.T) {
	s := Site{ID: "site-1", Network: SiteNetwork{TransitVPCExtID: "vpc-t", DNS: []string{"1.1.1.1"}, IntegratedConfirmed: true, Enabled: true}}
	b, _ := json.Marshal(s)
	var back Site
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	n := back.Network
	if n.TransitVPCExtID != "vpc-t" || len(n.DNS) != 1 || n.DNS[0] != "1.1.1.1" || !n.IntegratedConfirmed || !n.Enabled {
		t.Fatalf("network = %+v (json %s)", n, b)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if _, ok := m["network"]; !ok {
		t.Fatalf("no network key: %s", b)
	}
}

// A 0.1.0 document: no pool, and Project Admin, which was the default and
// not a choice, becomes Portico Tenant; another role is kept.
func TestLoadMovesProjectAdminToPorticoTenant(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ stored, want string }{{"Project Admin", "Portico Tenant"}, {"Custom Role", "Custom Role"}} {
		st := newStore(t)
		raw, _ := json.Marshal(map[string]any{"tenants": map[string]any{"project_prefix": "portico", "role": c.stored}})
		if err := st.SetSetting(ctx, SettingKey, raw); err != nil {
			t.Fatal(err)
		}
		got, err := NewService(st, sealerFor(t, 1)).Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got.Tenants.Role != c.want || got.Tenants.AddressPool != DefaultAddressPool {
			t.Fatalf("stored %q: tenants = %+v", c.stored, got.Tenants)
		}
	}
}

// A document without a pool gets the default when it is read, and any save
// stores it: switching a site's ready network on is a save, so a document
// still without a pool has no built network, and a new default moves
// nothing that was built (ADR 0009).
func TestSaveStoresTheFilledPool(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	raw, _ := json.Marshal(map[string]any{"tenants": map[string]any{"project_prefix": "portico"}})
	if err := st.SetSetting(ctx, SettingKey, raw); err != nil {
		t.Fatal(err)
	}
	if err := NewService(st, sealerFor(t, 1)).Patch(ctx, func(s *Settings) { s.Tenants.GroupPrefix = "tenant-" }); err != nil {
		t.Fatal(err)
	}
	stored, err := st.GetSetting(ctx, SettingKey)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tenants struct {
			AddressPool string `json:"address_pool"`
		} `json:"tenants"`
	}
	if err := json.Unmarshal(stored, &doc); err != nil || doc.Tenants.AddressPool != DefaultAddressPool {
		t.Fatalf("stored pool = %q, %v", doc.Tenants.AddressPool, err)
	}
}
