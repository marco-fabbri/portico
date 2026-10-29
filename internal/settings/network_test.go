package settings

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSettingsDefaultPool(t *testing.T) {
	d := Defaults()
	if d.Tenants.AddressPool != "10.100.0.0/16" || d.Tenants.Role != "Portico Tenant" {
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
		if got.Tenants.Role != c.want || got.Tenants.AddressPool != "10.100.0.0/16" {
			t.Fatalf("stored %q: tenants = %+v", c.stored, got.Tenants)
		}
	}
}
