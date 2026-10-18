package tenant

import (
	"context"
	"errors"
	"fmt"

	"github.com/marco-fabbri/portico/internal/idp/keycloak"
	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/store"
)

// stepFn performs one step and returns a detail for the record.
type stepFn func(ctx context.Context, t *store.Tenant) (detail string, err error)

// step runs fn, records the outcome and persists the tenant row.
func (s *Service) step(ctx context.Context, t *store.Tenant, name string, fn stepFn) error {
	detail, err := fn(ctx, t)
	rec := store.Step{TenantID: t.ID, Name: name, OK: err == nil, Detail: detail}
	if err != nil {
		rec.Detail = err.Error()
	}
	if aerr := s.st.AddStep(ctx, rec); aerr != nil {
		s.log.Warn("step record failed", "tenant", t.ID, "step", name, "err", aerr.Error())
	}
	s.log.Info("tenant step", "tenant", t.ID, "step", name, "ok", err == nil, "detail", rec.Detail)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return s.st.UpdateTenant(ctx, t)
}

// idp returns the single SAML identity provider named in the settings.
func (s *Service) idp(ctx context.Context) (*nutanix.SAMLIdP, error) {
	idps, err := s.pc.ListSAMLIdPs(ctx)
	if err != nil {
		return nil, err
	}
	var found []nutanix.SAMLIdP
	for _, i := range idps {
		if i.Name == s.cfg.SAMLIdPName {
			found = append(found, i)
		}
	}
	switch len(found) {
	case 1:
		return &found[0], nil
	case 0:
		return nil, fmt.Errorf("SAML identity provider %q not found in Prism Central", s.cfg.SAMLIdPName)
	default:
		return nil, fmt.Errorf("%d SAML identity providers named %q in Prism Central, expected one", len(found), s.cfg.SAMLIdPName)
	}
}

// provision runs the steps in order; the caller holds the tenant lock.
func (s *Service) provision(ctx context.Context, id string) error {
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
	// Preconditions before any write: the identity provider and the role.
	idp, err := s.idp(ctx)
	if err != nil {
		return fail(err)
	}
	role, err := s.pc.FindRole(ctx, s.cfg.RoleName)
	if err != nil {
		return fail(fmt.Errorf("role %q: %w", s.cfg.RoleName, err))
	}
	group := s.groupName(id)
	project := s.projectName(id)

	steps := []struct {
		name string
		fn   stepFn
	}{
		{"keycloak-group", func(ctx context.Context, t *store.Tenant) (string, error) {
			g, err := s.kc.FindGroup(ctx, group)
			if errors.Is(err, keycloak.ErrNotFound) {
				g, err = s.kc.CreateGroup(ctx, group)
			}
			if err != nil {
				return "", err
			}
			t.KeycloakGroupID = g.ID
			return g.ID, nil
		}},
		{"project", func(ctx context.Context, t *store.Tenant) (string, error) {
			p, err := s.pc.FindProject(ctx, project)
			if nutanix.IsNotFound(err) {
				p, err = s.pc.CreateProject(ctx, project, project, "Portico tenant "+t.DisplayName)
			}
			if err != nil {
				return "", err
			}
			if p.IsDefault {
				return "", fmt.Errorf("project %q is the default project; Portico never uses it", project)
			}
			t.ProjectID, t.ProjectExtID = project, p.ExtID
			return p.ExtID, nil
		}},
		{"resource-group", func(ctx context.Context, t *store.Tenant) (string, error) {
			rgs, err := s.pc.ListResourceGroups(ctx, t.ProjectExtID)
			if err != nil {
				return "", err
			}
			if len(rgs) == 0 {
				rg, err := s.pc.CreateResourceGroup(ctx, project+"-rg", t.ProjectExtID, s.cfg.ClusterExtID, s.cfg.StorageContainerExtID)
				if err != nil {
					return "", err
				}
				rgs = []nutanix.ResourceGroup{*rg}
			}
			t.ResourceGroupExtID = rgs[0].ExtID
			return rgs[0].ExtID, nil
		}},
		{"saml-group", func(ctx context.Context, t *store.Tenant) (string, error) {
			g, err := s.pc.FindUserGroup(ctx, group)
			if nutanix.IsNotFound(err) {
				g, err = s.pc.CreateUserGroup(ctx, group, idp.ExtID)
			}
			if err != nil {
				return "", err
			}
			t.SAMLGroupExtID = g.ExtID
			return g.ExtID, nil
		}},
		{"idp-share", func(ctx context.Context, t *store.Tenant) (string, error) {
			cur, err := s.pc.GetSAMLIdP(ctx, idp.ExtID)
			if err != nil {
				return "", err
			}
			if !cur.SharedWithAll && !contains(cur.SharedWithProjects, t.ProjectExtID) {
				if err := s.pc.ShareIdP(ctx, idp.ExtID, t.ProjectExtID); err != nil {
					return "", err
				}
			}
			t.IdPShared = true
			return idp.ExtID, nil
		}},
		{"role-membership", func(ctx context.Context, t *store.Tenant) (string, error) {
			rms, err := s.pc.ListRoleMemberships(ctx)
			if err != nil {
				return "", err
			}
			for _, rm := range rms {
				if rm.IdentityExtID == t.SAMLGroupExtID && rm.ProjectExtID == t.ProjectExtID && rm.RoleExtID == role.ExtID {
					t.RoleMembershipExtID = rm.ExtID
					return rm.ExtID, nil
				}
			}
			rm, err := s.pc.CreateRoleMembership(ctx, t.SAMLGroupExtID, idp.ExtID, t.ProjectExtID, role.ExtID)
			if err != nil {
				return "", err
			}
			t.RoleMembershipExtID = rm.ExtID
			return rm.ExtID, nil
		}},
		{"visibility", func(ctx context.Context, t *store.Tenant) (string, error) {
			v, err := s.visibility(ctx, t)
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
		if err := s.step(ctx, t, st.name, st.fn); err != nil {
			return fail(err)
		}
	}
	t.Status, t.LastError = store.StatusActive, ""
	return s.st.UpdateTenant(ctx, t)
}

// remove runs the deletion steps in reverse order; a 404 from either
// backend is recorded as "already absent". The caller holds the lock.
func (s *Service) remove(ctx context.Context, id string, deleteProject bool) error {
	t, err := s.st.GetTenant(ctx, id)
	if err != nil {
		return err
	}
	absent := func(what string) (string, error) { return what + " already absent", nil }
	tolerate := func(what string, err error) (string, error) {
		if err == nil {
			return what + " deleted", nil
		}
		if nutanix.IsNotFound(err) || errors.Is(err, keycloak.ErrNotFound) {
			return absent(what)
		}
		return "", err
	}
	var idp *nutanix.SAMLIdP
	if t.SAMLGroupExtID != "" || t.IdPShared {
		if idp, err = s.idp(ctx); err != nil {
			s.log.Warn("identity provider lookup during delete", "tenant", id, "err", err.Error())
		}
	}
	steps := []struct {
		name string
		fn   stepFn
	}{
		{"role-membership", func(ctx context.Context, t *store.Tenant) (string, error) {
			if t.SAMLGroupExtID == "" {
				return absent("role membership")
			}
			rms, err := s.pc.ListRoleMemberships(ctx)
			if err != nil {
				return "", err
			}
			n := 0
			for _, rm := range rms {
				if rm.IdentityExtID != t.SAMLGroupExtID {
					continue
				}
				if _, err := tolerate("role membership "+rm.ExtID, s.pc.DeleteRoleMembership(ctx, rm.ExtID)); err != nil {
					return "", err
				}
				n++
			}
			t.RoleMembershipExtID = ""
			if n == 0 {
				return absent("role membership")
			}
			return fmt.Sprintf("%d role membership(s) deleted", n), nil
		}},
		{"idp-unshare", func(ctx context.Context, t *store.Tenant) (string, error) {
			if idp == nil || t.ProjectExtID == "" {
				return absent("idp sharing")
			}
			cur, err := s.pc.GetSAMLIdP(ctx, idp.ExtID)
			if nutanix.IsNotFound(err) {
				return absent("idp sharing")
			}
			if err != nil {
				return "", err
			}
			if !contains(cur.SharedWithProjects, t.ProjectExtID) {
				t.IdPShared = false
				return absent("idp sharing")
			}
			if _, err := tolerate("idp sharing", s.pc.UnshareIdP(ctx, idp.ExtID, t.ProjectExtID)); err != nil {
				return "", err
			}
			t.IdPShared = false
			return "idp unshared", nil
		}},
		{"saml-group", func(ctx context.Context, t *store.Tenant) (string, error) {
			if t.SAMLGroupExtID == "" {
				return absent("saml group")
			}
			d, err := tolerate("saml group", s.pc.DeleteUserGroup(ctx, t.SAMLGroupExtID))
			if err == nil {
				t.SAMLGroupExtID = ""
			}
			return d, err
		}},
		{"resource-group", func(ctx context.Context, t *store.Tenant) (string, error) {
			if !deleteProject || t.ProjectExtID == "" {
				return "kept", nil
			}
			rgs, err := s.pc.ListResourceGroups(ctx, t.ProjectExtID)
			if nutanix.IsNotFound(err) || (err == nil && len(rgs) == 0) {
				t.ResourceGroupExtID = ""
				return absent("resource group")
			}
			if err != nil {
				return "", err
			}
			for _, rg := range rgs {
				if _, err := tolerate("resource group "+rg.ExtID, s.pc.DeleteResourceGroup(ctx, rg.ExtID)); err != nil {
					return "", err
				}
			}
			t.ResourceGroupExtID = ""
			return fmt.Sprintf("%d resource group(s) deleted", len(rgs)), nil
		}},
		{"project", func(ctx context.Context, t *store.Tenant) (string, error) {
			if !deleteProject || t.ProjectExtID == "" {
				return "kept", nil
			}
			if t.ProjectID != s.projectName(t.ID) {
				s.log.Warn("refusing to delete a project outside Portico's naming", "tenant", t.ID, "project", t.ProjectID, "expected", s.projectName(t.ID))
				return "kept: refusing to delete project " + t.ProjectID + " (not " + s.projectName(t.ID) + ")", nil
			}
			d, err := tolerate("project", s.pc.DeleteProject(ctx, t.ProjectExtID))
			if err == nil {
				t.ProjectExtID = ""
			}
			return d, err
		}},
		{"keycloak-group", func(ctx context.Context, t *store.Tenant) (string, error) {
			if t.KeycloakGroupID == "" {
				return absent("keycloak group")
			}
			if err := s.kc.DeleteGroup(ctx, t.KeycloakGroupID); err != nil {
				return "", err
			}
			t.KeycloakGroupID = ""
			return "keycloak group deleted", nil
		}},
	}
	for _, st := range steps {
		if err := s.step(ctx, t, st.name, st.fn); err != nil {
			t.Status, t.LastError = store.StatusFailed, err.Error()
			if uerr := s.st.UpdateTenant(ctx, t); uerr != nil {
				s.log.Warn("tenant update failed", "tenant", id, "err", uerr.Error())
			}
			return err
		}
	}
	return s.st.DeleteTenant(ctx, id)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
