// Package keycloak is Portico's Keycloak admin client: groups and users of
// the MSP realm, used by tenant provisioning (design §4.4).
//
// It authenticates as a confidential client with a service account
// (client_credentials) against Keycloak's loopback address; the realm
// roles that client needs are listed in deploy/portico.yaml.example. The
// client secret, the bearer token and any password never reach a log line.
package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned when a lookup finds nothing.
var ErrNotFound = errors.New("keycloak: not found")

// Group is a realm group.
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// User is a realm user.
type User struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Enabled   bool   `json:"enabled"`
}

// Client talks to one realm through the admin API.
type Client struct {
	base         string
	realm        string
	clientID     string
	clientSecret string
	http         *http.Client
	log          *slog.Logger

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// New returns a client for the realm at baseURL (scheme and host, usually
// the loopback). httpc may be nil for a 15 s default.
func New(baseURL, realm, clientID, clientSecret string, httpc *http.Client, log *slog.Logger) *Client {
	if httpc == nil {
		httpc = &http.Client{Timeout: 15 * time.Second}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Client{base: strings.TrimSuffix(baseURL, "/"), realm: realm, clientID: clientID, clientSecret: clientSecret, http: httpc, log: log}
}

// accessToken returns a cached client-credentials token, refreshing it 30 s
// before expiry.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expiry.Add(-30*time.Second)) {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {c.clientID}, "client_secret": {c.clientSecret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/realms/"+c.realm+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		c.log.Warn("keycloak token request failed", "err", err.Error())
		return "", fmt.Errorf("keycloak: token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The body may echo the request; only the status is reported.
		io.Copy(io.Discard, resp.Body)
		c.log.Warn("keycloak token request refused", "status", resp.StatusCode)
		return "", fmt.Errorf("keycloak: token: status %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		return "", errors.New("keycloak: token: malformed response")
	}
	c.token, c.expiry = tok.AccessToken, time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second)
	return c.token, nil
}

// do performs one admin call on a path relative to /admin/realms/<realm>.
// out (nil to discard) receives the JSON body. The Location header is
// returned for creates. A 401 (token invalidated server-side, for example
// after a Keycloak restart) drops the cached token and retries once.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any) (string, int, error) {
	loc, status, err := c.doOnce(ctx, method, path, query, body, out)
	if status == http.StatusUnauthorized {
		c.mu.Lock()
		c.token, c.expiry = "", time.Time{}
		c.mu.Unlock()
		return c.doOnce(ctx, method, path, query, body, out)
	}
	return loc, status, err
}

func (c *Client) doOnce(ctx context.Context, method, path string, query url.Values, body any, out any) (string, int, error) {
	tok, err := c.accessToken(ctx)
	if err != nil {
		return "", 0, err
	}
	u := c.base + "/admin/realms/" + c.realm + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return "", 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.log.Warn("keycloak call failed", "method", method, "path", path, "err", err.Error())
		return "", 0, fmt.Errorf("keycloak: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	c.log.Debug("keycloak call", "method", method, "path", path, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	if resp.StatusCode == http.StatusNotFound {
		return "", resp.StatusCode, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Error bodies are not logged or returned: Keycloak may echo request
		// fields (passwords included) in them.
		c.log.Warn("keycloak error", "method", method, "path", path, "status", resp.StatusCode)
		return "", resp.StatusCode, fmt.Errorf("keycloak: %s %s: status %d", method, path, resp.StatusCode)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return "", resp.StatusCode, fmt.Errorf("keycloak: %s %s: decode: %w", method, path, err)
		}
	}
	return resp.Header.Get("Location"), resp.StatusCode, nil
}

// FindGroup returns the top-level group with exactly that name.
func (c *Client) FindGroup(ctx context.Context, name string) (*Group, error) {
	var groups []Group
	if _, _, err := c.do(ctx, http.MethodGet, "/groups", url.Values{"search": {name}, "exact": {"true"}}, nil, &groups); err != nil {
		return nil, err
	}
	for i := range groups {
		if groups[i].Name == name {
			return &groups[i], nil
		}
	}
	return nil, ErrNotFound
}

// CreateGroup creates a top-level group and returns it.
func (c *Client) CreateGroup(ctx context.Context, name string) (*Group, error) {
	if _, _, err := c.do(ctx, http.MethodPost, "/groups", nil, map[string]string{"name": name}, nil); err != nil {
		return nil, err
	}
	return c.FindGroup(ctx, name)
}

// DeleteGroup deletes a group; an absent group is not an error.
func (c *Client) DeleteGroup(ctx context.Context, id string) error {
	_, _, err := c.do(ctx, http.MethodDelete, "/groups/"+url.PathEscape(id), nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// FindUserByEmail returns the user with exactly that email.
func (c *Client) FindUserByEmail(ctx context.Context, email string) (*User, error) {
	var users []User
	if _, _, err := c.do(ctx, http.MethodGet, "/users", url.Values{"email": {email}, "exact": {"true"}}, nil, &users); err != nil {
		return nil, err
	}
	for i := range users {
		if strings.EqualFold(users[i].Email, email) {
			return &users[i], nil
		}
	}
	return nil, ErrNotFound
}

// CreateUser creates an enabled user whose username is the email, with a
// temporary password and the UPDATE_PASSWORD required action.
func (c *Client) CreateUser(ctx context.Context, u User, temporaryPassword string) (*User, error) {
	body := map[string]any{
		"username":        u.Email,
		"email":           u.Email,
		"firstName":       u.FirstName,
		"lastName":        u.LastName,
		"enabled":         true,
		"emailVerified":   true,
		"requiredActions": []string{"UPDATE_PASSWORD"},
		"credentials":     []map[string]any{{"type": "password", "value": temporaryPassword, "temporary": true}},
	}
	loc, _, err := c.do(ctx, http.MethodPost, "/users", nil, body, nil)
	if err != nil {
		return nil, err
	}
	if i := strings.LastIndex(loc, "/"); i >= 0 && i < len(loc)-1 {
		var out User
		if _, _, err := c.do(ctx, http.MethodGet, "/users/"+loc[i+1:], nil, nil, &out); err == nil && out.ID != "" {
			return &out, nil
		}
	}
	return c.FindUserByEmail(ctx, u.Email)
}

// AddUserToGroup puts the user in the group (idempotent in Keycloak).
func (c *Client) AddUserToGroup(ctx context.Context, userID, groupID string) error {
	_, _, err := c.do(ctx, http.MethodPut, "/users/"+url.PathEscape(userID)+"/groups/"+url.PathEscape(groupID), nil, nil, nil)
	return err
}

// GroupMembers lists every member of a group, paging with first/max.
func (c *Client) GroupMembers(ctx context.Context, groupID string) ([]User, error) {
	const page = 100
	var all []User
	for first := 0; ; first += page {
		var users []User
		q := url.Values{"first": {strconv.Itoa(first)}, "max": {strconv.Itoa(page)}}
		if _, _, err := c.do(ctx, http.MethodGet, "/groups/"+url.PathEscape(groupID)+"/members", q, nil, &users); err != nil {
			return nil, err
		}
		all = append(all, users...)
		if len(users) < page {
			return all, nil
		}
	}
}

// SetRealmLoginTheme sets the realm's login theme, preserving the rest
// of the realm representation (GET, edit, PUT).
func (c *Client) SetRealmLoginTheme(ctx context.Context, theme string) error {
	var rep map[string]any
	if _, _, err := c.do(ctx, http.MethodGet, "", nil, nil, &rep); err != nil {
		return err
	}
	rep["loginTheme"] = theme
	_, _, err := c.do(ctx, http.MethodPut, "", nil, rep, nil)
	return err
}

// SetUserEnabled enables or disables a user.
func (c *Client) SetUserEnabled(ctx context.Context, userID string, enabled bool) error {
	var rep map[string]any
	if _, _, err := c.do(ctx, http.MethodGet, "/users/"+url.PathEscape(userID), nil, nil, &rep); err != nil {
		return err
	}
	rep["enabled"] = enabled
	_, _, err := c.do(ctx, http.MethodPut, "/users/"+url.PathEscape(userID), nil, rep, nil)
	return err
}
