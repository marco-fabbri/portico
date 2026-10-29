package tenant

import (
	"context"
	"fmt"
	"sort"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/store"
)

// RepairLine is one tenant's outcome of a site repair.
type RepairLine struct {
	Tenant  string `json:"tenant"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// RepairSite binds every tenant connected to the site again to the site's
// current identity provider (design sites 4.1, "Repair on a site in
// use"): its SAML group, the provider's sharing with its project and its
// role membership, the steps provisioning runs. A connection keeps its
// state, so a suspended one stays suspended. The role membership a
// connection had before is deleted when the repair made another one;
// with oldIdPExtID (the trust was just recreated) the tenant's groups on
// that provider are deleted too, and a group of any other provider is
// left alone. One tenant's failure does not stop the others; the error
// is for the site itself.
func (s *Service) RepairSite(ctx context.Context, siteID, oldIdPExtID string) ([]RepairLine, error) {
	site, err := s.site(siteID)
	if err != nil {
		return nil, err
	}
	ps, err := s.st.ListPlacementsBySite(ctx, siteID)
	if err != nil {
		return nil, err
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].TenantID < ps[j].TenantID })
	lines := []RepairLine{}
	var idp *nutanix.SAMLIdP
	var role *nutanix.Role
	for _, p := range ps {
		if p.Status != store.PlacementActive && p.Status != store.PlacementSuspended {
			continue
		}
		if idp == nil {
			if idp, err = s.idp(ctx, site); err != nil {
				return nil, err
			}
			if role, err = s.role(ctx, site.PC); err != nil {
				return nil, err
			}
		}
		line := RepairLine{Tenant: p.TenantID, OK: true, Message: "bound to identity provider " + idp.ExtID}
		if err := s.repairPlacement(ctx, site, p.TenantID, idp, role, oldIdPExtID); err != nil {
			line.OK, line.Message = false, err.Error()
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// repairPlacement runs the repair of one connection under the tenant's
// lock.
func (s *Service) repairPlacement(ctx context.Context, site Site, tenantID string, idp *nutanix.SAMLIdP, role *nutanix.Role, oldIdP string) error {
	if !s.acquire(tenantID) {
		return ErrBusy
	}
	defer s.release(tenantID)
	t, err := s.st.GetTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	p, err := s.st.GetPlacement(ctx, tenantID, site.ID)
	if err != nil {
		return err
	}
	pc, oldRM := site.PC, p.RoleMembershipExtID
	steps := []struct {
		name string
		fn   func(ctx context.Context) (string, error)
	}{
		{"saml-group", func(ctx context.Context) (string, error) { return s.samlGroupStep(ctx, pc, t, p, idp) }},
		{"idp-share", func(ctx context.Context) (string, error) { return s.idpShareStep(ctx, pc, p, idp) }},
		{"role-membership", func(ctx context.Context) (string, error) { return s.roleMembershipStep(ctx, pc, p, idp, role) }},
		{"repair-cleanup", func(ctx context.Context) (string, error) { return s.repairCleanup(ctx, pc, t, p, oldRM, oldIdP) }},
	}
	for _, st := range steps {
		if err := s.record(ctx, t.ID, site.ID, st.name, st.fn); err != nil {
			return err
		}
		if err := s.st.UpdatePlacement(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// repairCleanup deletes what pointed at the old provider: the role
// membership the connection had before, when the repair made another,
// and the tenant's groups on oldIdP.
func (s *Service) repairCleanup(ctx context.Context, pc PrismCentral, t *store.Tenant, p *store.Placement, oldRM, oldIdP string) (string, error) {
	n := 0
	if oldRM != "" && oldRM != p.RoleMembershipExtID {
		if err := pc.DeleteRoleMembership(ctx, oldRM); err != nil && !nutanix.IsNotFound(err) {
			return "", err
		}
		n++
	}
	if oldIdP != "" {
		groups, err := pc.ListUserGroups(ctx)
		if err != nil {
			return "", err
		}
		for _, g := range groups {
			if g.Name != s.groupName(t.ID) || g.IdPExtID != oldIdP || g.ExtID == p.SAMLGroupExtID {
				continue
			}
			if err := pc.DeleteUserGroup(ctx, g.ExtID); err != nil && !nutanix.IsNotFound(err) {
				return "", err
			}
			n++
		}
	}
	return fmt.Sprintf("%d object(s) of the old identity provider deleted", n), nil
}
