package tenant

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/marco-fabbri/portico/internal/idp/keycloak"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/store"
)

// record runs fn as the step name of a tenant, on a site or tenant-wide
// (siteID ""), and records the outcome.
func (s *Service) record(ctx context.Context, tenantID, siteID, name string, fn func(ctx context.Context) (string, error)) error {
	detail, err := fn(ctx)
	rec := store.Step{TenantID: tenantID, SiteID: siteID, Name: name, OK: err == nil, Detail: detail}
	if err != nil {
		rec.Detail = err.Error()
	}
	if aerr := s.st.AddStep(ctx, rec); aerr != nil {
		s.log.Warn("step record failed", "tenant", tenantID, "site", siteID, "step", name, "err", aerr.Error())
	}
	s.log.Info("tenant step", "tenant", tenantID, "site", siteID, "step", name, "ok", err == nil, "detail", rec.Detail)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// site returns the configured site of a placement.
func (s *Service) site(id string) (Site, error) {
	site, ok := s.sites[id]
	if !ok {
		return Site{}, fmt.Errorf("site %s is not configured in Portico", id)
	}
	return site, nil
}

// idp returns the site's single SAML identity provider named in the settings.
func (s *Service) idp(ctx context.Context, site Site) (*nutanix.SAMLIdP, error) {
	idps, err := site.PC.ListSAMLIdPs(ctx)
	if err != nil {
		return nil, err
	}
	var found []nutanix.SAMLIdP
	for _, i := range idps {
		if i.Name == site.SAMLIdPName {
			found = append(found, i)
		}
	}
	switch len(found) {
	case 1:
		return &found[0], nil
	case 0:
		return nil, fmt.Errorf("SAML identity provider %q not found in Prism Central", site.SAMLIdPName)
	default:
		return nil, fmt.Errorf("%d SAML identity providers named %q in Prism Central, expected one", len(found), site.SAMLIdPName)
	}
}

// provision runs the tenant-wide step, then the site steps on every
// placement that is not active; the caller holds the tenant lock. A
// failure on one site leaves the others done. With keepStatus the
// tenant's status is left as it is (a site added to an active tenant);
// otherwise it ends active or failed naming the sites that failed.
func (s *Service) provision(ctx context.Context, id string, keepStatus bool) error {
	defer s.changed(id)
	t, err := s.st.GetTenant(ctx, id)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		if keepStatus {
			return err
		}
		t.Status, t.LastError = store.StatusFailed, err.Error()
		if uerr := s.st.UpdateTenant(ctx, t); uerr != nil {
			s.log.Warn("tenant update failed", "tenant", id, "err", uerr.Error())
		}
		return err
	}
	ps, err := s.st.ListPlacements(ctx, id)
	if err != nil {
		return fail(err)
	}
	// Preconditions before any write, site by site: the identity provider
	// and the role. A site that fails them is failed; when every pending
	// site fails them nothing is written at all.
	var failures []string
	var ready []prepared
	for i := range ps {
		// Active sites are done; a site being removed, or whose removal
		// failed, is never provisioned again (review I2).
		switch ps[i].Status {
		case store.PlacementActive, store.PlacementRemoving, store.PlacementRemoveFailed:
			continue
		}
		pr, err := s.prepare(ctx, &ps[i])
		if err != nil {
			s.failPlacement(ctx, &ps[i], err)
			failures = append(failures, fmt.Sprintf("site %s: %v", s.SiteCode(ps[i].SiteID), err))
			continue
		}
		ready = append(ready, pr)
	}
	if len(ready) == 0 && len(failures) > 0 {
		return fail(errors.New(strings.Join(failures, "; ")))
	}
	group := s.groupName(id)
	if err := s.record(ctx, id, "", "keycloak-group", func(ctx context.Context) (string, error) {
		g, err := s.kc.FindGroup(ctx, group)
		if errors.Is(err, keycloak.ErrNotFound) {
			g, err = s.kc.CreateGroup(ctx, group)
		}
		if err != nil {
			return "", err
		}
		t.KeycloakGroupID = g.ID
		return g.ID, s.st.UpdateTenant(ctx, t)
	}); err != nil {
		return fail(err)
	}
	for _, pr := range ready {
		if err := s.provisionPlacement(ctx, t, pr); err != nil {
			failures = append(failures, fmt.Sprintf("site %s: %v", s.SiteCode(pr.p.SiteID), err))
		}
	}
	if len(failures) > 0 {
		return fail(errors.New(strings.Join(failures, "; ")))
	}
	if keepStatus {
		return nil
	}
	t.Status, t.LastError = store.StatusActive, ""
	return s.st.UpdateTenant(ctx, t)
}

// prepared is a placement whose site passed the preconditions.
type prepared struct {
	p    *store.Placement
	site Site
	idp  *nutanix.SAMLIdP
	role *nutanix.Role
}

// prepare checks a placement's site, identity provider and role.
func (s *Service) prepare(ctx context.Context, p *store.Placement) (prepared, error) {
	site, err := s.site(p.SiteID)
	if err != nil {
		return prepared{}, err
	}
	idp, err := s.idp(ctx, site)
	if err != nil {
		return prepared{}, err
	}
	role, err := site.PC.FindRole(ctx, s.cfg.RoleName)
	if err != nil {
		return prepared{}, fmt.Errorf("role %q: %w", s.cfg.RoleName, err)
	}
	return prepared{p: p, site: site, idp: idp, role: role}, nil
}

// failPlacement records a placement's failure.
func (s *Service) failPlacement(ctx context.Context, p *store.Placement, err error) {
	p.Status, p.LastError = store.PlacementFailed, err.Error()
	if uerr := s.st.UpdatePlacement(ctx, p); uerr != nil {
		s.log.Warn("placement update failed", "tenant", p.TenantID, "site", p.SiteID, "err", uerr.Error())
	}
}

// provisionPlacement runs the 2a steps on one prepared site, recording
// each step and the placement's identifiers; the placement ends active
// or failed.
func (s *Service) provisionPlacement(ctx context.Context, t *store.Tenant, pr prepared) error {
	p, pc, idp, role := pr.p, pr.site.PC, pr.idp, pr.role
	fail := func(err error) error {
		s.failPlacement(ctx, p, err)
		return err
	}
	p.Status, p.LastError = store.PlacementCreating, ""
	if err := s.st.UpdatePlacement(ctx, p); err != nil {
		return err
	}
	group := s.groupName(t.ID)
	project := s.projectName(t.ID)
	steps := []struct {
		name string
		fn   func(ctx context.Context) (string, error)
	}{
		{"project", func(ctx context.Context) (string, error) {
			pr, err := pc.FindProject(ctx, project)
			if nutanix.IsNotFound(err) {
				pr, err = pc.CreateProject(ctx, project, project, "Portico tenant "+t.DisplayName)
			}
			if err != nil {
				return "", err
			}
			if pr.IsDefault {
				return "", fmt.Errorf("project %q is the default project; Portico never uses it", project)
			}
			p.ProjectID, p.ProjectExtID = project, pr.ExtID
			return pr.ExtID, nil
		}},
		{"resource-group", func(ctx context.Context) (string, error) {
			rgs, err := pc.ListResourceGroups(ctx, p.ProjectExtID)
			if err != nil {
				return "", err
			}
			if len(rgs) == 0 {
				rg, err := pc.CreateResourceGroup(ctx, project+"-rg", p.ProjectExtID, p.ClusterExtID, p.StorageContainerExtID)
				if err != nil {
					return "", err
				}
				rgs = []nutanix.ResourceGroup{*rg}
			}
			p.ResourceGroupExtID = rgs[0].ExtID
			return rgs[0].ExtID, nil
		}},
		{"saml-group", func(ctx context.Context) (string, error) {
			g, err := pc.FindUserGroup(ctx, group)
			if nutanix.IsNotFound(err) {
				g, err = pc.CreateUserGroup(ctx, group, idp.ExtID)
			}
			if err != nil {
				return "", err
			}
			p.SAMLGroupExtID = g.ExtID
			return g.ExtID, nil
		}},
		{"idp-share", func(ctx context.Context) (string, error) {
			cur, err := pc.GetSAMLIdP(ctx, idp.ExtID)
			if err != nil {
				return "", err
			}
			if !cur.SharedWithAll && !contains(cur.SharedWithProjects, p.ProjectExtID) {
				if err := pc.ShareIdP(ctx, idp.ExtID, p.ProjectExtID); err != nil {
					return "", err
				}
			}
			p.IdPShared = true
			return idp.ExtID, nil
		}},
		{"role-membership", func(ctx context.Context) (string, error) {
			rms, err := pc.ListRoleMemberships(ctx)
			if err != nil {
				return "", err
			}
			for _, rm := range rms {
				if rm.IdentityExtID == p.SAMLGroupExtID && rm.ProjectExtID == p.ProjectExtID && rm.RoleExtID == role.ExtID {
					p.RoleMembershipExtID = rm.ExtID
					return rm.ExtID, nil
				}
			}
			rm, err := pc.CreateRoleMembership(ctx, p.SAMLGroupExtID, idp.ExtID, p.ProjectExtID, role.ExtID)
			if err != nil {
				return "", err
			}
			p.RoleMembershipExtID = rm.ExtID
			return rm.ExtID, nil
		}},
		{"visibility", func(ctx context.Context) (string, error) {
			v, err := s.visibility(ctx, pc, p)
			if err != nil {
				return "", err
			}
			if !v.OK {
				return "", fmt.Errorf("visibility check: %v", v.Findings)
			}
			return "ok", nil
		}},
	}
	for _, st := range steps {
		if err := s.record(ctx, t.ID, p.SiteID, st.name, st.fn); err != nil {
			return fail(err)
		}
		if err := s.st.UpdatePlacement(ctx, p); err != nil {
			return fail(err)
		}
	}
	p.Status, p.LastError = store.PlacementActive, ""
	return s.st.UpdatePlacement(ctx, p)
}

// remove takes the tenant off every site, then deletes its Keycloak group
// and its row; the caller holds the lock. A site whose removal fails
// keeps its placement (failed) and the tenant ends failed.
func (s *Service) remove(ctx context.Context, id string, deleteProject bool) error {
	t, err := s.st.GetTenant(ctx, id)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		t.Status, t.LastError = store.StatusFailed, err.Error()
		if uerr := s.st.UpdateTenant(ctx, t); uerr != nil {
			s.log.Warn("tenant update failed", "tenant", id, "err", uerr.Error())
		}
		return err
	}
	ps, err := s.st.ListPlacements(ctx, id)
	if err != nil {
		return fail(err)
	}
	var failures []string
	for _, p := range ps {
		if err := s.removePlacement(ctx, id, p.SiteID, deleteProject); err != nil {
			failures = append(failures, fmt.Sprintf("site %s: %v", s.SiteCode(p.SiteID), err))
		}
	}
	if len(failures) > 0 {
		return fail(errors.New(strings.Join(failures, "; ")))
	}
	if err := s.record(ctx, id, "", "keycloak-group", func(ctx context.Context) (string, error) {
		if t.KeycloakGroupID == "" {
			return "keycloak group already absent", nil
		}
		if err := s.kc.DeleteGroup(ctx, t.KeycloakGroupID); err != nil {
			return "", err
		}
		t.KeycloakGroupID = ""
		return "keycloak group deleted", nil
	}); err != nil {
		return fail(err)
	}
	return s.st.DeleteTenant(ctx, id)
}

// removePlacement runs the deletion steps on one site in reverse order
// (unshare before delete) and deletes the placement; a 404 is recorded as
// "already absent". On failure the placement stays, failed.
func (s *Service) removePlacement(ctx context.Context, tenantID, siteID string, deleteProject bool) error {
	p, err := s.st.GetPlacement(ctx, tenantID, siteID)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		p.Status, p.LastError = store.PlacementRemoveFailed, err.Error()
		if uerr := s.st.UpdatePlacement(ctx, p); uerr != nil {
			s.log.Warn("placement update failed", "tenant", tenantID, "site", siteID, "err", uerr.Error())
		}
		return err
	}
	site, err := s.site(siteID)
	if err != nil {
		return fail(err)
	}
	pc := site.PC
	absent := func(what string) (string, error) { return what + " already absent", nil }
	tolerate := func(what string, err error) (string, error) {
		if err == nil {
			return what + " deleted", nil
		}
		if nutanix.IsNotFound(err) {
			return absent(what)
		}
		return "", err
	}
	var idp *nutanix.SAMLIdP
	if p.SAMLGroupExtID != "" || p.IdPShared {
		if idp, err = s.idp(ctx, site); err != nil {
			s.log.Warn("identity provider lookup during delete", "tenant", tenantID, "site", siteID, "err", err.Error())
		}
	}
	steps := []struct {
		name string
		fn   func(ctx context.Context) (string, error)
	}{
		{"role-membership", func(ctx context.Context) (string, error) {
			if p.SAMLGroupExtID == "" {
				return absent("role membership")
			}
			rms, err := pc.ListRoleMemberships(ctx)
			if err != nil {
				return "", err
			}
			n := 0
			for _, rm := range rms {
				if rm.IdentityExtID != p.SAMLGroupExtID {
					continue
				}
				if _, err := tolerate("role membership "+rm.ExtID, pc.DeleteRoleMembership(ctx, rm.ExtID)); err != nil {
					return "", err
				}
				n++
			}
			p.RoleMembershipExtID = ""
			if n == 0 {
				return absent("role membership")
			}
			return fmt.Sprintf("%d role membership(s) deleted", n), nil
		}},
		{"idp-unshare", func(ctx context.Context) (string, error) {
			if idp == nil || p.ProjectExtID == "" {
				return absent("idp sharing")
			}
			cur, err := pc.GetSAMLIdP(ctx, idp.ExtID)
			if nutanix.IsNotFound(err) {
				return absent("idp sharing")
			}
			if err != nil {
				return "", err
			}
			if !contains(cur.SharedWithProjects, p.ProjectExtID) {
				p.IdPShared = false
				return absent("idp sharing")
			}
			if _, err := tolerate("idp sharing", pc.UnshareIdP(ctx, idp.ExtID, p.ProjectExtID)); err != nil {
				return "", err
			}
			p.IdPShared = false
			return "idp unshared", nil
		}},
		{"saml-group", func(ctx context.Context) (string, error) {
			if p.SAMLGroupExtID == "" {
				return absent("saml group")
			}
			d, err := tolerate("saml group", pc.DeleteUserGroup(ctx, p.SAMLGroupExtID))
			if err == nil {
				p.SAMLGroupExtID = ""
			}
			return d, err
		}},
		{"resource-group", func(ctx context.Context) (string, error) {
			if !deleteProject || p.ProjectExtID == "" {
				return "kept", nil
			}
			rgs, err := pc.ListResourceGroups(ctx, p.ProjectExtID)
			if nutanix.IsNotFound(err) || (err == nil && len(rgs) == 0) {
				p.ResourceGroupExtID = ""
				return absent("resource group")
			}
			if err != nil {
				return "", err
			}
			for _, rg := range rgs {
				if _, err := tolerate("resource group "+rg.ExtID, pc.DeleteResourceGroup(ctx, rg.ExtID)); err != nil {
					return "", err
				}
			}
			p.ResourceGroupExtID = ""
			return fmt.Sprintf("%d resource group(s) deleted", len(rgs)), nil
		}},
		{"project", func(ctx context.Context) (string, error) {
			if !deleteProject || p.ProjectExtID == "" {
				return "kept", nil
			}
			if p.ProjectID != s.projectName(tenantID) {
				s.log.Warn("refusing to delete a project outside Portico's naming", "tenant", tenantID, "site", siteID, "project", p.ProjectID, "expected", s.projectName(tenantID))
				return "kept: refusing to delete project " + p.ProjectID + " (not " + s.projectName(tenantID) + ")", nil
			}
			d, err := tolerate("project", pc.DeleteProject(ctx, p.ProjectExtID))
			if err == nil {
				p.ProjectExtID = ""
			}
			return d, err
		}},
	}
	for _, st := range steps {
		if err := s.record(ctx, tenantID, siteID, st.name, st.fn); err != nil {
			return fail(err)
		}
		if err := s.st.UpdatePlacement(ctx, p); err != nil {
			return fail(err)
		}
	}
	return s.st.DeletePlacement(ctx, tenantID, siteID)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
