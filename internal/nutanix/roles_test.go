package nutanix

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestOperationExtIDs(t *testing.T) {
	f := newFakePC(t)
	f.lists[PathIAM0+"/authz/operations"] = []any{map[string]any{"extId": "op-1", "displayName": "View_Subnet"}, map[string]any{"extId": "op-2", "displayName": "Create_Role"}}
	c := newClient(t, f, nil)
	ids, err := c.OperationExtIDs(context.Background(), []string{"View_Subnet", "Create_Role"})
	if err != nil || ids["View_Subnet"] != "op-1" || ids["Create_Role"] != "op-2" {
		t.Fatalf("OperationExtIDs = %v, %v", ids, err)
	}
	get := find(f.requests(), "GET", PathIAM0+"/authz/operations")
	if get == nil || !strings.Contains(get.Query, "displayName+in+%28%27View_Subnet%27%2C%27Create_Role%27%29") {
		t.Fatalf("operations query = %+v", get)
	}
}

func TestGetRoleOperations(t *testing.T) {
	f := newFakePC(t)
	f.objs[PathIAM1+"/authz/roles/role-1"] = map[string]any{"extId": "role-1", "displayName": "Project Admin", "operations": []any{"op-1", "op-2"}}
	c := newClient(t, f, nil)
	r, err := c.GetRole(context.Background(), "role-1")
	if err != nil || r.DisplayName != "Project Admin" || !slices.Equal(r.Operations, []string{"op-1", "op-2"}) {
		t.Fatalf("GetRole = %+v, %v", r, err)
	}
}

func TestCreateRoleBody(t *testing.T) {
	f := newFakePC(t)
	c := newClient(t, f, nil)
	r, err := c.CreateRole(context.Background(), "Portico Networking", "Portico: networking", []string{"op-1"})
	if err != nil || r.ExtID != "role-new" {
		t.Fatalf("CreateRole = %+v, %v", r, err)
	}
	b := find(f.requests(), "POST", PathIAM1+"/authz/roles").Body
	if b["displayName"] != "Portico Networking" || b["description"] != "Portico: networking" || len(b["operations"].([]any)) != 1 {
		t.Fatalf("POST body = %v", b)
	}
}

// The update sends back what the GET returned, without the fields Prism
// Central sets itself, with the new operations and the ETag.
func TestUpdateRoleOperationsSendsETag(t *testing.T) {
	f := newFakePC(t)
	f.objs[PathIAM1+"/authz/roles/role-1"] = map[string]any{"extId": "role-1", "displayName": "Portico Networking", "description": "d",
		"operations": []any{"op-1"}, "createdTime": "2026-10-29T10:00:00Z", "links": []any{}, "isSystemDefined": false}
	c := newClient(t, f, nil)
	if err := c.UpdateRoleOperations(context.Background(), "role-1", []string{"op-1", "op-2"}); err != nil {
		t.Fatalf("UpdateRoleOperations: %v", err)
	}
	put := find(f.requests(), "PUT", PathIAM1+"/authz/roles/role-1")
	if put == nil || put.Header.Get("If-Match") != f.etag {
		t.Fatalf("put = %+v", put)
	}
	b := put.Body
	if b["displayName"] != "Portico Networking" || b["description"] != "d" || len(b["operations"].([]any)) != 2 {
		t.Fatalf("PUT body = %v", b)
	}
	for _, k := range []string{"extId", "createdTime", "links", "isSystemDefined"} {
		if _, ok := b[k]; ok {
			t.Fatalf("PUT body carries %s: %v", k, b)
		}
	}
}

func TestCreateAuthorizationPolicyBody(t *testing.T) {
	f := newFakePC(t)
	c := newClient(t, f, nil)
	pol, err := c.CreateAuthorizationPolicy(context.Background(), "Portico Networking_portico", "d", "role-1", "sa-1", []string{"subnet", "vpc"})
	if err != nil || pol.ExtID != "pol-new" {
		t.Fatalf("CreateAuthorizationPolicy = %+v, %v", pol, err)
	}
	b := find(f.requests(), "POST", PathIAM1+"/authz/authorization-policies").Body
	ids := b["identities"].([]any)[0].(map[string]any)["identityFilter"].(map[string]any)["user"].(map[string]any)["uuid"].(map[string]any)["anyof"].([]any)
	ents := b["entities"].([]any)
	vpc := ents[1].(map[string]any)["entityFilter"].(map[string]any)["vpc"].(map[string]any)["*"].(map[string]any)["eq"]
	if b["role"] != "role-1" || b["authorizationPolicyType"] != "USER_DEFINED" || len(ids) != 1 || ids[0] != "sa-1" || len(ents) != 2 || vpc != "*" {
		t.Fatalf("POST body = %v", b)
	}
}

func TestFindAuthorizationPolicyReadsRoleAndBinding(t *testing.T) {
	f := newFakePC(t)
	c := newClient(t, f, nil)
	pol, err := c.FindAuthorizationPolicy(context.Background(), "Super Admin_acp")
	if err != nil || !pol.Names("u-1") || pol.Names("u-2") {
		t.Fatalf("policy = %+v, %v", pol, err)
	}
}
