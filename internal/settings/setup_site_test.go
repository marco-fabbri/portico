package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/marco-fabbri/portico/internal/proxy"
	"github.com/marco-fabbri/portico/internal/settings/settingstest"
	"github.com/marco-fabbri/portico/internal/store"
)

// setupFixture is a document ready for "Add a site": the public names,
// the Keycloak realm with its admin client, and site-a at an https Prism
// Central double whose certificate no root trusts, nothing set up on it.
type setupFixture struct {
	pc  *settingstest.PC
	kc  *settingstest.KC
	b   *Bootstrapper
	svc *Service
	pin string // the fingerprint the double presents
}

func setupEnv(t *testing.T) *setupFixture {
	t.Helper()
	pc, kc := settingstest.NewTLSPC(t), settingstest.NewKC(t)
	pc.Version = "pc.7.6.0.6"
	kc.Realms["msp"] = map[string]any{"realm": "msp"}
	svc := NewService(newStore(t), sealerFor(t, 1))
	s := Defaults()
	s.PortalHost, s.IDHost = "prism.example.test", "id.example.test"
	s.Keycloak.Realm, s.Keycloak.AdminClientID, s.Keycloak.AdminClientSecret = "msp", "portico-admin", Secret(kc.RealmSecret)
	s.Sites = []Site{{ID: "site-a", Code: "SITE-A", Name: "Milan", URL: pc.Srv.URL, TLSVerify: true}}
	if err := svc.Set(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return &setupFixture{pc: pc, kc: kc, b: NewBootstrapper(nil, svc), svc: svc, pin: proxy.Fingerprint(pc.Srv.Certificate().Raw)}
}

func (f *setupFixture) admin(pin string) SetupInput {
	return SetupInput{Username: "admin", Password: f.pc.Password, Pin: pin}
}

func (f *setupFixture) run(t *testing.T, in SetupInput) SetupResult {
	t.Helper()
	res, err := f.b.SetupSite(context.Background(), f.kc.Srv.URL, "site-a", in)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (f *setupFixture) doc(t *testing.T) Settings {
	t.Helper()
	d, err := f.svc.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f *setupFixture) stored(t *testing.T) Site {
	s, _ := f.doc(t).Site("site-a")
	return s
}

func stepOf(res SetupResult, name string) StepLine {
	for _, s := range res.Steps {
		if s.Name == name {
			return s
		}
	}
	return StepLine{}
}

var setupSteps = []string{"certificate", "version", "rights", "identity-provider", "service-account", "networking-rights", "saml-trust", "defaults", "live"}

func TestSetupUnconfirmedCertificateSendsNoCredentials(t *testing.T) {
	f := setupEnv(t)
	res := f.run(t, f.admin("AA:BB"))
	if s := stepOf(res, "certificate"); s.Status != "failed" || !strings.Contains(s.Message, "not confirmed") || len(res.Steps) != 1 || res.Complete {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if f.pc.Credentialed != 0 {
		t.Fatalf("%d credentialed requests to an unconfirmed server", f.pc.Credentialed)
	}
	if f.stored(t).TLSPin != "" {
		t.Fatal("a wrong pin was stored")
	}
}

func TestSetupPinnedRunsAllSteps(t *testing.T) {
	f := setupEnv(t)
	res := f.run(t, f.admin(f.pin))
	if len(res.Steps) != len(setupSteps) || !res.Complete || res.IdPChanged {
		t.Fatalf("res = %+v", res)
	}
	for i, s := range res.Steps {
		if s.Name != setupSteps[i] || (s.Status != "ok" && !(s.Name == "rights" && s.Status == "skipped")) {
			t.Fatalf("step %d = %+v", i, s)
		}
	}
	st := f.stored(t)
	if st.APIKey != Secret(f.pc.Key) || st.TLSPin != f.pin || st.SAMLIdPExtID != "idp-portico-keycloak" || st.SAMLIdPName != "portico-keycloak" ||
		st.SAMLEntityID != "https://prism.example.test/api/iam/authn/site-a" || st.ServiceAccountExtID != "sa-1" || st.APIKeyExtID != "key-1" ||
		st.ClusterExtID != "cl-1" || st.StorageContainerExtID != "sc-1" {
		t.Fatalf("stored site = %+v", st)
	}
	if len(f.doc(t).ServedSites()) != 1 {
		t.Fatal("the site is not served after a complete run")
	}
	raw, _ := json.Marshal(f.doc(t))
	if bytes.Contains(raw, []byte(f.pc.Password)) {
		t.Fatal("the administrator's password is in the stored document")
	}
	if _, ok := f.kc.Clients["cli-https___prism.example.test_api_iam_authn_site-a"]; !ok {
		t.Fatalf("no Keycloak client: %v", f.kc.Clients)
	}
}

func TestSetupOlderVersionStops(t *testing.T) {
	f := setupEnv(t)
	f.pc.Version = "pc.7.5.1"
	res := f.run(t, f.admin(f.pin))
	if s := stepOf(res, "version"); s.Status != "failed" || !strings.Contains(s.Message, "pc.7.5.1 is older than pc.7.6") || len(res.Steps) != 2 {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if f.pc.Created("/authn/users") || f.pc.Created("/saml-identity-providers") {
		t.Fatalf("created on an old Prism Central: %v", f.pc.Posts)
	}
}

// Repair on a site whose row is complete: a run that stops early is not
// complete, so the tenants are not bound again to a provider the run did
// not recreate (on the appliance Prism Central answered 504 at the
// version step after its provider was deleted, write-up 26).
func TestRepairStoppedEarlyIsNotComplete(t *testing.T) {
	f := setupEnv(t)
	if res := f.run(t, f.admin(f.pin)); !res.Complete {
		t.Fatalf("first run = %+v", res.Steps)
	}
	f.pc.Version = "pc.7.5.1"
	res := f.run(t, f.admin(""))
	if s := stepOf(res, "version"); s.Status != "failed" || res.Complete {
		t.Fatalf("complete=%v steps=%+v", res.Complete, res.Steps)
	}
}

func TestSetupForeignIdPStopsBeforeCreating(t *testing.T) {
	f := setupEnv(t)
	f.pc.AddIdP("other-idp")
	res := f.run(t, f.admin(f.pin))
	if s := stepOf(res, "identity-provider"); s.Status != "failed" || !strings.Contains(s.Message, "other-idp") || len(res.Steps) != 4 {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if f.pc.Created("/authn/users") || f.pc.Created("/keys") || len(f.pc.IdPs) != 1 {
		t.Fatalf("created next to another provider: %v", f.pc.Posts)
	}
}

func TestSetupKeycloakRefusedLeavesIncomplete(t *testing.T) {
	f := setupEnv(t)
	f.kc.FailClientCreate = 403
	res := f.run(t, f.admin(f.pin))
	if s := stepOf(res, "saml-trust"); s.Status != "failed" || len(res.Steps) != 7 || res.Complete {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if len(f.doc(t).ServedSites()) != 0 {
		t.Fatal("a site without its SAML trust is served")
	}
	if len(f.pc.IdPs) != 1 || f.stored(t).APIKey == "" {
		t.Fatalf("what was done is not kept: idps=%v site=%+v", f.pc.IdPs, f.stored(t))
	}
}

func TestFinishSetupReusesOwnIdP(t *testing.T) {
	f := setupEnv(t)
	f.kc.FailClientCreate = 403
	f.run(t, f.admin(f.pin))
	f.kc.FailClientCreate = 0
	res := f.run(t, f.admin("")) // the pin confirmed by the first run is stored
	if !res.Complete || res.IdPChanged || len(f.pc.IdPs) != 1 {
		t.Fatalf("res = %+v, idps = %v", res, f.pc.IdPs)
	}
	if s := stepOf(res, "identity-provider"); s.Status != "ok" || strings.Contains(s.Message, "another") {
		t.Fatalf("identity-provider = %+v", s)
	}
	if s := stepOf(res, "service-account"); s.Status != "kept" {
		t.Fatalf("a working key was replaced: %+v", s)
	}
}

func TestRemoveAfterRefusedTrustDeletesTheProvider(t *testing.T) {
	f := setupEnv(t)
	f.kc.FailClientCreate = 403
	f.run(t, f.admin(f.pin))
	if f.stored(t).Complete() {
		t.Fatal("a run stopped at the SAML trust made the site complete")
	}
	if objs := f.stored(t).PrismObjects(); !slices.ContainsFunc(objs, func(o string) bool { return strings.HasPrefix(o, "identity provider ") }) {
		t.Fatalf("the provider created is not among the site's objects: %v", objs)
	}
	deleted, left, err := f.b.RemoveFromPrismCentral(context.Background(), f.doc(t), "site-a", f.admin(""))
	if err != nil || len(f.pc.IdPs) != 0 {
		t.Fatalf("deleted=%v left=%v err=%v, idps=%v", deleted, left, err, f.pc.IdPs)
	}
}

func TestSetupSaysTheEarlierKeyStaysValid(t *testing.T) {
	f := setupEnv(t)
	// A key stored before Part C: no account id recorded, so the run
	// cannot tell it is the account's and issues a new one.
	if err := f.svc.Patch(context.Background(), func(d *Settings) { d.Sites[0].APIKey = "pasted-by-hand" }); err != nil {
		t.Fatal(err)
	}
	res := f.run(t, f.admin(f.pin))
	s := stepOf(res, "service-account")
	if s.Status != "ok" || !strings.Contains(s.Message, "the key stored before stays valid in Prism Central and is no longer held by Portico; revoke it there") {
		t.Fatalf("service-account = %+v", s)
	}
}

func TestSetupKeepsChosenDefaults(t *testing.T) {
	f := setupEnv(t)
	f.pc.Containers = []map[string]any{
		{"containerExtId": "sc-1", "name": "gold", "clusterExtId": "cl-1"},
		{"containerExtId": "sc-2", "name": "default-container-1", "clusterExtId": "cl-1"},
	}
	if err := f.svc.Patch(context.Background(), func(d *Settings) { d.Sites[0].ClusterExtID, d.Sites[0].StorageContainerExtID = "cl-1", "sc-1" }); err != nil {
		t.Fatal(err)
	}
	res := f.run(t, f.admin(f.pin))
	if s := stepOf(res, "defaults"); s.Status != "kept" || f.stored(t).StorageContainerExtID != "sc-1" {
		t.Fatalf("defaults = %+v, stored %q", s, f.stored(t).StorageContainerExtID)
	}
}

func TestSetupPicksDefaultContainer(t *testing.T) {
	f := setupEnv(t)
	f.pc.Containers = []map[string]any{
		{"containerExtId": "sc-1", "name": "gold", "clusterExtId": "cl-1"},
		{"containerExtId": "sc-2", "name": "default-container-1", "clusterExtId": "cl-1"},
	}
	f.run(t, f.admin(f.pin))
	if st := f.stored(t); st.ClusterExtID != "cl-1" || st.StorageContainerExtID != "sc-2" {
		t.Fatalf("defaults = %s %s", st.ClusterExtID, st.StorageContainerExtID)
	}
}

func TestSetupRightsMissingNamed(t *testing.T) {
	f := setupEnv(t)
	f.pc.AdminOps = []string{"Create_User", "Create_User_Key", "Update_Authorization_Policy"}
	res := f.run(t, f.admin(f.pin))
	if s := stepOf(res, "rights"); s.Status != "failed" || !strings.Contains(s.Message, "Create_Saml_Identity_Provider") || len(res.Steps) != 3 {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if f.pc.Created("/authn/users") {
		t.Fatal("created without the rights")
	}
}

func TestSetupRightsUnreadableSkips(t *testing.T) {
	f := setupEnv(t)
	f.pc.RolesForbidden = true
	res := f.run(t, f.admin(f.pin))
	if s := stepOf(res, "rights"); s.Status != "skipped" || !strings.Contains(s.Message, "could not read the account's rights") || !res.Complete {
		t.Fatalf("res = %+v", res)
	}
}

func TestSetupInvalidInput(t *testing.T) {
	f := setupEnv(t)
	if _, err := f.b.SetupSite(context.Background(), f.kc.Srv.URL, "nope", f.admin(f.pin)); err == nil {
		t.Fatal("an unknown site ran")
	}
	if _, err := f.b.SetupSite(context.Background(), f.kc.Srv.URL, "site-a", SetupInput{Username: "admin"}); err == nil {
		t.Fatal("ran without a password")
	}
}

// writesStore refuses the settings writes while fail is set.
type writesStore struct {
	store.Store
	fail *bool
}

func (s writesStore) SetSetting(ctx context.Context, k string, v []byte) error {
	if *s.fail {
		return errors.New("disk full")
	}
	return s.Store.SetSetting(ctx, k, v)
}

// A key that could not be stored is a key nobody holds: the run revokes
// it and the step says so.
func TestSetupRevokesTheKeyWhenTheStoreFails(t *testing.T) {
	f := setupEnv(t)
	fail := false
	f.svc = NewService(writesStore{f.svc.st, &fail}, f.svc.sealer)
	f.b = NewBootstrapper(nil, f.svc)
	d := f.doc(t)
	d.Sites[0].TLSPin = f.pin
	if err := f.svc.Set(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	fail = true
	res := f.run(t, f.admin(""))
	if s := stepOf(res, "service-account"); s.Status != "failed" || !strings.Contains(s.Message, "so the new key was revoked") || res.Complete {
		t.Fatalf("steps = %+v", res.Steps)
	}
	if len(f.pc.DeletedKeys) != 1 {
		t.Fatalf("revoked keys = %v", f.pc.DeletedKeys)
	}
}
