// Package nutanix is Portico's Prism Central client: the v4 calls measured
// on pc.7.6.0.6 for tenant provisioning (docs/lab write-ups 04, 05, 07).
//
// Authentication is the service-account API key, sent as X-ntnx-api-key
// (design §4.5); the bootstrap alone uses an administrator's one-time
// credentials with Basic auth (NewWithPassword), never stored. Every write carries NTNX-Request-Id; every
// update, delete and $action carries If-Match with the ETag of a GET made
// just before. Long-running calls return a task that WaitTask polls.
package nutanix

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// API path prefixes pinned to the versions measured on pc.7.6.0.6.
const (
	PathMultidomain = "/api/multidomain/v4.4.b1"
	PathIAM0        = "/api/iam/v4.0"
	PathIAM1        = "/api/iam/v4.1.b3"
	PathTasks       = "/api/prism/v4.0/config/tasks"
	// ScopeTemplate scopes a role membership to one project; the name
	// cannot be discovered from the API (write-up 05).
	ScopeTemplate = "ProjectsScopeTemplate"
)

// APIError is a non-2xx answer from Prism Central.
type APIError struct {
	Status  int
	Message string
	// Code is the first error code of the answer (IAM-21016), if any.
	Code string
	// Task is set when a task that Prism Central accepted ended FAILED or
	// CANCELED: Prism Central refused the operation itself.
	Task bool
}

func (e *APIError) Error() string { return fmt.Sprintf("prism central: %d %s", e.Status, e.Message) }

// IsTaskFailed reports whether err is a task Prism Central ran and
// failed (a refusal of the operation, not a transport or API error).
func IsTaskFailed(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Task
}

// IsNotFound reports whether err is a 404 from Prism Central or a lookup
// that found nothing.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// notFound is the error returned by Find* helpers when nothing matches.
func notFound(what string) error {
	return &APIError{Status: http.StatusNotFound, Message: what + " not found"}
}

// Client talks to one Prism Central.
type Client struct {
	base     *url.URL
	auth     func(*http.Request) // sets the credential header(s)
	http     *http.Client
	log      *slog.Logger
	taskPoll time.Duration
}

// New returns a client for baseURL (scheme and host) using apiKey. tlsCfg
// may be nil for the default verification.
func New(baseURL, apiKey string, tlsCfg *tls.Config, log *slog.Logger) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("prism central: empty api key")
	}
	return newWithAuth(baseURL, func(r *http.Request) { r.Header.Set("X-Ntnx-Api-Key", apiKey) }, tlsCfg, log)
}

// NewWithPassword returns a client that authenticates with Basic auth:
// the bootstrap's one-time administrator credentials (design §4.5). They
// live in this client only, for the length of the bootstrap.
func NewWithPassword(baseURL, username, password string, tlsCfg *tls.Config, log *slog.Logger) (*Client, error) {
	if username == "" || password == "" {
		return nil, errors.New("prism central: empty username or password")
	}
	return newWithAuth(baseURL, func(r *http.Request) { r.SetBasicAuth(username, password) }, tlsCfg, log)
}

func newWithAuth(baseURL string, auth func(*http.Request), tlsCfg *tls.Config, log *slog.Logger) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("prism central: invalid url %q", baseURL)
	}
	if log == nil {
		log = slog.Default()
	}
	tr := &http.Transport{TLSClientConfig: tlsCfg, ForceAttemptHTTP2: false, MaxIdleConnsPerHost: 4}
	return &Client{
		base: &url.URL{Scheme: u.Scheme, Host: u.Host},
		auth: auth,
		// The lab Prism Central answered one call in 48 s (2026-10-18).
		http:     &http.Client{Transport: tr, Timeout: 60 * time.Second},
		log:      log,
		taskPoll: 5 * time.Second,
	}, nil
}

// CloseIdleConnections closes the client's idle connections, once a
// site's client is replaced (sites usability design 4.9).
func (c *Client) CloseIdleConnections() { c.http.CloseIdleConnections() }

// do performs one call. body (nil for none) is JSON-encoded; out (nil to
// discard) receives the "data" member of the envelope. The ETag header of
// the response is returned so a write can follow a GET.
func (c *Client) do(ctx context.Context, method, path, query string, body any, ifMatch string, out any) (string, error) {
	return c.call(ctx, method, path, query, body, ifMatch, out, nil)
}

// call is do that also reads metadata.totalAvailableResults into total
// when total is not nil (a count with $limit=1).
func (c *Client) call(ctx context.Context, method, path, query string, body any, ifMatch string, out any, total *int) (string, error) {
	u := *c.base
	u.Path, u.RawQuery = path, query
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	c.auth(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("NTNX-Request-Id", randomUUID())
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.log.Warn("prism central call failed", "method", method, "path", path, "err", err.Error())
		return "", fmt.Errorf("prism central: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("prism central: %s %s: read: %w", method, path, err)
	}
	c.log.Debug("prism central call", "method", method, "path", path, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, code := errorMessage(raw), errorCode(raw)
		if userRecord(path) {
			// The text may name the user: the log keeps the code.
			c.log.Warn("prism central error", "method", method, "path", path, "status", resp.StatusCode, "code", code)
		} else {
			c.log.Warn("prism central error", "method", method, "path", path, "status", resp.StatusCode, "message", msg)
		}
		return "", &APIError{Status: resp.StatusCode, Message: msg, Code: code}
	}
	if (out != nil || total != nil) && len(raw) > 0 {
		var env struct {
			Data     json.RawMessage `json:"data"`
			Metadata struct {
				TotalAvailableResults int `json:"totalAvailableResults"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return "", fmt.Errorf("prism central: %s %s: decode: %w", method, path, err)
		}
		if total != nil {
			*total = env.Metadata.TotalAvailableResults
		}
		if out != nil && len(env.Data) > 0 {
			if err := json.Unmarshal(env.Data, out); err != nil {
				return "", fmt.Errorf("prism central: %s %s: decode data: %w", method, path, err)
			}
		}
	}
	return resp.Header.Get("ETag"), nil
}

// getRaw performs a GET whose body is not the JSON envelope (the SAML
// SP metadata is XML) and returns it as read, capped at 1 MiB.
func (c *Client) getRaw(ctx context.Context, path string) ([]byte, error) {
	u := *c.base
	u.Path = path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		c.log.Warn("prism central call failed", "method", "GET", "path", path, "err", err.Error())
		return nil, fmt.Errorf("prism central: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("prism central: GET %s: read: %w", path, err)
	}
	c.log.Debug("prism central call", "method", "GET", "path", path, "status", resp.StatusCode)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := errorMessage(raw)
		c.log.Warn("prism central error", "method", "GET", "path", path, "status", resp.StatusCode, "message", msg)
		return nil, &APIError{Status: resp.StatusCode, Message: msg}
	}
	return raw, nil
}

// errorMessage extracts a human-readable message from a v4 error body:
// data.error as a string, else every "message" found under it, else the
// first 200 bytes of the body.
func errorMessage(raw []byte) string {
	var env struct {
		Data struct {
			Error   json.RawMessage `json:"error"`
			Message string          `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err == nil {
		if env.Data.Message != "" {
			return env.Data.Message
		}
		if len(env.Data.Error) > 0 {
			var s string
			if json.Unmarshal(env.Data.Error, &s) == nil {
				return s
			}
			var v any
			if json.Unmarshal(env.Data.Error, &v) == nil {
				if msgs := collectMessages(v, nil); len(msgs) > 0 {
					return strings.Join(msgs, "; ")
				}
			}
		}
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// errorCode returns the first "code" of a v4 error body, "" without one.
func errorCode(raw []byte) string {
	var env struct {
		Data struct {
			Error json.RawMessage `json:"error"`
		} `json:"data"`
	}
	var v any
	if json.Unmarshal(raw, &env) != nil || json.Unmarshal(env.Data.Error, &v) != nil {
		return ""
	}
	return firstCode(v)
}

func firstCode(v any) string {
	switch x := v.(type) {
	case map[string]any:
		if c, ok := x["code"].(string); ok && c != "" {
			return c
		}
		for _, child := range x {
			if c := firstCode(child); c != "" {
				return c
			}
		}
	case []any:
		for _, child := range x {
			if c := firstCode(child); c != "" {
				return c
			}
		}
	}
	return ""
}

// userRecord reports whether path names one user record, whose errors
// may carry the user's name.
func userRecord(path string) bool {
	_, rest, ok := strings.Cut(path, "/authn/users/")
	return ok && rest != ""
}

func collectMessages(v any, acc []string) []string {
	switch x := v.(type) {
	case map[string]any:
		if m, ok := x["message"].(string); ok && m != "" {
			acc = append(acc, m)
		}
		for _, child := range x {
			acc = collectMessages(child, acc)
		}
	case []any:
		for _, child := range x {
			acc = collectMessages(child, acc)
		}
	}
	return acc
}

// etagOf returns the ETag of a GET on path.
func (c *Client) etagOf(ctx context.Context, path string) (string, error) {
	etag, err := c.do(ctx, http.MethodGet, path, "", nil, "", nil)
	if err != nil {
		return "", err
	}
	if etag == "" {
		return "", fmt.Errorf("prism central: GET %s returned no ETag", path)
	}
	return etag, nil
}

type taskRef struct {
	ExtID string `json:"extId"`
}

type taskStatus struct {
	Status        string `json:"status"`
	ErrorMessages []struct {
		Message string `json:"message"`
	} `json:"errorMessages"`
	LegacyErrorMessage string `json:"legacyErrorMessage"`
}

// WaitTask polls a task until it succeeds, fails, or ctx ends.
func (c *Client) WaitTask(ctx context.Context, taskExtID string) error {
	for {
		var st taskStatus
		if _, err := c.do(ctx, http.MethodGet, PathTasks+"/"+taskExtID, "", nil, "", &st); err != nil {
			return err
		}
		switch st.Status {
		case "SUCCEEDED":
			return nil
		case "FAILED", "CANCELED":
			msgs := make([]string, 0, len(st.ErrorMessages))
			for _, m := range st.ErrorMessages {
				msgs = append(msgs, m.Message)
			}
			if st.LegacyErrorMessage != "" {
				msgs = append(msgs, st.LegacyErrorMessage)
			}
			return &APIError{Status: http.StatusInternalServerError, Message: fmt.Sprintf("task %s %s: %s", taskExtID, st.Status, strings.Join(msgs, "; ")), Task: true}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.taskPoll):
		}
	}
}

// randomUUID returns a random version 4 UUID.
func randomUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
