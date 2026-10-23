package keycloak

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// This file is the realm-administration side of the client, used by the
// bootstrap (design §4.4 and §4.5): realms, clients, their secrets and
// service accounts, client-role mappings, protocol mappers and the SAML
// descriptor. Every Ensure* call is idempotent so a bootstrap can be
// rerun after a partial failure.

// CreateRealm creates a realm from its representation (POST on the
// admin root). The client keeps administering its own realm.
func (c *Client) CreateRealm(ctx context.Context, rep map[string]any) error {
	_, _, err := c.do(ctx, http.MethodPost, "/admin/realms", nil, rep, nil)
	return err
}

// FindClient returns the client with exactly that clientId.
func (c *Client) FindClient(ctx context.Context, clientID string) (map[string]any, error) {
	var clients []map[string]any
	if _, _, err := c.do(ctx, http.MethodGet, "/clients", url.Values{"clientId": {clientID}}, nil, &clients); err != nil {
		return nil, err
	}
	for _, cl := range clients {
		if cl["clientId"] == clientID {
			return cl, nil
		}
	}
	return nil, ErrNotFound
}

// DeleteClient deletes the client with that clientId; an absent client is
// not an error.
func (c *Client) DeleteClient(ctx context.Context, clientID string) error {
	cl, err := c.FindClient(ctx, clientID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	id, _ := cl["id"].(string)
	if _, status, err := c.do(ctx, http.MethodDelete, "/clients/"+url.PathEscape(id), nil, nil, nil); err != nil && status != http.StatusNotFound {
		return err
	}
	return nil
}

// EnsureClient creates the client described by rep (keyed by its
// clientId) or updates the existing one with rep. It returns the
// client's internal id and whether it was created.
func (c *Client) EnsureClient(ctx context.Context, rep map[string]any) (string, bool, error) {
	clientID, _ := rep["clientId"].(string)
	if clientID == "" {
		return "", false, errors.New("keycloak: client representation without clientId")
	}
	existing, err := c.FindClient(ctx, clientID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", false, err
	}
	if err == nil {
		id, _ := existing["id"].(string)
		upd := make(map[string]any, len(rep)+1)
		for k, v := range rep {
			upd[k] = v
		}
		upd["id"] = id
		if _, _, err := c.do(ctx, http.MethodPut, "/clients/"+url.PathEscape(id), nil, upd, nil); err != nil {
			return "", false, err
		}
		return id, false, nil
	}
	loc, _, err := c.do(ctx, http.MethodPost, "/clients", nil, rep, nil)
	if err != nil {
		return "", false, err
	}
	if i := strings.LastIndex(loc, "/"); i >= 0 && i < len(loc)-1 {
		return loc[i+1:], true, nil
	}
	created, err := c.FindClient(ctx, clientID)
	if err != nil {
		return "", false, fmt.Errorf("keycloak: client %q created but not found: %w", clientID, err)
	}
	id, _ := created["id"].(string)
	return id, true, nil
}

// ClientSecret returns the secret of a confidential client.
func (c *Client) ClientSecret(ctx context.Context, id string) (string, error) {
	var out struct {
		Value string `json:"value"`
	}
	if _, _, err := c.do(ctx, http.MethodGet, "/clients/"+url.PathEscape(id)+"/client-secret", nil, nil, &out); err != nil {
		return "", err
	}
	if out.Value == "" {
		return "", errors.New("keycloak: the client has no secret")
	}
	return out.Value, nil
}

// ServiceAccountUserID returns the id of the user behind a client's
// service account.
func (c *Client) ServiceAccountUserID(ctx context.Context, clientUUID string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if _, _, err := c.do(ctx, http.MethodGet, "/clients/"+url.PathEscape(clientUUID)+"/service-account-user", nil, nil, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("keycloak: the client has no service account")
	}
	return out.ID, nil
}

// roleRep is a role as the admin API returns it.
type roleRep struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// EnsureClientRoles maps on the user every role of the target client
// (by clientId, e.g. realm-management) it does not hold yet, and returns
// the names it added. A role that does not exist is ErrNotFound.
func (c *Client) EnsureClientRoles(ctx context.Context, userID, targetClientID string, roles []string) ([]string, error) {
	target, err := c.FindClient(ctx, targetClientID)
	if err != nil {
		return nil, fmt.Errorf("keycloak: client %q: %w", targetClientID, err)
	}
	cid, _ := target["id"].(string)
	path := "/users/" + url.PathEscape(userID) + "/role-mappings/clients/" + url.PathEscape(cid)
	var mapped []roleRep
	if _, _, err := c.do(ctx, http.MethodGet, path, nil, nil, &mapped); err != nil {
		return nil, err
	}
	held := map[string]bool{}
	for _, r := range mapped {
		held[r.Name] = true
	}
	var reps []roleRep
	var added []string
	for _, name := range roles {
		if held[name] {
			continue
		}
		var r roleRep
		if _, _, err := c.do(ctx, http.MethodGet, "/clients/"+url.PathEscape(cid)+"/roles/"+url.PathEscape(name), nil, nil, &r); err != nil {
			return nil, fmt.Errorf("keycloak: role %q of %q: %w", name, targetClientID, err)
		}
		reps = append(reps, r)
		added = append(added, name)
	}
	if len(reps) == 0 {
		return nil, nil
	}
	if _, _, err := c.do(ctx, http.MethodPost, path, nil, reps, nil); err != nil {
		return nil, err
	}
	return added, nil
}

// EnsureProtocolMappers adds to the client every mapper (by name) it
// does not have and returns the names it added.
func (c *Client) EnsureProtocolMappers(ctx context.Context, clientUUID string, mappers []map[string]any) ([]string, error) {
	path := "/clients/" + url.PathEscape(clientUUID) + "/protocol-mappers/models"
	var existing []map[string]any
	if _, _, err := c.do(ctx, http.MethodGet, path, nil, nil, &existing); err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, m := range existing {
		if n, _ := m["name"].(string); n != "" {
			have[n] = true
		}
	}
	var added []string
	for _, m := range mappers {
		name, _ := m["name"].(string)
		if name == "" || have[name] {
			continue
		}
		if _, _, err := c.do(ctx, http.MethodPost, path, nil, m, nil); err != nil {
			return added, err
		}
		added = append(added, name)
	}
	return added, nil
}

// SAMLDescriptor fetches the realm's SAML identity-provider metadata
// (public endpoint, no token). Keycloak writes its public host name into
// it (KC_HOSTNAME), so the caller checks the entityID before trusting
// it (design §4.4).
func (c *Client) SAMLDescriptor(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/realms/"+url.PathEscape(c.realm)+"/protocol/saml/descriptor", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.log.Warn("keycloak descriptor request failed", "err", err.Error())
		return nil, fmt.Errorf("keycloak: saml descriptor: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("keycloak: saml descriptor: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keycloak: saml descriptor: status %d", resp.StatusCode)
	}
	return raw, nil
}
