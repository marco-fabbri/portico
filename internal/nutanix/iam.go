package nutanix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// SAMLIdP is a SAML identity provider configured in Prism Central.
type SAMLIdP struct {
	ExtID              string   `json:"extId"`
	Name               string   `json:"name"`
	SharedWithAll      bool     `json:"isSharedWithAllProjects"`
	SharedWithProjects []string `json:"sharedWithProjects"`
}

// UserGroup is a SAML group known to Prism Central.
type UserGroup struct {
	ExtID string `json:"extId"`
	Name  string `json:"name"`
}

// Role is a Prism Central role.
type Role struct {
	ExtID       string `json:"extId"`
	DisplayName string `json:"displayName"`
}

// RoleMembership binds an identity to a role inside a project.
type RoleMembership struct {
	ExtID             string `json:"extId"`
	IdentityExtID     string `json:"identityExtId"`
	IdentityType      string `json:"identityType"`
	ProjectExtID      string `json:"projectExtId"`
	RoleExtID         string `json:"roleExtId"`
	ScopeTemplateName string `json:"scopeTemplateName"`
}

// ListSAMLIdPs returns every SAML identity provider.
func (c *Client) ListSAMLIdPs(ctx context.Context) ([]SAMLIdP, error) {
	var out []SAMLIdP
	err := c.listAll(ctx, PathIAM1+"/authn/saml-identity-providers", nil, &out)
	return out, err
}

// GetSAMLIdP returns one SAML identity provider.
func (c *Client) GetSAMLIdP(ctx context.Context, extID string) (*SAMLIdP, error) {
	var out SAMLIdP
	if _, err := c.do(ctx, http.MethodGet, PathIAM1+"/authn/saml-identity-providers/"+extID, "", nil, "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ShareIdP shares the identity provider with a project (singular
// projectExtId, write-up 05).
func (c *Client) ShareIdP(ctx context.Context, idpExtID, projectExtID string) error {
	return c.idpAction(ctx, idpExtID, "share", projectExtID)
}

// UnshareIdP reverses ShareIdP; required before the project is deleted.
func (c *Client) UnshareIdP(ctx context.Context, idpExtID, projectExtID string) error {
	return c.idpAction(ctx, idpExtID, "unshare", projectExtID)
}

func (c *Client) idpAction(ctx context.Context, idpExtID, action, projectExtID string) error {
	etag, err := c.etagOf(ctx, PathIAM1+"/authn/saml-identity-providers/"+idpExtID)
	if err != nil {
		return err
	}
	path := PathIAM1 + "/authn/config/saml-identity-provider/" + idpExtID + "/$actions/" + action
	_, err = c.do(ctx, http.MethodPost, path, "", map[string]string{"projectExtId": projectExtID}, etag, nil)
	return err
}

// FindUserGroup returns the SAML group with the given name.
func (c *Client) FindUserGroup(ctx context.Context, name string) (*UserGroup, error) {
	var groups []UserGroup
	if err := c.listAll(ctx, PathIAM0+"/authn/user-groups", nil, &groups); err != nil {
		return nil, err
	}
	for i := range groups {
		if groups[i].Name == name {
			return &groups[i], nil
		}
	}
	return nil, notFound("user group " + name)
}

// CreateUserGroup registers a SAML group for the identity provider. The
// name must equal the value Keycloak sends in the groups attribute.
func (c *Client) CreateUserGroup(ctx context.Context, name, idpExtID string) (*UserGroup, error) {
	body := map[string]string{"name": name, "groupType": "SAML", "idpId": idpExtID}
	var out UserGroup
	if _, err := c.do(ctx, http.MethodPost, PathIAM0+"/authn/user-groups", "", body, "", &out); err != nil {
		return nil, err
	}
	if out.ExtID == "" {
		return c.FindUserGroup(ctx, name)
	}
	return &out, nil
}

// DeleteUserGroup deletes a SAML group.
func (c *Client) DeleteUserGroup(ctx context.Context, extID string) error {
	return c.deleteSync(ctx, PathIAM0+"/authn/user-groups/"+extID)
}

// deleteSync does GET (ETag) → DELETE with If-Match for synchronous deletes.
func (c *Client) deleteSync(ctx context.Context, path string) error {
	etag, err := c.etagOf(ctx, path)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodDelete, path, "", nil, etag, nil)
	return err
}

// FindRole returns the role with the given display name.
func (c *Client) FindRole(ctx context.Context, displayName string) (*Role, error) {
	q := url.Values{}
	q.Set("$filter", "displayName eq '"+strings.ReplaceAll(displayName, "'", "''")+"'")
	var roles []Role
	if _, err := c.do(ctx, http.MethodGet, PathIAM0+"/authz/roles", q.Encode(), nil, "", &roles); err != nil {
		return nil, err
	}
	for i := range roles {
		if roles[i].DisplayName == displayName {
			return &roles[i], nil
		}
	}
	return nil, notFound("role " + displayName)
}

// ListRoleMemberships returns every role membership.
func (c *Client) ListRoleMemberships(ctx context.Context) ([]RoleMembership, error) {
	var out []RoleMembership
	err := c.listAll(ctx, PathIAM1+"/authz/role-memberships", nil, &out)
	return out, err
}

// CreateRoleMembership binds a SAML group to a role inside a project with
// the ProjectsScopeTemplate (payloads/role-membership-create.json).
func (c *Client) CreateRoleMembership(ctx context.Context, groupExtID, idpExtID, projectExtID, roleExtID string) (*RoleMembership, error) {
	body := map[string]any{
		"identityExtId":     groupExtID,
		"identityType":      "GROUP",
		"idpExtId":          idpExtID,
		"projectExtId":      projectExtID,
		"roleExtId":         roleExtID,
		"scopeTemplateName": ScopeTemplate,
		"scopeTemplateNameValues": []map[string]string{
			{"name": "projectExtId", "value": projectExtID},
		},
	}
	var out RoleMembership
	if _, err := c.do(ctx, http.MethodPost, PathIAM1+"/authz/role-memberships", "", body, "", &out); err != nil {
		return nil, err
	}
	if out.ProjectExtID == "" {
		out.IdentityExtID, out.IdentityType, out.ProjectExtID, out.RoleExtID, out.ScopeTemplateName = groupExtID, "GROUP", projectExtID, roleExtID, ScopeTemplate
	}
	return &out, nil
}

// DeleteRoleMembership deletes a role membership.
func (c *Client) DeleteRoleMembership(ctx context.Context, extID string) error {
	return c.deleteSync(ctx, PathIAM1+"/authz/role-memberships/"+extID)
}

// PoliciesMentioning returns the display names of the global authorization
// policies whose definition contains extID anywhere (the static visibility
// check uses it to assert a tenant group holds no global role).
func (c *Client) PoliciesMentioning(ctx context.Context, extID string) ([]string, error) {
	var policies []map[string]any
	if err := c.listAll(ctx, PathIAM1+"/authz/authorization-policies", nil, &policies); err != nil {
		return nil, err
	}
	var names []string
	for _, p := range policies {
		raw, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		if strings.Contains(string(raw), extID) {
			name, _ := p["displayName"].(string)
			names = append(names, name)
		}
	}
	return names, nil
}

// remarshal converts a generic decoded value into a typed one.
func remarshal(in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
