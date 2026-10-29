package settings

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/settings/settingstest"
)

// opsOf returns a fake role's operations as strings.
func opsOf(role map[string]any) []string {
	var out []string
	raw, _ := json.Marshal(role["operations"])
	json.Unmarshal(raw, &out)
	slices.Sort(out)
	return out
}

func networkingOpIDs() []string {
	var out []string
	for _, n := range NetworkingOperations {
		out = append(out, "op-"+n)
	}
	slices.Sort(out)
	return out
}

func rolesNamed(f *settingstest.PC, name string) int {
	n := 0
	for _, r := range f.Roles {
		if r["displayName"] == name {
			n++
		}
	}
	return n
}

func TestSetupCreatesNetworkingRole(t *testing.T) {
	f := setupEnv(t)
	res := f.run(t, f.admin(f.pin))
	if s := stepOf(res, "networking-rights"); s.Status != "ok" || !res.Complete {
		t.Fatalf("steps = %+v", res.Steps)
	}
	role := f.pc.Role("Portico Networking")
	if role == nil || !slices.Equal(opsOf(role), networkingOpIDs()) {
		t.Fatalf("role = %v", role)
	}
	pol := f.pc.Policy("Portico Networking_portico")
	if pol == nil || pol["role"] != role["extId"] {
		t.Fatalf("policy = %v", pol)
	}
	raw, _ := json.Marshal(pol)
	for _, want := range []string{`"sa-1"`, `"subnet"`, `"floating_ip"`, `"vpc"`, `"vpc_route_table"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("policy lacks %s: %s", want, raw)
		}
	}
}

func TestSetupCreatesPorticoTenantRole(t *testing.T) {
	f := setupEnv(t)
	f.run(t, f.admin(f.pin))
	role := f.pc.Role("Portico Tenant")
	want := []string{"op-Update_Floating_Ip", "op-View_Project", "op-View_Subnet"}
	if role == nil || !slices.Equal(opsOf(role), want) {
		t.Fatalf("role = %v, want operations %v", role, want)
	}
}

// A role made by the 0.1 lab script, with 6 operations, gets the list.
func TestSetupResyncsRoleOperations(t *testing.T) {
	f := setupEnv(t)
	f.pc.Roles = append(f.pc.Roles, map[string]any{"extId": "role-old", "displayName": "Portico Networking", "operations": []any{"op-View_Subnet"}})
	f.run(t, f.admin(f.pin))
	role := f.pc.Role("Portico Networking")
	if rolesNamed(f.pc, "Portico Networking") != 1 || role["extId"] != "role-old" || f.pc.RoleUpdates != 1 || !slices.Equal(opsOf(role), networkingOpIDs()) {
		t.Fatalf("role = %v, updates %d", role, f.pc.RoleUpdates)
	}
	if pol := f.pc.Policy("Portico Networking_portico"); pol == nil || pol["role"] != "role-old" {
		t.Fatalf("policy = %v", pol)
	}
}

// Prism Central refused the update of the lab role (write-up 34): the
// networking role, named by Portico's policy only, is made again.
func TestSetupReplacesRefusedNetworkingRole(t *testing.T) {
	f := setupEnv(t)
	f.pc.RoleUpdateStatus = 400
	f.pc.Roles = append(f.pc.Roles, map[string]any{"extId": "role-old", "displayName": "Portico Networking", "operations": []any{"op-View_Subnet"}})
	f.pc.Policies = append(f.pc.Policies, map[string]any{"extId": "pol-old", "displayName": "Portico Networking_portico", "role": "role-old",
		"identities": []any{map[string]any{"identityFilter": map[string]any{"user": map[string]any{"uuid": map[string]any{"anyof": []any{"sa-1"}}}}}}})
	res := f.run(t, f.admin(f.pin))
	role := f.pc.Role("Portico Networking")
	if s := stepOf(res, "networking-rights"); s.Status != "ok" || rolesNamed(f.pc, "Portico Networking") != 1 || role["extId"] == "role-old" || !slices.Equal(opsOf(role), networkingOpIDs()) {
		t.Fatalf("step = %+v, role = %v", stepOf(res, "networking-rights"), role)
	}
	if pol := f.pc.Policy("Portico Networking_portico"); pol == nil || pol["role"] != role["extId"] || len(f.pc.Policies) != 1 {
		t.Fatalf("policies = %v", f.pc.Policies)
	}
}

// Portico Tenant is named by the tenants' memberships: a refused update
// keeps it and says so, the run goes on.
func TestSetupKeepsTenantRoleWhenUpdateRefused(t *testing.T) {
	f := setupEnv(t)
	f.pc.RoleUpdateStatus = 400
	f.pc.Roles = append(f.pc.Roles, map[string]any{"extId": "role-pt", "displayName": "Portico Tenant", "operations": []any{"op-View_Project"}})
	res := f.run(t, f.admin(f.pin))
	s := stepOf(res, "networking-rights")
	if s.Status != "ok" || !strings.Contains(s.Message, "refused") || !res.Complete || f.pc.Role("Portico Tenant")["extId"] != "role-pt" {
		t.Fatalf("step = %+v", s)
	}
}

func TestSetupKeepsRightRoles(t *testing.T) {
	f := setupEnv(t)
	f.run(t, f.admin(f.pin))
	res := f.run(t, f.admin(f.pin))
	if s := stepOf(res, "networking-rights"); s.Status != "kept" || f.pc.RoleUpdates != 0 || len(f.pc.Policies) != 1 {
		t.Fatalf("step = %+v, updates %d, policies %d", s, f.pc.RoleUpdates, len(f.pc.Policies))
	}
}

func TestSetupStepOrder(t *testing.T) {
	f := setupEnv(t)
	f.pc.RoleCreateStatus = 403
	res := f.run(t, f.admin(f.pin))
	i := slices.IndexFunc(res.Steps, func(s StepLine) bool { return s.Name == "networking-rights" })
	if i < 1 || res.Steps[i-1].Name != "service-account" || i != len(res.Steps)-1 {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if s := res.Steps[i]; s.Status != "failed" || !strings.Contains(s.Message, "Create_Role") || res.Complete {
		t.Fatalf("step = %+v", s)
	}
}
