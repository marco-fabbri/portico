package settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/marco-fabbri/portico/internal/nutanix"
)

// fakePCAdmin is an httptest Prism Central for the bootstrap: Basic auth
// admin/pc-pw-1 on the IAM calls, the issued key on the list calls the
// verification makes.
type fakePCAdmin struct {
	srv        *httptest.Server
	mu         sync.Mutex
	user       map[string]any
	inPolicy   bool
	noPolicy   bool
	keyDeleted int
	posts      []string
	issuedKey  string
	idps       []map[string]any
	idpPost    map[string]any
	forbidKey  bool // the IdP calls answer 403 to the API key (service account without the right)
}

// addIdP pre-seeds an identity provider.
func (f *fakePCAdmin) addIdP(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.idps = append(f.idps, map[string]any{"extId": "idp-" + name, "name": name, "entityIssuer": "https://prism.example.test/api/iam/authn"})
}

func newFakePCAdmin(t *testing.T) *fakePCAdmin {
	f := &fakePCAdmin{issuedKey: "issued-key-9z"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePCAdmin) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	user, pw, basic := r.BasicAuth()
	keyOK := r.Header.Get("X-Ntnx-Api-Key") == f.issuedKey
	if !keyOK && !(basic && user == "admin" && pw == "pc-pw-1") {
		w.WriteHeader(401)
		w.Write([]byte(`{"data":{"error":"unauthorized"}}`))
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	reply := func(code int, data any) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}
	if r.Method == "POST" {
		f.posts = append(f.posts, r.URL.Path)
	}
	p := r.URL.Path
	if strings.Contains(p, "/authn/saml-identity-providers") {
		if f.forbidKey && !basic {
			w.WriteHeader(403)
			w.Write([]byte(`{"data":{"error":{"message":"forbidden"}}}`))
			return
		}
		switch {
		case p == nutanix.PathIAM1+"/authn/saml-identity-providers" && r.Method == "GET":
			out := f.idps
			if out == nil {
				out = []map[string]any{}
			}
			reply(200, out)
		case p == nutanix.PathIAM1+"/authn/saml-identity-providers" && r.Method == "POST":
			f.idpPost = body
			idp := map[string]any{"extId": "idp-" + body["name"].(string), "name": body["name"], "entityIssuer": "https://prism.example.test/api/iam/authn", "redirectURL": body["redirectURL"]}
			f.idps = append(f.idps, idp)
			reply(201, idp)
		case strings.HasSuffix(p, "/sp-metadata"):
			w.Header().Set("Content-Type", "application/xml")
			w.Write([]byte(`<?xml version="1.0"?><md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://prism.example.test/api/iam/authn"><md:SPSSODescriptor><md:AssertionConsumerService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" Location="https://prism.example.test/api/iam/authn/callback" index="1"/></md:SPSSODescriptor></md:EntityDescriptor>`))
		default:
			w.WriteHeader(404)
		}
		return
	}
	switch {
	case strings.HasSuffix(p, "/config/clusters"):
		reply(200, []map[string]any{{"extId": "cl-1", "name": "NTNX-01", "config": map[string]any{"clusterFunction": []string{"AOS"}}}})
	case strings.HasSuffix(p, "/config/storage-containers"):
		reply(200, []map[string]any{{"containerExtId": "sc-1", "name": "default"}})
	case p == nutanix.PathIAM0+"/authn/users":
		out := []map[string]any{}
		if f.user != nil {
			out = append(out, f.user)
		}
		reply(200, out)
	case p == nutanix.PathIAM1+"/authn/users" && r.Method == "POST":
		f.user = map[string]any{"extId": "sa-1", "username": body["username"], "userType": "SERVICE_ACCOUNT"}
		reply(201, f.user)
	case p == nutanix.PathIAM1+"/authn/users/sa-1/keys" && r.Method == "POST":
		reply(201, map[string]any{"extId": "key-1", "name": body["name"], "keyDetails": map[string]any{"apiKey": f.issuedKey}, "expiryTime": "2126-10-18T00:00:00Z"})
	case p == nutanix.PathIAM1+"/authn/users/sa-1/keys/key-1" && r.Method == "GET":
		w.Header().Set("ETag", `"k"`)
		reply(200, map[string]any{"extId": "key-1"})
	case p == nutanix.PathIAM1+"/authn/users/sa-1/keys/key-1" && r.Method == "DELETE":
		f.keyDeleted++
		w.WriteHeader(204)
	case p == nutanix.PathIAM1+"/authz/authorization-policies":
		if f.noPolicy {
			reply(200, []map[string]any{{"extId": "pol-1", "displayName": "Super Admin_acp"}})
			return
		}
		reply(200, []map[string]any{{"extId": "pol-pm", "displayName": "Project Manager_acp"}})
	case p == nutanix.PathIAM1+"/authz/authorization-policies/pol-pm" && r.Method == "GET":
		anyof := []any{"u-existing"}
		if f.inPolicy {
			anyof = append(anyof, "sa-1")
		}
		w.Header().Set("ETag", `"p"`)
		reply(200, map[string]any{"extId": "pol-pm", "displayName": "Project Manager_acp", "role": "role-pm",
			"identities": []any{map[string]any{"identityFilter": map[string]any{"user": map[string]any{"uuid": map[string]any{"anyof": anyof}}}}},
			"entities":   []any{map[string]any{"entityFilter": map[string]any{"*": map[string]any{"*": map[string]any{"eq": "*"}}}}}})
	case p == nutanix.PathIAM1+"/authz/authorization-policies/pol-pm" && r.Method == "PUT":
		f.inPolicy = true
		reply(200, map[string]any{"message": "updated"})
	default:
		w.WriteHeader(404)
		w.Write([]byte(`{"data":{"error":"no route"}}`))
	}
}

func prismInput(url string) PrismInput {
	return PrismInput{Username: "admin", Password: "pc-pw-1", URL: url, TLSVerify: false}
}

func TestPrismBootstrapCreatesAccountKeyAndPolicyMember(t *testing.T) {
	pc := newFakePCAdmin(t)
	b := NewBootstrapper(nil)
	s := Defaults()
	s.PrismHost = "prism.example.test"
	res, out, err := b.PrismCentral(context.Background(), s, prismInput(pc.srv.URL))
	if err != nil || !res.OK || len(res.Created) != 3 || res.Created[0] != "service account portico" || res.Created[2] != "policy member" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if out.PrismCentral.APIKey != Secret(pc.issuedKey) || out.PrismCentral.URL != pc.srv.URL || out.PrismCentral.TLSVerify {
		t.Fatalf("out = %+v", out)
	}
	if len(res.Clusters) != 1 || res.Clusters[0].ExtID != "cl-1" || len(res.Containers) != 1 || res.ServiceAccount != "portico" || res.ServiceAccountExtID != "sa-1" || !strings.HasPrefix(res.KeyName, "portico-") || res.KeyExpires.IsZero() {
		t.Fatalf("res = %+v", res)
	}
	if pc.user["username"] != "portico" || !pc.inPolicy {
		t.Fatalf("fake state: user=%v inPolicy=%v", pc.user, pc.inPolicy)
	}
}

func TestPrismBootstrapExistingAccountGetsNewKey(t *testing.T) {
	pc := newFakePCAdmin(t)
	pc.user = map[string]any{"extId": "sa-1", "username": "portico", "userType": "SERVICE_ACCOUNT"}
	pc.inPolicy = true
	res, _, err := NewBootstrapper(nil).PrismCentral(context.Background(), Defaults(), prismInput(pc.srv.URL))
	if err != nil || !res.OK || len(res.Created) != 1 || !strings.HasPrefix(res.Created[0], "api key portico-") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestPrismBootstrapWrongCredentials(t *testing.T) {
	pc := newFakePCAdmin(t)
	in := prismInput(pc.srv.URL)
	in.Password = "wrong"
	s := Defaults()
	res, out, err := NewBootstrapper(nil).PrismCentral(context.Background(), s, in)
	if err != nil || res.OK || res.Message != "Prism Central refused the credentials (401)" || len(pc.posts) != 0 || out != s {
		t.Fatalf("res=%+v err=%v posts=%v", res, err, pc.posts)
	}
}

func TestPrismBootstrapPolicyMissingDeletesTheKey(t *testing.T) {
	pc := newFakePCAdmin(t)
	pc.noPolicy = true
	res, _, err := NewBootstrapper(nil).PrismCentral(context.Background(), Defaults(), prismInput(pc.srv.URL))
	if err != nil || res.OK || res.Message != `authorization policy "Project Manager_acp" not found` || pc.keyDeleted != 1 {
		t.Fatalf("res=%+v err=%v deleted=%d", res, err, pc.keyDeleted)
	}
}

func TestPrismBootstrapPreconditions(t *testing.T) {
	pc := newFakePCAdmin(t)
	b := NewBootstrapper(nil)
	var se *StateError
	for name, in := range map[string]PrismInput{
		"empty password": {Username: "admin", URL: pc.srv.URL},
		"bad url":        {Username: "admin", Password: "x", URL: "not a url"},
		"bad account":    {Username: "admin", Password: "x", URL: pc.srv.URL, ServiceAccount: "Bad Name"},
	} {
		if _, _, err := b.PrismCentral(context.Background(), Defaults(), in); !errors.As(err, &se) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
