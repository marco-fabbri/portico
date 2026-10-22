package nutanix

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakePC is an httptest Prism Central answering the v4 shapes measured in
// docs/lab write-ups 05 and 07. It records every request for assertions.
type fakePC struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []recorded
	polls  int
	key    string
	etag   string
	fail   map[string]int // "METHOD path" → status to answer with an error body
	status string         // task status after polls
	totals map[string]int // list path → metadata.totalAvailableResults
	iamSt  *iamState      // service account, key and policy (bootstrap_test.go)
}

type recorded struct {
	Method, Path, Query string
	Header              http.Header
	Body                map[string]any
}

func newFakePC(t *testing.T) *fakePC {
	f := &fakePC{t: t, key: "k-secret-123", etag: `"etag-abc"`, fail: map[string]int{}, status: "SUCCEEDED", totals: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePC) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

func (f *fakePC) handle(w http.ResponseWriter, r *http.Request) {
	rec := recorded{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone()}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&rec.Body)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, rec)
	key := r.Method + " " + r.URL.Path
	status, failing := f.fail[key]
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	user, pw, basic := r.BasicAuth()
	if r.Header.Get("X-Ntnx-Api-Key") != f.key && !(basic && user == "admin" && pw == testAdminPW) {
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"error": "unauthorized"}})
		return
	}
	if failing {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"error": map[string]any{
			"validationErrorMessages": []map[string]any{{"message": "missing property 'id'"}}}}})
		return
	}
	reply := func(code int, data any) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{"data": data, "metadata": map[string]any{"totalAvailableResults": 1}})
	}
	p := r.URL.Path
	if n, ok := f.totals[p]; ok {
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "metadata": map[string]any{"totalAvailableResults": n}})
		return
	}
	if f.handleIAM(w, r, rec, reply) {
		return
	}
	switch {
	case strings.HasPrefix(p, PathTasks+"/"):
		f.mu.Lock()
		f.polls++
		n := f.polls
		st := f.status
		f.mu.Unlock()
		if n < 2 {
			st = "RUNNING"
		}
		data := map[string]any{"extId": strings.TrimPrefix(p, PathTasks+"/"), "status": st}
		if st == "FAILED" {
			data["errorMessages"] = []map[string]any{{"message": "task exploded"}}
		}
		reply(200, data)
	case p == PathMultidomain+"/config/projects" && r.Method == "GET":
		reply(200, []map[string]any{
			{"extId": "00000000-0000-0000-0000-000000000000", "id": "default", "name": "_internal", "isDefault": true},
			{"extId": "p-ext-1", "id": "portico-acme", "name": "portico-acme", "isDefault": false},
		})
	case p == PathMultidomain+"/config/projects" && r.Method == "POST":
		reply(202, map[string]any{"extId": "task-create-project"})
	case strings.HasPrefix(p, PathMultidomain+"/config/projects/") && r.Method == "GET":
		w.Header().Set("ETag", f.etag)
		reply(200, map[string]any{"extId": "p-ext-1", "id": "portico-acme", "name": "portico-acme"})
	case strings.HasPrefix(p, PathMultidomain+"/config/projects/") && r.Method == "DELETE":
		reply(202, map[string]any{"extId": "task-delete-project"})
	case p == PathMultidomain+"/config/resource-groups" && r.Method == "GET":
		reply(200, []map[string]any{{"extId": "rg-1", "name": "portico-acme-rg", "projectExtId": "p-ext-1"}})
	case p == PathMultidomain+"/config/resource-groups" && r.Method == "POST":
		reply(202, map[string]any{"extId": "task-create-rg"})
	case strings.HasPrefix(p, PathMultidomain+"/config/resource-groups/") && r.Method == "GET":
		w.Header().Set("ETag", f.etag)
		reply(200, map[string]any{"extId": "rg-1"})
	case strings.HasPrefix(p, PathMultidomain+"/config/resource-groups/") && r.Method == "DELETE":
		reply(202, map[string]any{"extId": "task-delete-rg"})
	case p == PathIAM1+"/authn/saml-identity-providers" && r.Method == "GET":
		reply(200, []map[string]any{{"extId": "idp-1", "name": "portico-keycloak", "isSharedWithAllProjects": false, "sharedWithProjects": []string{"p-ext-1"}}})
	case strings.HasPrefix(p, PathIAM1+"/authn/saml-identity-providers/") && r.Method == "GET":
		w.Header().Set("ETag", f.etag)
		reply(200, map[string]any{"extId": "idp-1", "name": "portico-keycloak", "isSharedWithAllProjects": false, "sharedWithProjects": []string{"p-ext-1"}})
	case strings.HasSuffix(p, "/$actions/share") || strings.HasSuffix(p, "/$actions/unshare"):
		reply(200, map[string]any{"message": "done"})
	case p == PathIAM0+"/authn/user-groups" && r.Method == "GET":
		reply(200, []map[string]any{{"extId": "g-1", "name": "tenant-acme"}})
	case p == PathIAM0+"/authn/user-groups" && r.Method == "POST":
		reply(201, map[string]any{"extId": "g-2", "name": rec.Body["name"]})
	case strings.HasPrefix(p, PathIAM0+"/authn/user-groups/") && r.Method == "GET":
		w.Header().Set("ETag", f.etag)
		reply(200, map[string]any{"extId": "g-1"})
	case strings.HasPrefix(p, PathIAM0+"/authn/user-groups/") && r.Method == "DELETE":
		w.WriteHeader(204)
	case p == PathIAM0+"/authz/roles":
		reply(200, []map[string]any{{"extId": "ca386756-e45f-5555-8625-5b68ae17393b", "displayName": "Project Admin"}})
	case p == PathIAM1+"/authz/role-memberships" && r.Method == "GET":
		reply(200, []map[string]any{{"extId": "rm-1", "identityExtId": "g-1", "identityType": "GROUP", "projectExtId": "p-ext-1", "roleExtId": "r-1", "scopeTemplateName": ScopeTemplate}})
	case p == PathIAM1+"/authz/role-memberships" && r.Method == "POST":
		reply(201, map[string]any{"extId": "rm-2"})
	case strings.HasPrefix(p, PathIAM1+"/authz/role-memberships/") && r.Method == "GET":
		w.Header().Set("ETag", f.etag)
		reply(200, map[string]any{"extId": "rm-1"})
	case strings.HasPrefix(p, PathIAM1+"/authz/role-memberships/") && r.Method == "DELETE":
		w.WriteHeader(204)
	case p == PathIAM1+"/authz/authorization-policies":
		reply(200, []map[string]any{
			{"extId": "pol-pm", "displayName": "Project Manager_acp"},
			{"extId": "pol-1", "displayName": "Super Admin_acp", "identities": []map[string]any{{"identityFilter": map[string]any{"user": map[string]any{"uuid": map[string]any{"anyof": []string{"u-1"}}}}}}},
			{"extId": "pol-2", "displayName": "Leaky_acp", "identities": []map[string]any{{"identityFilter": map[string]any{"group": map[string]any{"uuid": map[string]any{"anyof": []string{"g-1"}}}}}}},
		})
	default:
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"error": "no route " + key}})
	}
}
