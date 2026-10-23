package nutanix

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func newClient(t *testing.T, f *fakePC, logBuf *bytes.Buffer) *Client {
	t.Helper()
	var log *slog.Logger
	if logBuf != nil {
		log = slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	c, err := New(f.srv.URL, f.key, nil, log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.taskPoll = 5 * time.Millisecond
	return c
}

func find(reqs []recorded, method, path string) *recorded {
	for i := range reqs {
		if reqs[i].Method == method && reqs[i].Path == path {
			return &reqs[i]
		}
	}
	return nil
}

func TestCreateProjectPostsWaitsAndFinds(t *testing.T) {
	f := newFakePC(t)
	c := newClient(t, f, nil)
	p, err := c.CreateProject(context.Background(), "portico-acme", "portico-acme", "Portico tenant ACME")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.ExtID != "p-ext-1" || p.Name != "portico-acme" {
		t.Fatalf("project = %+v", p)
	}
	post := find(f.requests(), "POST", PathMultidomain+"/config/projects")
	if post == nil {
		t.Fatal("no POST to projects")
	}
	if post.Body["id"] != "portico-acme" || post.Body["name"] != "portico-acme" || post.Body["description"] != "Portico tenant ACME" {
		t.Fatalf("POST body = %v", post.Body)
	}
	if post.Header.Get("NTNX-Request-Id") == "" || post.Header.Get("X-Ntnx-Api-Key") != f.key {
		t.Fatalf("POST headers = %v", post.Header)
	}
	if f.polls < 2 {
		t.Fatalf("task polled %d times, want at least 2", f.polls)
	}
}

func TestDeleteProjectSendsIfMatch(t *testing.T) {
	f := newFakePC(t)
	c := newClient(t, f, nil)
	if err := c.DeleteProject(context.Background(), "p-ext-1"); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	del := find(f.requests(), "DELETE", PathMultidomain+"/config/projects/p-ext-1")
	if del == nil {
		t.Fatal("no DELETE")
	}
	if del.Header.Get("If-Match") != f.etag {
		t.Fatalf("If-Match = %q, want %q", del.Header.Get("If-Match"), f.etag)
	}
}

func TestDeleteSAMLIdPSendsIfMatch(t *testing.T) {
	f := newFakePC(t)
	c := newClient(t, f, nil)
	if err := c.DeleteSAMLIdP(context.Background(), "idp-1"); err != nil {
		t.Fatalf("DeleteSAMLIdP: %v", err)
	}
	del := find(f.requests(), "DELETE", PathIAM1+"/authn/saml-identity-providers/idp-1")
	if del == nil {
		t.Fatal("no DELETE")
	}
	if del.Header.Get("If-Match") != f.etag {
		t.Fatalf("If-Match = %q, want %q", del.Header.Get("If-Match"), f.etag)
	}
	f.fail["GET "+PathIAM1+"/authn/saml-identity-providers/idp-gone"] = 404
	if err := c.DeleteSAMLIdP(context.Background(), "idp-gone"); err != nil {
		t.Fatalf("DeleteSAMLIdP of a missing provider: %v", err)
	}
}

func TestShareIdPUsesSingularProjectExtId(t *testing.T) {
	f := newFakePC(t)
	c := newClient(t, f, nil)
	if err := c.ShareIdP(context.Background(), "idp-1", "p-ext-9"); err != nil {
		t.Fatalf("ShareIdP: %v", err)
	}
	share := find(f.requests(), "POST", PathIAM1+"/authn/config/saml-identity-provider/idp-1/$actions/share")
	if share == nil {
		t.Fatal("no share POST")
	}
	if len(share.Body) != 1 || share.Body["projectExtId"] != "p-ext-9" {
		t.Fatalf("share body = %v", share.Body)
	}
	if share.Header.Get("If-Match") != f.etag {
		t.Fatalf("If-Match = %q", share.Header.Get("If-Match"))
	}
}

func TestCreateRoleMembershipBodyShape(t *testing.T) {
	f := newFakePC(t)
	c := newClient(t, f, nil)
	rm, err := c.CreateRoleMembership(context.Background(), "g-1", "idp-1", "p-ext-1", "r-1")
	if err != nil {
		t.Fatalf("CreateRoleMembership: %v", err)
	}
	if rm.ExtID != "rm-2" {
		t.Fatalf("membership = %+v", rm)
	}
	post := find(f.requests(), "POST", PathIAM1+"/authz/role-memberships")
	b := post.Body
	if b["identityType"] != "GROUP" || b["identityExtId"] != "g-1" || b["idpExtId"] != "idp-1" || b["projectExtId"] != "p-ext-1" || b["roleExtId"] != "r-1" {
		t.Fatalf("body = %v", b)
	}
	if b["scopeTemplateName"] != ScopeTemplate {
		t.Fatalf("scopeTemplateName = %v", b["scopeTemplateName"])
	}
	vals, _ := b["scopeTemplateNameValues"].([]any)
	if len(vals) != 1 {
		t.Fatalf("scopeTemplateNameValues = %v", b["scopeTemplateNameValues"])
	}
	v := vals[0].(map[string]any)
	if v["name"] != "projectExtId" || v["value"] != "p-ext-1" {
		t.Fatalf("scope value = %v", v)
	}
}

func TestFindRoleEscapesFilter(t *testing.T) {
	f := newFakePC(t)
	c := newClient(t, f, nil)
	r, err := c.FindRole(context.Background(), "Project Admin")
	if err != nil || r.ExtID != "ca386756-e45f-5555-8625-5b68ae17393b" {
		t.Fatalf("FindRole = %+v, %v", r, err)
	}
	get := find(f.requests(), "GET", PathIAM0+"/authz/roles")
	if get == nil || !strings.Contains(get.Query, "%24filter=displayName+eq+%27Project+Admin%27") && !strings.Contains(get.Query, "%24filter=displayName%20eq%20%27Project%20Admin%27") {
		t.Fatalf("query = %q", get.Query)
	}
}

func TestAPIErrorFromBody(t *testing.T) {
	f := newFakePC(t)
	f.fail["POST "+PathMultidomain+"/config/projects"] = 400
	c := newClient(t, f, nil)
	_, err := c.CreateProject(context.Background(), "x", "x", "x")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 || !strings.Contains(apiErr.Message, "missing property 'id'") {
		t.Fatalf("err = %v", err)
	}
	_, err = c.FindProject(context.Background(), "nope")
	if !IsNotFound(err) {
		t.Fatalf("FindProject unknown = %v, want not found", err)
	}
}

func TestWaitTaskFailure(t *testing.T) {
	f := newFakePC(t)
	f.status = "FAILED"
	c := newClient(t, f, nil)
	err := c.WaitTask(context.Background(), "task-x")
	if err == nil || !strings.Contains(err.Error(), "task exploded") {
		t.Fatalf("WaitTask = %v", err)
	}
}

func TestKeyNeverLogged(t *testing.T) {
	f := newFakePC(t)
	f.fail["GET "+PathIAM0+"/authn/user-groups"] = 500
	var buf bytes.Buffer
	c := newClient(t, f, &buf)
	_, _ = c.FindUserGroup(context.Background(), "tenant-acme")
	if buf.Len() == 0 {
		t.Fatal("expected a log line for the failed call")
	}
	if strings.Contains(buf.String(), f.key) {
		t.Fatal("the API key reached the log")
	}
}
