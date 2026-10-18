package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeKC is an httptest Keycloak: token endpoint plus the admin calls the
// client uses. It records requests for assertions.
type fakeKC struct {
	srv       *httptest.Server
	mu        sync.Mutex
	tokenReqs int
	reqs      []recorded
	groups    []map[string]any
	users     map[string]map[string]any // id → representation
	fail      map[string]int
	revoked   map[string]bool // tokens that no longer work
	members   int             // number of members GroupMembers pages through
	realm     map[string]any  // realm representation served on GET /admin/realms/lab
}

type recorded struct {
	Method, Path, Query, Auth string
	Body                      map[string]any
}

const (
	testSecret   = "kc-secret-xyz"
	testPassword = "Tmp-Passw0rd-123456"
)

func newFakeKC(t *testing.T) *fakeKC {
	f := &fakeKC{users: map[string]map[string]any{}, fail: map[string]int{}, revoked: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKC) handle(w http.ResponseWriter, r *http.Request) {
	rec := recorded{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization")}
	if r.URL.Path == "/realms/lab/protocol/openid-connect/token" {
		r.ParseForm()
		f.mu.Lock()
		f.tokenReqs++
		f.mu.Unlock()
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "portico-admin" || r.Form.Get("client_secret") != testSecret {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"invalid_client","error_description":"bad secret ` + r.Form.Get("client_secret") + `"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`{"access_token":"tok-%d","expires_in":300,"token_type":"Bearer"}`, f.tokenReqs)))
		return
	}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&rec.Body)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, rec)
	status, failing := f.fail[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	f.mu.Lock()
	tokenOK := strings.HasPrefix(rec.Auth, "Bearer tok-") && !f.revoked[strings.TrimPrefix(rec.Auth, "Bearer ")]
	f.mu.Unlock()
	if !tokenOK {
		w.WriteHeader(401)
		return
	}
	if failing {
		w.WriteHeader(status)
		w.Write([]byte(`{"errorMessage":"boom ` + rec.Auth + `"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	p := strings.TrimPrefix(r.URL.Path, "/admin/realms/lab")
	switch {
	case p == "" && r.Method == "GET":
		json.NewEncoder(w).Encode(f.realm)
	case p == "" && r.Method == "PUT":
		w.WriteHeader(204)
	case p == "/groups" && r.Method == "GET":
		name := r.URL.Query().Get("search")
		var out []map[string]any
		for _, g := range f.groups {
			if g["name"] == name {
				out = append(out, g)
			}
		}
		if out == nil {
			out = []map[string]any{}
		}
		json.NewEncoder(w).Encode(out)
	case p == "/groups" && r.Method == "POST":
		f.groups = append(f.groups, map[string]any{"id": "grp-" + rec.Body["name"].(string), "name": rec.Body["name"]})
		w.WriteHeader(201)
	case strings.HasPrefix(p, "/groups/") && strings.HasSuffix(p, "/members"):
		out := []map[string]any{}
		if f.members > 0 {
			first, _ := strconv.Atoi(r.URL.Query().Get("first"))
			max, _ := strconv.Atoi(r.URL.Query().Get("max"))
			if max <= 0 || max > 100 {
				max = 100 // Keycloak caps a page; the client must page
			}
			for i := first; i < f.members && i < first+max; i++ {
				out = append(out, map[string]any{"id": fmt.Sprintf("m-%d", i), "email": fmt.Sprintf("m%d@lab.test", i), "enabled": true})
			}
		} else {
			for _, u := range f.users {
				out = append(out, u)
			}
		}
		json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(p, "/groups/") && r.Method == "DELETE":
		w.WriteHeader(404)
	case p == "/users" && r.Method == "GET":
		email := r.URL.Query().Get("email")
		out := []map[string]any{}
		for _, u := range f.users {
			if u["email"] == email {
				out = append(out, u)
			}
		}
		json.NewEncoder(w).Encode(out)
	case p == "/users" && r.Method == "POST":
		id := "usr-1"
		f.users[id] = map[string]any{"id": id, "username": rec.Body["username"], "email": rec.Body["email"], "firstName": rec.Body["firstName"], "lastName": rec.Body["lastName"], "enabled": true}
		w.Header().Set("Location", f.srv.URL+"/admin/realms/lab/users/"+id)
		w.WriteHeader(201)
	case strings.HasPrefix(p, "/users/") && strings.Contains(p, "/groups/") && r.Method == "PUT":
		w.WriteHeader(204)
	case strings.HasPrefix(p, "/users/") && r.Method == "GET":
		id := strings.TrimPrefix(p, "/users/")
		json.NewEncoder(w).Encode(f.users[id])
	case strings.HasPrefix(p, "/users/") && r.Method == "PUT":
		id := strings.TrimPrefix(p, "/users/")
		f.users[id]["enabled"] = rec.Body["enabled"]
		w.WriteHeader(204)
	default:
		w.WriteHeader(404)
	}
}

func (f *fakeKC) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

func newClient(f *fakeKC, buf *bytes.Buffer) *Client {
	var log *slog.Logger
	if buf != nil {
		log = slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return New(f.srv.URL, "lab", "portico-admin", testSecret, nil, log)
}

func TestTokenIsRequestedOnceWhileValid(t *testing.T) {
	f := newFakeKC(t)
	f.groups = []map[string]any{{"id": "grp-a", "name": "tenant-a"}}
	c := newClient(f, nil)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		g, err := c.FindGroup(ctx, "tenant-a")
		if err != nil || g.ID != "grp-a" {
			t.Fatalf("FindGroup #%d = %+v, %v", i, g, err)
		}
	}
	if f.tokenReqs != 1 {
		t.Fatalf("token requested %d times, want 1", f.tokenReqs)
	}
	for _, r := range f.requests() {
		if r.Auth != "Bearer tok-1" {
			t.Fatalf("request %s %s without the bearer token", r.Method, r.Path)
		}
	}
}

func TestCreateUserBody(t *testing.T) {
	f := newFakeKC(t)
	c := newClient(f, nil)
	u, err := c.CreateUser(context.Background(), User{Email: "carol@lab.test", FirstName: "Carol", LastName: "Lab"}, testPassword)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID != "usr-1" || u.Email != "carol@lab.test" {
		t.Fatalf("user = %+v", u)
	}
	var post *recorded
	for _, r := range f.requests() {
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/users") {
			rr := r
			post = &rr
		}
	}
	if post == nil {
		t.Fatal("no POST /users")
	}
	b := post.Body
	if b["username"] != "carol@lab.test" || b["email"] != "carol@lab.test" || b["enabled"] != true || b["emailVerified"] != true {
		t.Fatalf("body = %v", b)
	}
	ra, _ := b["requiredActions"].([]any)
	if len(ra) != 1 || ra[0] != "UPDATE_PASSWORD" {
		t.Fatalf("requiredActions = %v", b["requiredActions"])
	}
	creds, _ := b["credentials"].([]any)
	if len(creds) != 1 {
		t.Fatalf("credentials = %v", b["credentials"])
	}
	cred := creds[0].(map[string]any)
	if cred["type"] != "password" || cred["temporary"] != true || cred["value"] != testPassword {
		t.Fatalf("credential = %v", cred)
	}
}

func TestFindGroupNotFound(t *testing.T) {
	f := newFakeKC(t)
	c := newClient(f, nil)
	_, err := c.FindGroup(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindGroup = %v, want ErrNotFound", err)
	}
	_, err = c.FindUserByEmail(context.Background(), "nobody@lab.test")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("FindUserByEmail = %v, want ErrNotFound", err)
	}
}

func TestDeleteGroupTolerates404(t *testing.T) {
	f := newFakeKC(t)
	c := newClient(f, nil)
	if err := c.DeleteGroup(context.Background(), "grp-gone"); err != nil {
		t.Fatalf("DeleteGroup on 404 = %v, want nil", err)
	}
}

func TestSecretAndPasswordNeverLogged(t *testing.T) {
	f := newFakeKC(t)
	f.fail["POST /admin/realms/lab/users"] = 500
	var buf bytes.Buffer
	c := newClient(f, &buf)
	_, err := c.CreateUser(context.Background(), User{Email: "x@lab.test"}, testPassword)
	if err == nil {
		t.Fatal("expected an error")
	}
	if buf.Len() == 0 {
		t.Fatal("expected a log line for the failed call")
	}
	out := buf.String() + err.Error()
	if strings.Contains(out, testSecret) || strings.Contains(out, testPassword) || strings.Contains(out, "tok-1") {
		t.Fatalf("secret material reached the log or the error: %s", out)
	}
}

// 2a review minor: a token invalidated server-side is refreshed once.
func TestTokenRefreshedOnceOn401(t *testing.T) {
	f := newFakeKC(t)
	f.groups = []map[string]any{{"id": "grp-a", "name": "tenant-a"}}
	c := newClient(f, nil)
	ctx := context.Background()
	if _, err := c.FindGroup(ctx, "tenant-a"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.revoked["tok-1"] = true
	f.mu.Unlock()
	if _, err := c.FindGroup(ctx, "tenant-a"); err != nil {
		t.Fatalf("FindGroup after revocation: %v", err)
	}
	if f.tokenReqs != 2 {
		t.Fatalf("token requested %d times, want 2", f.tokenReqs)
	}
}

// 2a review minor: GroupMembers pages through large groups.
func TestGroupMembersPages(t *testing.T) {
	f := newFakeKC(t)
	f.members = 103
	c := newClient(f, nil)
	users, err := c.GroupMembers(context.Background(), "grp-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 103 {
		t.Fatalf("got %d members, want 103", len(users))
	}
}

func TestSetRealmLoginTheme(t *testing.T) {
	f := newFakeKC(t)
	f.realm = map[string]any{"realm": "lab", "loginTheme": "keycloak", "displayName": "Lab realm", "enabled": true}
	c := newClient(f, nil)
	if err := c.SetRealmLoginTheme(context.Background(), "portico"); err != nil {
		t.Fatalf("SetRealmLoginTheme: %v", err)
	}
	var put *recorded
	for _, r := range f.requests() {
		if r.Method == "PUT" && r.Path == "/admin/realms/lab" {
			rr := r
			put = &rr
		}
	}
	if put == nil {
		t.Fatal("no PUT on the realm")
	}
	if put.Body["loginTheme"] != "portico" || put.Body["displayName"] != "Lab realm" || put.Body["enabled"] != true {
		t.Fatalf("PUT body = %v", put.Body)
	}
}
