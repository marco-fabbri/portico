package nutanix

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

const testAdminPW = "pc-pw-1"

// iamState is the bootstrap side of the fake: the service account, its
// key and the Project Manager_acp policy with its read-only fields.
type iamState struct {
	saUser    map[string]any // nil until created or preset
	keyDel    int
	policy    map[string]any
	policyPut map[string]any
	idpPost   map[string]any
}

// testSPMetadata is the shape measured on pc.7.6.0.6 (2026-10-18): plain
// XML, no JSON envelope.
const testSPMetadata = `<?xml version="1.0" encoding="UTF-8"?><md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://prism.example.test/api/iam/authn"><md:SPSSODescriptor AuthnRequestsSigned="false" protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol"><md:NameIDFormat>urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress</md:NameIDFormat><md:AssertionConsumerService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" Location="https://prism.example.test/api/iam/authn/callback" index="1"/></md:SPSSODescriptor></md:EntityDescriptor>`

func (f *fakePC) iam() *iamState {
	if f.iamSt == nil {
		f.iamSt = &iamState{policy: map[string]any{
			"$reserved": map[string]any{"$fv": "v4.r1"}, "$objectType": "iam.v4.authz.AuthorizationPolicy",
			"links": []map[string]any{{"href": "self", "rel": "self"}}, "tenantId": nil,
			"extId": "pol-pm", "displayName": "Project Manager_acp", "description": "Project Manager",
			"authorizationPolicyType": "PREDEFINED_UPDATE_IDENTITY_ONLY", "role": "role-pm",
			"createdBy": "x", "createdTime": "2026-01-01T00:00:00Z", "lastUpdatedTime": "2026-01-01T00:00:00Z",
			"isSystemDefined": true, "clientName": "iam", "assignedUsersCount": 1, "assignedUserGroupsCount": 0,
			"identities": []any{map[string]any{"identityFilter": map[string]any{"user": map[string]any{"uuid": map[string]any{"anyof": []any{"u-existing"}}}}}},
			"entities":   []any{map[string]any{"$reserved": map[string]any{"$fv": "v4.r1"}, "entityFilter": map[string]any{"*": map[string]any{"*": map[string]any{"eq": "*"}}}}},
		}}
	}
	return f.iamSt
}

func (f *fakePC) handleIAM(w http.ResponseWriter, r *http.Request, rec recorded, reply func(int, any)) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.iam()
	p := r.URL.Path
	switch {
	case p == PathIAM0+"/authn/users" && r.Method == "GET":
		out := []map[string]any{}
		if st.saUser != nil && strings.Contains(r.URL.Query().Get("$filter"), "'"+st.saUser["username"].(string)+"'") {
			out = append(out, st.saUser)
		}
		reply(200, out)
	case p == PathIAM1+"/authn/users" && r.Method == "POST":
		st.saUser = map[string]any{"extId": "sa-1", "username": rec.Body["username"], "userType": rec.Body["userType"]}
		reply(201, st.saUser)
	case p == PathIAM1+"/authn/users/sa-1/keys" && r.Method == "GET":
		reply(200, []map[string]any{{"extId": "key-1", "name": "portico-x", "status": "VALID"}, {"extId": "key-2", "name": "ops", "status": "REVOKED"}})
	case p == PathIAM1+"/authn/users/sa-1/keys" && r.Method == "POST":
		reply(201, map[string]any{"extId": "key-1", "name": rec.Body["name"], "keyType": "API_KEY", "keyDetails": map[string]any{"apiKey": "k-new-456"}, "expiryTime": "2126-10-18T00:00:00Z"})
	case p == PathIAM1+"/authn/users/sa-1/keys/key-1" && r.Method == "GET":
		w.Header().Set("ETag", f.etag)
		reply(200, map[string]any{"extId": "key-1", "name": "portico-x", "status": "VALID"})
	case p == PathIAM1+"/authn/users/sa-1/keys/key-1" && r.Method == "DELETE":
		st.keyDel++
		w.WriteHeader(204)
	case p == PathIAM1+"/authn/saml-identity-providers" && r.Method == "POST":
		st.idpPost = rec.Body
		reply(201, map[string]any{"extId": "idp-new", "name": rec.Body["name"], "entityIssuer": "https://prism.example.test/api/iam/authn", "redirectURL": rec.Body["redirectURL"]})
	case p == PathIAM1+"/authn/saml-identity-providers/idp-new/sp-metadata" && r.Method == "GET":
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(200)
		w.Write([]byte(testSPMetadata))
	case strings.HasSuffix(p, "/sp-metadata") && r.Method == "GET":
		reply(404, map[string]any{"error": "identity provider not found"})
	case p == PathIAM1+"/authz/authorization-policies/pol-pm" && r.Method == "GET":
		w.Header().Set("ETag", f.etag)
		reply(200, st.policy)
	case p == PathIAM1+"/authz/authorization-policies/pol-pm" && r.Method == "PUT":
		st.policyPut = rec.Body
		st.policy["identities"] = rec.Body["identities"]
		reply(200, map[string]any{"message": "updated"})
	default:
		return false
	}
	return true
}

func basicClient(t *testing.T, f *fakePC) *Client {
	t.Helper()
	c, err := NewWithPassword(f.srv.URL, "admin", testAdminPW, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBasicAuthClientSendsNoAPIKey(t *testing.T) {
	f := newFakePC(t)
	c := basicClient(t, f)
	if _, err := c.FindUser(context.Background(), "portico"); !IsNotFound(err) {
		t.Fatalf("FindUser on an empty list = %v, want not found", err)
	}
	reqs := f.requests()
	if len(reqs) != 1 || reqs[0].Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:"+testAdminPW)) || reqs[0].Header.Get("X-Ntnx-Api-Key") != "" {
		t.Fatalf("headers = %v", reqs[0].Header)
	}
	if _, err := NewWithPassword(f.srv.URL, "admin", "", nil, nil); err == nil {
		t.Fatal("an empty password was accepted")
	}
}

func TestFindUserFiltersByUsername(t *testing.T) {
	f := newFakePC(t)
	f.iam().saUser = map[string]any{"extId": "sa-1", "username": "portico", "userType": "SERVICE_ACCOUNT"}
	c := basicClient(t, f)
	u, err := c.FindUser(context.Background(), "portico")
	if err != nil || u.ExtID != "sa-1" || u.UserType != "SERVICE_ACCOUNT" {
		t.Fatalf("FindUser = %+v, %v", u, err)
	}
	if q := f.requests()[0].Query; !strings.Contains(q, "%24filter=username+eq+%27portico%27") {
		t.Fatalf("query = %q", q)
	}
	if _, err := c.FindUser(context.Background(), "nobody"); !IsNotFound(err) {
		t.Fatalf("FindUser nobody = %v", err)
	}
}

func TestCreateServiceAccountAndKey(t *testing.T) {
	f := newFakePC(t)
	c := basicClient(t, f)
	ctx := context.Background()
	u, err := c.CreateServiceAccount(ctx, "portico", "Portico service account", "Portico: tenant provisioning", "portico@prism.example.test")
	if err != nil || u.ExtID != "sa-1" {
		t.Fatalf("CreateServiceAccount = %+v, %v", u, err)
	}
	post := find(f.requests(), "POST", PathIAM1+"/authn/users")
	if post == nil || post.Body["username"] != "portico" || post.Body["userType"] != "SERVICE_ACCOUNT" || post.Body["displayName"] != "Portico service account" || post.Body["description"] != "Portico: tenant provisioning" || post.Body["emailId"] != "portico@prism.example.test" {
		t.Fatalf("POST users = %+v", post)
	}
	k, err := c.CreateAPIKey(ctx, "sa-1", "portico-20261018-120000", "Portico appliance")
	if err != nil || k.ExtID != "key-1" || k.Key != "k-new-456" || k.Name != "portico-20261018-120000" || !k.ExpiryTime.Equal(time.Date(2126, 10, 18, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("CreateAPIKey = %+v, %v", k, err)
	}
	kp := find(f.requests(), "POST", PathIAM1+"/authn/users/sa-1/keys")
	if kp == nil || kp.Body["name"] != "portico-20261018-120000" || kp.Body["keyType"] != "API_KEY" || kp.Body["description"] != "Portico appliance" {
		t.Fatalf("POST keys = %+v", kp)
	}
}

func TestAddPolicyUserStripsReadOnlyFieldsAndIsIdempotent(t *testing.T) {
	f := newFakePC(t)
	c := basicClient(t, f)
	ctx := context.Background()
	pol, err := c.FindAuthorizationPolicy(ctx, "Project Manager_acp")
	if err != nil || pol.ExtID != "pol-pm" {
		t.Fatalf("FindAuthorizationPolicy = %+v, %v", pol, err)
	}
	added, err := c.AddPolicyUser(ctx, "pol-pm", "sa-1")
	if err != nil || !added {
		t.Fatalf("AddPolicyUser = %v, %v", added, err)
	}
	put := find(f.requests(), "PUT", PathIAM1+"/authz/authorization-policies/pol-pm")
	if put == nil || put.Header.Get("If-Match") != f.etag {
		t.Fatalf("PUT = %+v", put)
	}
	for _, k := range []string{"$reserved", "$objectType", "links", "tenantId", "createdBy", "createdTime", "lastUpdatedTime", "extId", "isSystemDefined", "clientName", "assignedUserGroupsCount", "assignedUsersCount"} {
		if _, present := put.Body[k]; present {
			t.Fatalf("PUT body still has %q: %v", k, put.Body)
		}
	}
	raw, _ := json.Marshal(put.Body)
	if strings.Contains(string(raw), "$reserved") || put.Body["displayName"] != "Project Manager_acp" || put.Body["role"] != "role-pm" {
		t.Fatalf("PUT body = %s", raw)
	}
	anyof := put.Body["identities"].([]any)[0].(map[string]any)["identityFilter"].(map[string]any)["user"].(map[string]any)["uuid"].(map[string]any)["anyof"].([]any)
	if len(anyof) != 2 || anyof[0] != "u-existing" || anyof[1] != "sa-1" {
		t.Fatalf("anyof = %v", anyof)
	}
	n := len(f.requests())
	added, err = c.AddPolicyUser(ctx, "pol-pm", "sa-1")
	if err != nil || added || len(f.requests()) != n+1 {
		t.Fatalf("second AddPolicyUser = %v, %v, requests %d → %d", added, err, n, len(f.requests()))
	}
	if _, err := c.FindAuthorizationPolicy(ctx, "Nope_acp"); !IsNotFound(err) {
		t.Fatalf("missing policy = %v", err)
	}
}

func TestListAPIKeysReturnsNamesAndIDs(t *testing.T) {
	f := newFakePC(t)
	c := basicClient(t, f)
	keys, err := c.ListAPIKeys(context.Background(), "sa-1")
	if err != nil || len(keys) != 2 || keys[0].ExtID != "key-1" || keys[0].Name != "portico-x" || keys[1].Name != "ops" {
		t.Fatalf("keys=%+v err=%v", keys, err)
	}
}

func TestDeleteAPIKeyUsesETag(t *testing.T) {
	f := newFakePC(t)
	c := basicClient(t, f)
	if err := c.DeleteAPIKey(context.Background(), "sa-1", "key-1"); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}
	del := find(f.requests(), "DELETE", PathIAM1+"/authn/users/sa-1/keys/key-1")
	if del == nil || del.Header.Get("If-Match") != f.etag || f.iam().keyDel != 1 {
		t.Fatalf("DELETE = %+v", del)
	}
}

func TestCreateSAMLIdPEncodesMetadata(t *testing.T) {
	f := newFakePC(t)
	c := newClient(t, f, nil)
	xml := []byte(`<EntityDescriptor entityID="https://id.example.test/realms/msp"/>`)
	idp, err := c.CreateSAMLIdP(context.Background(), SAMLIdPSpec{Name: "portico-keycloak", MetadataXML: xml, RedirectURL: "https://prism.example.test"})
	if err != nil || idp.ExtID != "idp-new" || idp.EntityIssuer != "https://prism.example.test/api/iam/authn" {
		t.Fatalf("CreateSAMLIdP = %+v, %v", idp, err)
	}
	b := f.iam().idpPost
	if b["name"] != "portico-keycloak" || b["usernameAttribute"] != "email" || b["emailAttribute"] != "email" || b["groupsAttribute"] != "groups" || b["isSignedAuthnReqEnabled"] != false || b["redirectURL"] != "https://prism.example.test" {
		t.Fatalf("POST body = %v", b)
	}
	if dec, err := base64.StdEncoding.DecodeString(b["idpMetadataXml"].(string)); err != nil || string(dec) != string(xml) {
		t.Fatalf("idpMetadataXml = %v (%v)", b["idpMetadataXml"], err)
	}
}

func TestSPMetadataParsesEntityIDAndACS(t *testing.T) {
	f := newFakePC(t)
	c := newClient(t, f, nil)
	entityID, acs, err := c.SPMetadata(context.Background(), "idp-new")
	if err != nil || entityID != "https://prism.example.test/api/iam/authn" || acs != "https://prism.example.test/api/iam/authn/callback" {
		t.Fatalf("SPMetadata = %q %q %v", entityID, acs, err)
	}
	if _, _, err := c.SPMetadata(context.Background(), "idp-missing"); !IsNotFound(err) {
		t.Fatalf("missing IdP = %v", err)
	}
}
