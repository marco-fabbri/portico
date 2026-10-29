package nutanix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// This file is the role side of the client: the custom roles the site
// setup makes (tenant networking design 4.3 and 4.5), as
// lab/auth/networking-role.sh does them on pc.7.6.

// RoleDetail is a role with its operations, by extId.
type RoleDetail struct {
	ExtID       string   `json:"extId"`
	DisplayName string   `json:"displayName"`
	Description string   `json:"description"`
	Operations  []string `json:"operations"`
}

// roleReadOnly are the fields of a role GET that Prism Central sets
// itself; the update leaves them out (the Role model of the v4 IAM API).
var roleReadOnly = []string{"$reserved", "$objectType", "$unknownFields", "links", "tenantId", "extId", "createdBy", "createdTime", "lastUpdatedTime",
	"isSystemDefined", "clientName", "assignedUsersCount", "assignedUserGroupsCount", "accessibleClients", "accessibleClientsCount",
	"accessibleEntityTypes", "accessibleEntityTypesCount", "operationSchemaChangeImpact"}

// OperationExtIDs returns the extIds of the named operations, by name; a
// name this Prism Central does not list is absent from the map.
func (c *Client) OperationExtIDs(ctx context.Context, names []string) (map[string]string, error) {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "'" + strings.ReplaceAll(n, "'", "''") + "'"
	}
	q := url.Values{}
	q.Set("$filter", "displayName in ("+strings.Join(quoted, ",")+")")
	q.Set("$limit", "100")
	var ops []struct {
		ExtID       string `json:"extId"`
		DisplayName string `json:"displayName"`
	}
	if _, err := c.do(ctx, http.MethodGet, PathIAM0+"/authz/operations", q.Encode(), nil, "", &ops); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, o := range ops {
		out[o.DisplayName] = o.ExtID
	}
	return out, nil
}

// GetRole returns a role with its operations.
func (c *Client) GetRole(ctx context.Context, extID string) (*RoleDetail, error) {
	var r RoleDetail
	if _, err := c.do(ctx, http.MethodGet, PathIAM1+"/authz/roles/"+extID, "", nil, "", &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// CreateRole creates a custom role with the operations, by extId.
func (c *Client) CreateRole(ctx context.Context, name, description string, operations []string) (*RoleDetail, error) {
	body := map[string]any{"displayName": name, "description": description, "operations": operations}
	var r RoleDetail
	if _, err := c.do(ctx, http.MethodPost, PathIAM1+"/authz/roles", "", body, "", &r); err != nil {
		return nil, err
	}
	if r.ExtID == "" {
		return nil, errors.New("prism central: the role creation returned no extId")
	}
	return &r, nil
}

// UpdateRoleOperations replaces a role's operations: the role as read,
// without the fields Prism Central sets, PUT back with If-Match.
func (c *Client) UpdateRoleOperations(ctx context.Context, extID string, operations []string) error {
	path := PathIAM1 + "/authz/roles/" + extID
	var role map[string]any
	etag, err := c.do(ctx, http.MethodGet, path, "", nil, "", &role)
	if err != nil {
		return err
	}
	if etag == "" {
		return errors.New("prism central: GET " + path + " returned no ETag")
	}
	for _, k := range roleReadOnly {
		delete(role, k)
	}
	role["operations"] = operations
	_, err = c.do(ctx, http.MethodPut, path, "", role, etag, nil)
	return err
}

// DeleteRole deletes a custom role.
func (c *Client) DeleteRole(ctx context.Context, extID string) error {
	return c.deleteSync(ctx, PathIAM1+"/authz/roles/"+extID)
}

// CreateAuthorizationPolicy binds the role to one user on every entity
// of the given types.
func (c *Client) CreateAuthorizationPolicy(ctx context.Context, name, description, roleExtID, userExtID string, entityTypes []string) (*AuthorizationPolicy, error) {
	entities := make([]any, len(entityTypes))
	for i, t := range entityTypes {
		entities[i] = map[string]any{"entityFilter": map[string]any{t: map[string]any{"*": map[string]any{"eq": "*"}}}}
	}
	body := map[string]any{
		"displayName": name, "description": description,
		"authorizationPolicyType": "USER_DEFINED", "role": roleExtID,
		"identities": []any{map[string]any{"identityFilter": map[string]any{"user": map[string]any{"uuid": map[string]any{"anyof": []string{userExtID}}}}}},
		"entities":   entities,
	}
	var pol AuthorizationPolicy
	if _, err := c.do(ctx, http.MethodPost, PathIAM1+"/authz/authorization-policies", "", body, "", &pol); err != nil {
		return nil, err
	}
	if pol.ExtID == "" {
		return nil, errors.New("prism central: the policy creation returned no extId")
	}
	return &pol, nil
}

// DeleteAuthorizationPolicy deletes a policy.
func (c *Client) DeleteAuthorizationPolicy(ctx context.Context, extID string) error {
	return c.deleteSync(ctx, PathIAM1+"/authz/authorization-policies/"+extID)
}

// Names reports whether the policy's identities name the user directly.
func (p *AuthorizationPolicy) Names(userExtID string) bool {
	return userExtID != "" && bytes.Contains(p.Identities, []byte(`"`+userExtID+`"`))
}

// EntityTypes returns the entity types the policy's filters name.
func (p *AuthorizationPolicy) EntityTypes() []string {
	var entities []struct {
		EntityFilter map[string]json.RawMessage `json:"entityFilter"`
	}
	if len(p.Entities) == 0 || json.Unmarshal(p.Entities, &entities) != nil {
		return nil
	}
	var out []string
	for _, e := range entities {
		for t := range e.EntityFilter {
			out = append(out, t)
		}
	}
	return out
}
