package nutanix

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// This file is the bootstrap side of the client (design §4.5): the
// service account, its API key and its membership in the Project Manager
// policy, measured in write-up 07 and scripted in
// lab/auth/create-service-account.sh.

// User is a Prism Central user (local, SAML or service account).
type User struct {
	ExtID    string `json:"extId"`
	Username string `json:"username"`
	UserType string `json:"userType"`
}

// APIKey is a key issued to a service account. Key is only ever
// returned by the create call: Prism Central never shows it again.
type APIKey struct {
	ExtID      string
	Name       string
	Key        string
	ExpiryTime time.Time
}

// AuthorizationPolicy is a global authorization policy.
type AuthorizationPolicy struct {
	ExtID       string `json:"extId"`
	DisplayName string `json:"displayName"`
}

// FindUser returns the user with exactly that username.
func (c *Client) FindUser(ctx context.Context, username string) (*User, error) {
	q := url.Values{}
	q.Set("$filter", "username eq '"+strings.ReplaceAll(username, "'", "''")+"'")
	var users []User
	if _, err := c.do(ctx, http.MethodGet, PathIAM0+"/authn/users", q.Encode(), nil, "", &users); err != nil {
		return nil, err
	}
	for i := range users {
		if users[i].Username == username {
			return &users[i], nil
		}
	}
	return nil, notFound("user " + username)
}

// CreateServiceAccount creates a SERVICE_ACCOUNT user
// (payloads/service-account-create.json).
func (c *Client) CreateServiceAccount(ctx context.Context, username, displayName, description, email string) (*User, error) {
	body := map[string]string{"username": username, "userType": "SERVICE_ACCOUNT", "displayName": displayName, "description": description, "emailId": email}
	var out User
	if _, err := c.do(ctx, http.MethodPost, PathIAM1+"/authn/users", "", body, "", &out); err != nil {
		return nil, err
	}
	if out.ExtID == "" {
		return c.FindUser(ctx, username)
	}
	return &out, nil
}

// CreateAPIKey issues an API key to the user
// (payloads/service-account-key-create.json).
func (c *Client) CreateAPIKey(ctx context.Context, userExtID, name, description string) (*APIKey, error) {
	body := map[string]string{"name": name, "keyType": "API_KEY", "description": description}
	var out struct {
		ExtID      string `json:"extId"`
		Name       string `json:"name"`
		ExpiryTime string `json:"expiryTime"`
		KeyDetails struct {
			APIKey string `json:"apiKey"`
		} `json:"keyDetails"`
	}
	if _, err := c.do(ctx, http.MethodPost, PathIAM1+"/authn/users/"+userExtID+"/keys", "", body, "", &out); err != nil {
		return nil, err
	}
	if out.KeyDetails.APIKey == "" {
		return nil, errors.New("prism central: the key creation returned no key value")
	}
	k := &APIKey{ExtID: out.ExtID, Name: out.Name, Key: out.KeyDetails.APIKey}
	if out.ExpiryTime != "" {
		k.ExpiryTime, _ = time.Parse(time.RFC3339, out.ExpiryTime)
	}
	return k, nil
}

// ListAPIKeys lists the user's keys (ext id, name, expiry; never the key
// value, which Prism Central shows once at creation).
func (c *Client) ListAPIKeys(ctx context.Context, userExtID string) ([]APIKey, error) {
	var raw []struct {
		ExtID      string `json:"extId"`
		Name       string `json:"name"`
		ExpiryTime string `json:"expiryTime"`
	}
	if err := c.listAll(ctx, PathIAM1+"/authn/users/"+userExtID+"/keys", nil, &raw); err != nil {
		return nil, err
	}
	keys := make([]APIKey, 0, len(raw))
	for _, r := range raw {
		k := APIKey{ExtID: r.ExtID, Name: r.Name}
		if r.ExpiryTime != "" {
			k.ExpiryTime, _ = time.Parse(time.RFC3339, r.ExpiryTime)
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// DeleteAPIKey revokes a key (GET for the ETag, DELETE with If-Match).
func (c *Client) DeleteAPIKey(ctx context.Context, userExtID, keyExtID string) error {
	return c.deleteSync(ctx, PathIAM1+"/authn/users/"+userExtID+"/keys/"+keyExtID)
}

// FindAuthorizationPolicy returns the policy with that display name.
func (c *Client) FindAuthorizationPolicy(ctx context.Context, displayName string) (*AuthorizationPolicy, error) {
	var policies []AuthorizationPolicy
	if err := c.listAll(ctx, PathIAM1+"/authz/authorization-policies", nil, &policies); err != nil {
		return nil, err
	}
	for i := range policies {
		if policies[i].DisplayName == displayName {
			return &policies[i], nil
		}
	}
	return nil, notFound("authorization policy " + displayName)
}

// policyReadOnly are the fields a policy GET returns that a PUT rejects
// (payloads/authorization-policy-update.json).
var policyReadOnly = []string{"$reserved", "$objectType", "links", "tenantId", "createdBy", "createdTime", "lastUpdatedTime", "extId", "isSystemDefined", "clientName", "assignedUserGroupsCount", "assignedUsersCount"}

// AddPolicyUser appends the user to the first user identity filter of
// the policy and PUTs it back with If-Match. It reports whether the user
// was added (false when already a member, with no write).
func (c *Client) AddPolicyUser(ctx context.Context, policyExtID, userExtID string) (bool, error) {
	path := PathIAM1 + "/authz/authorization-policies/" + policyExtID
	var pol map[string]any
	etag, err := c.do(ctx, http.MethodGet, path, "", nil, "", &pol)
	if err != nil {
		return false, err
	}
	if etag == "" {
		return false, errors.New("prism central: GET " + path + " returned no ETag")
	}
	identities, _ := pol["identities"].([]any)
	var anyof []any
	var holder map[string]any
	for _, id := range identities {
		m, _ := id.(map[string]any)
		filter, _ := m["identityFilter"].(map[string]any)
		user, _ := filter["user"].(map[string]any)
		uuid, _ := user["uuid"].(map[string]any)
		if list, ok := uuid["anyof"].([]any); ok {
			anyof, holder = list, uuid
			break
		}
	}
	if holder == nil {
		return false, errors.New("prism central: policy has no user identity filter")
	}
	for _, v := range anyof {
		if v == userExtID {
			return false, nil
		}
	}
	holder["anyof"] = append(anyof, userExtID)
	for _, k := range policyReadOnly {
		delete(pol, k)
	}
	if entities, ok := pol["entities"].([]any); ok {
		for _, e := range entities {
			if m, ok := e.(map[string]any); ok {
				delete(m, "$reserved")
			}
		}
	}
	if _, err := c.do(ctx, http.MethodPut, path, "", pol, etag, nil); err != nil {
		return false, err
	}
	return true, nil
}
