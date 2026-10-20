package settings

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// samlEnv is a settings document after the Keycloak and Prism Central
// bootstraps, pointing at the two fakes.
func samlEnv(t *testing.T) (*fakePCAdmin, *fakeKCAdmin, Settings) {
	t.Helper()
	pc, kc := newFakePCAdmin(t), newFakeKCAdmin(t)
	kc.realmReps["msp"] = map[string]any{"realm": "msp"}
	s := Defaults()
	s.PrismHost, s.IDHost = "prism.example.test", "id.example.test"
	s.Keycloak.Realm, s.Keycloak.AdminClientSecret = "msp", Secret(kc.adminSecret)
	s.Sites = []Site{{ID: "site-a", Code: "SITE-A", URL: pc.srv.URL, APIKey: Secret(pc.issuedKey)}}
	return pc, kc, s
}

func TestSAMLBootstrapCreatesIdPAndKeycloakClient(t *testing.T) {
	pc, kc, s := samlEnv(t)
	res, out, err := NewBootstrapper(nil, nil).SAML(context.Background(), kc.srv.URL, s, "site-a", SAMLInput{})
	if res.Name != "portico-keycloak" {
		t.Fatalf("result name %q, want the default", res.Name)
	}
	if err != nil || !res.OK || res.UsedCredentials || out.Sites[0].SAMLIdPName != "portico-keycloak" {
		t.Fatalf("res=%+v out=%+v err=%v", res, out, err)
	}
	if pc.idpPost["redirectURL"] != "https://prism.example.test" || pc.idpPost["name"] != "portico-keycloak" || pc.idpPost["idpMetadataXml"] == nil {
		t.Fatalf("IdP POST = %v", pc.idpPost)
	}
	// Each site's Prism Central is named by its own issuer (D3, write-up 20).
	if res.IdPExtID != "idp-portico-keycloak" || res.EntityID != "https://prism.example.test/api/iam/authn/site-a" || res.ACSURL != "https://prism.example.test/api/iam/authn/callback" || res.KeycloakClientID != res.EntityID {
		t.Fatalf("res = %+v", res)
	}
	cl := kc.client(res.EntityID)
	if cl == nil || cl["protocol"] != "saml" || cl["redirectUris"].([]any)[0] != res.ACSURL || cl["frontchannelLogout"] != false || cl["attributes"].(map[string]any)["saml_name_id_format"] != "email" || cl["attributes"].(map[string]any)["saml_single_logout_service_url_post"] != "" {
		t.Fatalf("saml client = %v", cl)
	}
	if m := kc.mappers[clientUUID(res.EntityID)]; len(m) != 2 || m[0]["name"] != "email" || m[1]["name"] != "groups" || m[1]["protocolMapper"] != "saml-group-membership-mapper" {
		t.Fatalf("mappers = %v", m)
	}
	if len(res.Created) != 3 || res.Created[0] != "identity provider portico-keycloak" || res.Created[1] != "keycloak client "+res.EntityID || res.Created[2] != "mappers email, groups" {
		t.Fatalf("created = %v", res.Created)
	}
}

func TestSAMLBootstrapRefusesAnotherIdP(t *testing.T) {
	pc, kc, s := samlEnv(t)
	pc.addIdP("corp-adfs")
	var se *StateError
	_, _, err := NewBootstrapper(nil, nil).SAML(context.Background(), kc.srv.URL, s, "site-a", SAMLInput{})
	if !errors.As(err, &se) || !strings.Contains(err.Error(), "corp-adfs") || !strings.Contains(err.Error(), "does not adopt") {
		t.Fatalf("another IdP: %v", err)
	}
	if kc.tokens != 0 {
		t.Fatal("Keycloak was called although the IdP was refused")
	}
}

func TestSAMLBootstrapDescriptorMismatch(t *testing.T) {
	pc, kc, s := samlEnv(t)
	kc.descriptorEntityID = "https://localhost:8080/realms/msp"
	res, _, err := NewBootstrapper(nil, nil).SAML(context.Background(), kc.srv.URL, s, "site-a", SAMLInput{})
	if err != nil || res.OK || !strings.Contains(res.Message, "check Keycloak's hostname setting") || !strings.Contains(res.Message, "https://localhost:8080/realms/msp") {
		t.Fatalf("mismatch: %+v %v", res, err)
	}
	if len(pc.idps) != 0 || pc.idpPost != nil {
		t.Fatal("Prism Central was called with a wrong descriptor")
	}
}

func TestSAMLBootstrapForbiddenAsksForCredentials(t *testing.T) {
	pc, kc, s := samlEnv(t)
	pc.forbidKey = true
	res, _, err := NewBootstrapper(nil, nil).SAML(context.Background(), kc.srv.URL, s, "site-a", SAMLInput{})
	if err != nil || res.OK || res.Message != "Prism Central refused the service account (403): give the credentials of an account that can manage identity providers" {
		t.Fatalf("forbidden: %+v %v", res, err)
	}
	res, out, err := NewBootstrapper(nil, nil).SAML(context.Background(), kc.srv.URL, s, "site-a", SAMLInput{Username: "admin", Password: "pc-pw-1"})
	if err != nil || !res.OK || !res.UsedCredentials || out.Sites[0].SAMLIdPName != "portico-keycloak" || out.Sites[0].APIKey != Secret(pc.issuedKey) {
		t.Fatalf("with credentials: %+v %v", res, err)
	}
}

func TestSAMLBootstrapIsIdempotent(t *testing.T) {
	pc, kc, s := samlEnv(t)
	b := NewBootstrapper(nil, nil)
	if res, _, err := b.SAML(context.Background(), kc.srv.URL, s, "site-a", SAMLInput{Name: "msp-idp"}); err != nil || !res.OK {
		t.Fatalf("first run: %+v %v", res, err)
	}
	res, out, err := b.SAML(context.Background(), kc.srv.URL, s, "site-a", SAMLInput{Name: "msp-idp"})
	if err != nil || !res.OK || len(res.Created) != 0 || res.IdPExtID != "idp-msp-idp" || out.Sites[0].SAMLIdPName != "msp-idp" {
		t.Fatalf("second run: %+v %v", res, err)
	}
	if len(pc.idps) != 1 || len(kc.mappers[clientUUID("https://prism.example.test/api/iam/authn/site-a")]) != 2 {
		t.Fatalf("duplicates: idps=%d mappers=%d", len(pc.idps), len(kc.mappers[clientUUID("https://prism.example.test/api/iam/authn/site-a")]))
	}
}

func TestSAMLBootstrapPreconditions(t *testing.T) {
	_, kc, s := samlEnv(t)
	b := NewBootstrapper(nil, nil)
	var se *StateError
	for name, mut := range map[string]func(*Settings){
		"no hosts":        func(s *Settings) { s.IDHost = "" },
		"no realm":        func(s *Settings) { s.Keycloak.Realm = "" },
		"no admin secret": func(s *Settings) { s.Keycloak.AdminClientSecret = "" },
		"no pc url":       func(s *Settings) { s.Sites[0].URL = "" },
		"no key":          func(s *Settings) { s.Sites[0].APIKey = "" },
	} {
		c := s
		c.Sites = append([]Site(nil), s.Sites...)
		mut(&c)
		if _, _, err := b.SAML(context.Background(), kc.srv.URL, c, "site-a", SAMLInput{}); !errors.As(err, &se) || !strings.Contains(err.Error(), "must be set first") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, _, err := b.SAML(context.Background(), kc.srv.URL, s, "site-a", SAMLInput{Name: "Bad Name"}); !errors.As(err, &se) {
		t.Fatalf("bad name: %v", err)
	}
	// Without the key, both credentials make the precondition pass.
	c := s
	c.Sites = append([]Site(nil), s.Sites...)
	c.Sites[0].APIKey = ""
	if res, _, err := b.SAML(context.Background(), kc.srv.URL, c, "site-a", SAMLInput{Username: "admin", Password: "pc-pw-1"}); err != nil || !res.OK {
		t.Fatalf("credentials instead of the key: %+v %v", res, err)
	}
}
