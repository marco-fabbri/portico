package settings

import (
	"context"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/nutanix"
)

// samlEnv is a settings document with the Keycloak realm and a site whose
// service account key is stored, pointing at the two fakes.
func samlEnv(t *testing.T) (*fakePCAdmin, *fakeKCAdmin, Settings) {
	t.Helper()
	pc, kc := newFakePCAdmin(t), newFakeKCAdmin(t)
	kc.realmReps["msp"] = map[string]any{"realm": "msp"}
	s := Defaults()
	s.PortalHost, s.IDHost = "prism.example.test", "id.example.test"
	s.Keycloak.Realm, s.Keycloak.AdminClientSecret = "msp", Secret(kc.adminSecret)
	s.Sites = []Site{{ID: "site-a", Code: "SITE-A", URL: pc.srv.URL, APIKey: Secret(pc.issuedKey)}}
	return pc, kc, s
}

// samlOn runs what the identity-provider and saml-trust steps of "Finish
// setup" run: the realm's descriptor, Prism Central's providers, then the
// trust. foreign names a provider that is not Portico's.
func samlOn(t *testing.T, kc *fakeKCAdmin, s Settings, siteID, name string, creds bool) (out samlOutcome, msg, foreign string) {
	t.Helper()
	ctx := context.Background()
	b := NewBootstrapper(nil, nil)
	k := KeycloakAdmin(kc.srv.URL, s, b.log)
	descriptor, msg := samlDescriptor(ctx, k, s)
	if msg != "" {
		return out, msg, ""
	}
	site, _ := s.Site(siteID)
	var client *nutanix.Client
	var err error
	if creds {
		client, err = nutanix.NewWithPassword(site.URL, "admin", "pc-pw-1", nil, nil)
	} else {
		client, err = nutanix.New(site.URL, string(site.APIKey), nil, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	idps, err := client.ListSAMLIdPs(ctx)
	if err != nil {
		return out, samlRefusal(site.URL, err, creds), ""
	}
	own, foreign := ownIdP(idps, s, site, name)
	if foreign != "" {
		return out, "", foreign
	}
	out, msg = b.samlTrust(ctx, client, k, s, siteID, name, own, descriptor, creds)
	return out, msg, ""
}

func TestSAMLTrustCreatesIdPAndKeycloakClient(t *testing.T) {
	pc, kc, s := samlEnv(t)
	res, msg, _ := samlOn(t, kc, s, "site-a", defaultIdPName, false)
	if msg != "" {
		t.Fatalf("msg = %q", msg)
	}
	if pc.idpPost["redirectURL"] != "https://prism.example.test" || pc.idpPost["name"] != "portico-keycloak" || pc.idpPost["idpMetadataXml"] == nil {
		t.Fatalf("IdP POST = %v", pc.idpPost)
	}
	// Each site's Prism Central is named by its own issuer (D3, write-up 20).
	if res.IdPExtID != "idp-portico-keycloak" || res.EntityID != "https://prism.example.test/api/iam/authn/site-a" || res.ACSURL != "https://prism.example.test/api/iam/authn/callback" {
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

func TestSAMLTrustNamesAnotherIdP(t *testing.T) {
	pc, kc, s := samlEnv(t)
	pc.addIdP("corp-adfs")
	if _, _, foreign := samlOn(t, kc, s, "site-a", defaultIdPName, false); foreign != "corp-adfs" {
		t.Fatalf("foreign = %q", foreign)
	}
	if pc.idpPost != nil {
		t.Fatal("an identity provider was created next to another one")
	}
}

func TestSAMLTrustDescriptorMismatch(t *testing.T) {
	pc, kc, s := samlEnv(t)
	kc.descriptorEntityID = "https://localhost:8080/realms/msp"
	_, msg, _ := samlOn(t, kc, s, "site-a", defaultIdPName, false)
	if !strings.Contains(msg, "check Keycloak's hostname setting") || !strings.Contains(msg, "https://localhost:8080/realms/msp") {
		t.Fatalf("mismatch: %q", msg)
	}
	if len(pc.idps) != 0 || pc.idpPost != nil {
		t.Fatal("Prism Central was called with a wrong descriptor")
	}
}

func TestSAMLTrustForbiddenAsksForCredentials(t *testing.T) {
	pc, kc, s := samlEnv(t)
	pc.forbidKey = true
	if _, msg, _ := samlOn(t, kc, s, "site-a", defaultIdPName, false); msg != "Prism Central refused the service account (403): give the credentials of an account that can manage identity providers" {
		t.Fatalf("forbidden: %q", msg)
	}
	if res, msg, _ := samlOn(t, kc, s, "site-a", defaultIdPName, true); msg != "" || res.IdPExtID != "idp-portico-keycloak" {
		t.Fatalf("with credentials: %+v %q", res, msg)
	}
}

func TestSAMLTrustIsIdempotent(t *testing.T) {
	pc, kc, s := samlEnv(t)
	if _, msg, _ := samlOn(t, kc, s, "site-a", "msp-idp", false); msg != "" {
		t.Fatalf("first run: %q", msg)
	}
	res, msg, _ := samlOn(t, kc, s, "site-a", "msp-idp", false)
	if msg != "" || len(res.Created) != 0 || res.IdPExtID != "idp-msp-idp" {
		t.Fatalf("second run: %+v %q", res, msg)
	}
	if len(pc.idps) != 1 || len(kc.mappers[clientUUID("https://prism.example.test/api/iam/authn/site-a")]) != 2 {
		t.Fatalf("duplicates: idps=%d mappers=%d", len(pc.idps), len(kc.mappers[clientUUID("https://prism.example.test/api/iam/authn/site-a")]))
	}
}

// Keycloak's SAML backchannel logout goes to the client's admin URL, the
// public Prism Central name, which the Keycloak container cannot resolve
// and which Portico would refuse without a session anyway (write-up 24):
// the client has none, and a client left by an older run loses it when
// the trust is set up again.
func TestSAMLClientHasNoBackchannelLogoutURL(t *testing.T) {
	_, kc, s := samlEnv(t)
	res, msg, _ := samlOn(t, kc, s, "site-a", defaultIdPName, false)
	if msg != "" {
		t.Fatalf("msg = %q", msg)
	}
	if cl := kc.client(res.EntityID); cl["adminUrl"] != "" {
		t.Fatalf("adminUrl = %v, want empty", cl["adminUrl"])
	}
	kc.client(res.EntityID)["adminUrl"] = "https://prism.example.test/api/iam/authn/callback"
	if _, msg, _ = samlOn(t, kc, s, "site-a", defaultIdPName, false); msg != "" {
		t.Fatalf("second run: %q", msg)
	}
	if cl := kc.client(res.EntityID); cl["adminUrl"] != "" {
		t.Fatalf("adminUrl after a second run = %v, want empty", cl["adminUrl"])
	}
}
