package tenant

import (
	"context"
	"fmt"

	"github.com/marco-fabbri/portico/internal/nutanix"
	"github.com/marco-fabbri/portico/internal/store"
)

// Visibility runs the static visibility check of design §4.4 on every
// site of a tenant: the project exists, the SAML group holds exactly one
// project-scoped role membership and it is in this project, and no global
// authorization policy mentions the group. Each violation is one finding;
// the result is keyed by site id.
func (s *Service) Visibility(ctx context.Context, id string) (map[string]*Visibility, error) {
	if _, err := s.st.GetTenant(ctx, id); err != nil {
		return nil, err
	}
	ps, err := s.st.ListPlacements(ctx, id)
	if err != nil {
		return nil, err
	}
	out := map[string]*Visibility{}
	for i := range ps {
		site, err := s.site(ps[i].SiteID)
		if err != nil {
			out[ps[i].SiteID] = &Visibility{Findings: []string{err.Error()}}
			continue
		}
		v, err := s.visibility(ctx, site.PC, &ps[i])
		if err != nil {
			return nil, fmt.Errorf("site %s: %w", site.Code, err)
		}
		out[ps[i].SiteID] = v
	}
	return out, nil
}

func (s *Service) visibility(ctx context.Context, pc PrismCentral, t *store.Placement) (*Visibility, error) {
	v := &Visibility{Findings: []string{}}
	p, err := pc.FindProject(ctx, t.ProjectID)
	switch {
	case nutanix.IsNotFound(err):
		v.Findings = append(v.Findings, fmt.Sprintf("project %s does not exist in Prism Central", t.ProjectID))
	case err != nil:
		return nil, err
	case p.ExtID != t.ProjectExtID:
		v.Findings = append(v.Findings, fmt.Sprintf("project %s has extId %s, the record says %s", t.ProjectID, p.ExtID, t.ProjectExtID))
	}
	rms, err := pc.ListRoleMemberships(ctx)
	if err != nil {
		return nil, err
	}
	inProject := 0
	for _, rm := range rms {
		if rm.IdentityExtID != t.SAMLGroupExtID {
			continue
		}
		if rm.ProjectExtID != t.ProjectExtID {
			v.Findings = append(v.Findings, fmt.Sprintf("group %s has a role membership %s in another project %s", t.SAMLGroupExtID, rm.ExtID, rm.ProjectExtID))
			continue
		}
		if rm.ScopeTemplateName != nutanix.ScopeTemplate {
			v.Findings = append(v.Findings, fmt.Sprintf("role membership %s is not project-scoped (scope template %q)", rm.ExtID, rm.ScopeTemplateName))
			continue
		}
		inProject++
	}
	if inProject != 1 {
		v.Findings = append(v.Findings, fmt.Sprintf("group %s has %d project-scoped role memberships in project %s, expected 1", t.SAMLGroupExtID, inProject, t.ProjectExtID))
	}
	if t.SAMLGroupExtID != "" {
		names, err := pc.PoliciesMentioning(ctx, t.SAMLGroupExtID)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			v.Findings = append(v.Findings, fmt.Sprintf("global authorization policy %q mentions group %s", n, t.SAMLGroupExtID))
		}
	}
	v.OK = len(v.Findings) == 0
	return v, nil
}
