package tenant

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/marco-fabbri/portico/internal/nutanix"
)

// withDefaultRole replaces the env's service with one on the default
// role, Portico Tenant.
func withDefaultRole(t *testing.T, e *env) {
	t.Helper()
	cfg := Config{ProjectPrefix: "portico", GroupPrefix: "tenant-", OperationTimeout: 5 * time.Second}
	e.svc = NewService(e.st, e.svc.Sites(), e.kc, cfg, slog.New(slog.DiscardHandler))
}

// A site set up before 0.2.0 has no Portico Tenant role: the tenant gets
// Project Admin, and the step says so (tenant networking design 4.3).
func TestRoleFallbackOnUnrepairedSite(t *testing.T) {
	e := newEnv(t)
	withDefaultRole(t, e)
	e.create(t, "acme")
	e.svc.Wait()
	if len(e.pc.Memberships) != 1 || e.pc.Memberships[0].RoleExtID != "role-pa" {
		t.Fatalf("memberships = %+v", e.pc.Memberships)
	}
	steps, _ := e.st.ListSteps(context.Background(), "acme")
	var detail string
	for _, s := range steps {
		if s.Name == "role-membership" {
			detail = s.Detail
		}
	}
	if !strings.Contains(detail, "fallback: Project Admin, Repair the site") {
		t.Fatalf("role-membership detail = %q", detail)
	}
}

// Once the site has the role, Repair moves the group to it and deletes
// the Project Admin membership.
func TestRoleMembershipMoves(t *testing.T) {
	e := newEnv(t)
	withDefaultRole(t, e)
	e.create(t, "acme")
	e.svc.Wait()
	e.pc.Roles = append(e.pc.Roles, nutanix.Role{ExtID: "role-pt", DisplayName: "Portico Tenant"})
	if _, err := e.svc.RepairSite(context.Background(), "site-a", ""); err != nil {
		t.Fatalf("RepairSite: %v", err)
	}
	pl := e.place(t, "acme")
	if len(e.pc.Memberships) != 1 || e.pc.Memberships[0].RoleExtID != "role-pt" || pl.RoleMembershipExtID != e.pc.Memberships[0].ExtID {
		t.Fatalf("memberships = %+v, stored %q", e.pc.Memberships, pl.RoleMembershipExtID)
	}
}
