package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeKCAdmin is an httptest Keycloak for the bootstrap: the master
// token (password grant for kcadmin/adm-pw-1), realm creation, clients,
// secrets, service accounts and realm-management role mappings.
type fakeKCAdmin struct {
	srv       *httptest.Server
	mu        sync.Mutex
	realmReps map[string]map[string]any
	clients   map[string]map[string]any // uuid → representation
	mapped    map[string][]string       // userID/clientUUID → roles
	mappers   map[string][]map[string]any
	tokens    int
	// descriptorEntityID is what the realm's SAML descriptor names;
	// adminSecret is the portico-admin client secret the realm token
	// endpoint accepts.
	descriptorEntityID, adminSecret string
	onClients                       func() // called when the clients are listed (a save made meanwhile)
	masterTokenStatus               int    // when set, the master token endpoint answers this status
	// masterDeleted: the temporary administrator kcadmin was deleted
	// from the master realm (its password no longer gets a token);
	// onMasterDelete runs just before.
	masterDeleted  bool
	onMasterDelete func()
	// resetAdmin: the admin API drops the connection (Keycloak
	// restarting with its public name).
	resetAdmin bool
}

func newFakeKCAdmin(t *testing.T) *fakeKCAdmin {
	f := &fakeKCAdmin{realmReps: map[string]map[string]any{}, clients: map[string]map[string]any{}, mapped: map[string][]string{}, mappers: map[string][]map[string]any{},
		descriptorEntityID: "https://id.example.test/realms/msp", adminSecret: "secret-of-portico-admin"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// clientUUID is the fake's internal id for a clientId: real ids are
// UUIDs, so a clientId that is a URL must not leak slashes into paths.
func clientUUID(clientID string) string {
	return "cli-" + strings.NewReplacer("/", "_", ":", "_").Replace(clientID)
}

func (f *fakeKCAdmin) client(clientID string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.clients {
		if c["clientId"] == clientID {
			return c
		}
	}
	return nil
}

func (f *fakeKCAdmin) mappedRoles(clientID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mapped["sa-"+clientID+"/cli-realm-management"]
}

func (f *fakeKCAdmin) realms() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.realmReps)
}

func (f *fakeKCAdmin) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/realms/master/protocol/openid-connect/token" {
		if f.masterTokenStatus != 0 {
			w.WriteHeader(f.masterTokenStatus)
			return
		}
		r.ParseForm()
		if f.masterDeleted || r.Form.Get("grant_type") != "password" || r.Form.Get("client_id") != "admin-cli" || r.Form.Get("username") != "kcadmin" || r.Form.Get("password") != "adm-pw-1" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		f.tokens++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":60}`, f.tokens)
		return
	}
	if realm, ok := strings.CutPrefix(r.URL.Path, "/realms/"); ok {
		name, rest, _ := strings.Cut(realm, "/")
		switch rest {
		case "protocol/openid-connect/token":
			r.ParseForm()
			if _, exists := f.realmReps[name]; !exists || r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "portico-admin" || r.Form.Get("client_secret") != f.adminSecret {
				w.WriteHeader(401)
				return
			}
			f.tokens++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":60}`, f.tokens)
			return
		case "protocol/saml/descriptor":
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<?xml version="1.0"?><EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="%s"><IDPSSODescriptor/></EntityDescriptor>`, f.descriptorEntityID)
			return
		}
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
		w.WriteHeader(401)
		return
	}
	if f.resetAdmin {
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			conn.Close()
		}
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(raw, &body)
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/admin/realms/master/users" && r.Method == "GET" {
		out := []map[string]any{}
		if !f.masterDeleted && r.URL.Query().Get("username") == "kcadmin" {
			out = append(out, map[string]any{"id": "u-kcadmin", "username": "kcadmin"})
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	if r.URL.Path == "/admin/realms/master/users/u-kcadmin" && r.Method == "DELETE" {
		if f.onMasterDelete != nil {
			f.onMasterDelete()
		}
		f.masterDeleted = true
		w.WriteHeader(204)
		return
	}
	if r.URL.Path == "/admin/realms" && r.Method == "POST" {
		name := body["realm"].(string)
		f.realmReps[name] = body
		f.clients["cli-realm-management"] = map[string]any{"id": "cli-realm-management", "clientId": "realm-management"}
		w.WriteHeader(201)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/admin/realms/")
	realm, p, _ := strings.Cut(rest, "/")
	if _, ok := f.realmReps[realm]; !ok {
		w.WriteHeader(404)
		return
	}
	parts := strings.Split(p, "/")
	switch {
	case p == "" && r.Method == "GET":
		json.NewEncoder(w).Encode(f.realmReps[realm])
	case p == "clients" && r.Method == "GET":
		if f.onClients != nil {
			f.onClients()
		}
		out := []map[string]any{}
		for _, c := range f.clients {
			if c["clientId"] == r.URL.Query().Get("clientId") {
				out = append(out, c)
			}
		}
		json.NewEncoder(w).Encode(out)
	case p == "clients" && r.Method == "POST":
		id := clientUUID(body["clientId"].(string))
		body["id"] = id
		f.clients[id] = body
		w.Header().Set("Location", f.srv.URL+r.URL.Path+"/"+id)
		w.WriteHeader(201)
	case len(parts) == 2 && parts[0] == "clients" && r.Method == "PUT":
		body["id"] = parts[1]
		f.clients[parts[1]] = body
		w.WriteHeader(204)
	case len(parts) == 3 && parts[0] == "clients" && parts[2] == "client-secret":
		json.NewEncoder(w).Encode(map[string]any{"value": "secret-of-" + strings.TrimPrefix(parts[1], "cli-")})
	case len(parts) == 3 && parts[0] == "clients" && parts[2] == "service-account-user":
		json.NewEncoder(w).Encode(map[string]any{"id": "sa-" + strings.TrimPrefix(parts[1], "cli-")})
	case len(parts) == 4 && parts[0] == "clients" && parts[2] == "roles":
		json.NewEncoder(w).Encode(map[string]any{"id": "role-" + parts[3], "name": parts[3]})
	case len(parts) == 4 && parts[0] == "clients" && parts[2] == "protocol-mappers":
		if r.Method == "POST" {
			f.mappers[parts[1]] = append(f.mappers[parts[1]], body)
			w.WriteHeader(201)
			return
		}
		out := f.mappers[parts[1]]
		if out == nil {
			out = []map[string]any{}
		}
		json.NewEncoder(w).Encode(out)
	case len(parts) == 5 && parts[0] == "users" && parts[2] == "role-mappings":
		key := parts[1] + "/" + parts[4]
		if r.Method == "POST" {
			var reps []map[string]any
			json.Unmarshal(raw, &reps)
			for _, rep := range reps {
				f.mapped[key] = append(f.mapped[key], rep["name"].(string))
			}
			w.WriteHeader(204)
			return
		}
		out := []map[string]any{}
		for _, n := range f.mapped[key] {
			out = append(out, map[string]any{"id": "role-" + n, "name": n})
		}
		json.NewEncoder(w).Encode(out)
	default:
		w.WriteHeader(404)
	}
}

func TestKeycloakBootstrapCreatesRealmAndClients(t *testing.T) {
	kc := newFakeKCAdmin(t)
	b := NewBootstrapper(nil, nil)
	s := Defaults()
	s.PortalHost, s.IDHost = "prism.example.test", "id.example.test"
	res, out, err := b.Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "adm-pw-1", Realm: "msp"})
	if err != nil || !res.OK || out.Keycloak.Realm != "msp" || out.OIDC.Issuer != "https://id.example.test/realms/msp" || out.Keycloak.AdminClientSecret == "" || out.OIDC.ClientSecret == "" {
		t.Fatalf("res=%+v out=%+v err=%v", res, out, err)
	}
	if out.Keycloak.AdminClientID != "portico-admin" || out.OIDC.ClientID != "portico" || out.Keycloak.AdminClientSecret != "secret-of-portico-admin" || out.OIDC.ClientSecret != "secret-of-portico" {
		t.Fatalf("out = %+v", out)
	}
	if got := kc.client("portico"); got["frontchannelLogout"] != false || got["redirectUris"].([]any)[0] != "https://prism.example.test/.portico/callback" || got["attributes"].(map[string]any)["pkce.code.challenge.method"] != "S256" {
		t.Fatalf("oidc client = %v", got)
	}
	if got := kc.client("portico-admin"); got["serviceAccountsEnabled"] != true || got["standardFlowEnabled"] != false {
		t.Fatalf("admin client = %v", got)
	}
	if roles := kc.mappedRoles("portico-admin"); len(roles) != 8 || !slices.Contains(roles, "manage-clients") {
		t.Fatalf("roles = %v", roles)
	}
	if res.Created[0] != "realm" || len(res.Created) != 3 {
		t.Fatalf("created = %v", res.Created)
	}
	if rep := kc.realmReps["msp"]; rep["loginWithEmailAllowed"] != true || rep["registrationEmailAsUsername"] != true || rep["displayName"] != "msp" {
		t.Fatalf("realm = %v", rep)
	}
	// Second run: nothing created, same secrets, still OK.
	res2, out2, _ := b.Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "adm-pw-1", Realm: "msp"})
	if !res2.OK || len(res2.Created) != 0 || out2.OIDC.ClientSecret != out.OIDC.ClientSecret {
		t.Fatalf("second run = %+v", res2)
	}
}

func TestKeycloakBootstrapPreconditionsAndRefusal(t *testing.T) {
	kc := newFakeKCAdmin(t)
	b := NewBootstrapper(nil, nil)
	var se *StateError
	if _, _, err := b.Keycloak(context.Background(), kc.srv.URL, Defaults(), KeycloakInput{AdminUsername: "a", AdminPassword: "b", Realm: "msp"}); !errors.As(err, &se) || !strings.Contains(err.Error(), "public names") {
		t.Fatalf("no hosts: %v", err)
	}
	s := Defaults()
	s.PortalHost, s.IDHost = "prism.example.test", "id.example.test"
	if _, _, err := b.Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "a", AdminPassword: "b", Realm: "Bad Realm"}); !errors.As(err, &se) {
		t.Fatalf("bad realm: %v", err)
	}
	if _, _, err := b.Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "", AdminPassword: "b", Realm: "msp"}); !errors.As(err, &se) {
		t.Fatalf("no username: %v", err)
	}
	res, _, err := b.Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "wrong", Realm: "msp"})
	if err != nil || res.OK || res.Message != "Keycloak refused the administrator credentials" || kc.realms() != 0 {
		t.Fatalf("wrong password: %+v %v realms=%d", res, err, kc.realms())
	}
	shortKeycloakWait(t)
	res, _, err = b.Keycloak(context.Background(), "http://127.0.0.1:1", s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "adm-pw-1", Realm: "msp"})
	if err != nil || res.OK || !strings.Contains(res.Message, "Keycloak is not answering") {
		t.Fatalf("keycloak down: %+v %v", res, err)
	}
}

// A card saved while the bootstrap runs survives: the bootstrap stores
// its own fields through Service.Patch instead of writing back the
// document it started from.
func TestKeycloakBootstrapKeepsASaveMadeMeanwhile(t *testing.T) {
	ctx := context.Background()
	kc := newFakeKCAdmin(t)
	svc := newService(t)
	s := Defaults()
	s.PortalHost, s.IDHost = "prism.example.test", "id.example.test"
	if err := svc.Set(ctx, s); err != nil {
		t.Fatal(err)
	}
	kc.onClients = func() {
		kc.onClients = nil
		if err := svc.Patch(ctx, func(d *Settings) { d.Tenants.Role = "role-9" }); err != nil {
			t.Error(err)
		}
	}
	res, _, err := NewBootstrapper(nil, svc).Keycloak(ctx, kc.srv.URL, s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "adm-pw-1", Realm: "msp"})
	if err != nil || !res.OK || !res.Stored {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	got, err := svc.Get(ctx)
	if err != nil || got.Keycloak.Realm != "msp" || got.Tenants.Role != "role-9" || got.OIDC.ClientSecret == "" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

// A token endpoint answering something other than 401 is reported with
// its status, not as refused credentials.
func TestKeycloakBootstrapNamesTheTokenStatus(t *testing.T) {
	kc := newFakeKCAdmin(t)
	kc.masterTokenStatus = 503
	s := Defaults()
	s.PortalHost, s.IDHost = "prism.example.test", "id.example.test"
	res, _, err := NewBootstrapper(nil, nil).Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "x", Realm: "msp"})
	if err != nil || res.OK || res.Message != "Keycloak answered status 503 at the token endpoint" {
		t.Fatalf("token 503: %+v %v", res, err)
	}
}

// The realm the bootstrap creates carries the password policy; a realm
// that already exists is left as it is (adoption is step 2).
func TestKeycloakBootstrapSetsAPasswordPolicy(t *testing.T) {
	kc := newFakeKCAdmin(t)
	s := Defaults()
	s.PortalHost, s.IDHost = "prism.example.test", "id.example.test"
	res, _, err := NewBootstrapper(nil, nil).Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "adm-pw-1", Realm: "msp"})
	if err != nil || !res.OK {
		t.Fatalf("bootstrap: %+v %v", res, err)
	}
	kc.mu.Lock()
	got := kc.realmReps["msp"]["passwordPolicy"]
	kc.mu.Unlock()
	if got != RealmPasswordPolicy || RealmPasswordPolicy != "length(12) and notUsername and notEmail" {
		t.Fatalf("passwordPolicy = %v", got)
	}
}

// Review of Milestone 3: saving the public names restarts Keycloak (the
// hostname file), and the Identity card is the next thing an administrator
// does. The bootstrap waits for Keycloak to answer (bounded) instead of
// failing on the restart.
func TestKeycloakBootstrapWaitsForARestartingKeycloak(t *testing.T) {
	kc := newFakeKCAdmin(t)
	var down atomic.Int32
	down.Store(3) // three refused calls, as while the container restarts
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() > 0 {
			down.Add(-1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		kc.handle(w, r)
	}))
	t.Cleanup(front.Close)
	shortKeycloakWait(t)
	KeycloakReadyWait = 5 * time.Second
	b := NewBootstrapper(nil, nil)
	s := Defaults()
	s.PortalHost, s.IDHost = "prism.example.test", "id.example.test"
	res, _, err := b.Keycloak(context.Background(), front.URL, s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "adm-pw-1", Realm: "msp"})
	if err != nil || !res.OK {
		t.Fatalf("bootstrap during a restart: %+v %v", res, err)
	}
	// Still down after the wait: the message says so.
	down.Store(1 << 20)
	KeycloakReadyWait = 50 * time.Millisecond
	res, _, _ = b.Keycloak(context.Background(), front.URL, s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "adm-pw-1", Realm: "msp"})
	if res.OK || !strings.Contains(res.Message, "restarts") {
		t.Fatalf("down Keycloak: %+v", res)
	}
}

// shortKeycloakWait makes the bootstrap's wait for Keycloak short for one
// test and restores it afterwards.
func shortKeycloakWait(t *testing.T) {
	t.Helper()
	w, p := KeycloakReadyWait, KeycloakReadyPoll
	KeycloakReadyWait, KeycloakReadyPoll = 50*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { KeycloakReadyWait, KeycloakReadyPoll = w, p })
}

// writeBootstrapFile is the installer's copy of the temporary
// administrator, readable by portico alone.
func writeBootstrapFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "keycloak-bootstrap.env")
	if err := os.WriteFile(p, []byte("KC_BOOTSTRAP_ADMIN_USERNAME=kcadmin\nKC_BOOTSTRAP_ADMIN_PASSWORD=adm-pw-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func namedSettings(t *testing.T, svc *Service) Settings {
	s := Defaults()
	s.PortalHost, s.IDHost = "prism.example.test", "id.example.test"
	if err := svc.Set(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestKeycloakAutoCreatesTheRealmAndDeletesTheTemporaryAdmin(t *testing.T) {
	ctx := context.Background()
	kc := newFakeKCAdmin(t)
	svc := newService(t)
	s := namedSettings(t, svc)
	file := writeBootstrapFile(t)
	storedBeforeDelete := false
	kc.onMasterDelete = func() {
		got, _ := svc.Get(ctx)
		storedBeforeDelete = got.Keycloak.Realm == "portico" && got.OIDC.ClientSecret != ""
	}
	res, out, err := NewBootstrapper(nil, svc).KeycloakAuto(ctx, kc.srv.URL, s, file)
	if err != nil || !res.OK || out.Keycloak.Realm != "portico" || out.OIDC.Issuer != "https://id.example.test/realms/portico" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if rep := kc.realmReps["portico"]; rep["displayName"] != "Portico MSP" {
		t.Fatalf("realm %v", rep)
	}
	if !kc.masterDeleted || !storedBeforeDelete {
		t.Fatalf("temporary admin deleted %v, settings stored before %v", kc.masterDeleted, storedBeforeDelete)
	}
	if _, err := os.Stat(file); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("bootstrap file left: %v", err)
	}
	if strings.Contains(res.Message, "adm-pw-1") {
		t.Fatal("password in the message")
	}
}

// A restart between "settings stored" and "temporary administrator
// deleted" (or between the delete and the file's removal): the next run
// finishes the job and keeps the realm.
func TestKeycloakAutoAfterARestartBeforeTheDelete(t *testing.T) {
	ctx := context.Background()
	for _, adminGone := range []bool{false, true} {
		kc := newFakeKCAdmin(t)
		svc := newService(t)
		s := namedSettings(t, svc)
		if res, _, err := NewBootstrapper(nil, svc).Keycloak(ctx, kc.srv.URL, s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "adm-pw-1", Realm: "portico", DisplayName: "Portico MSP"}); err != nil || !res.OK {
			t.Fatalf("first run: %+v %v", res, err)
		}
		kc.masterDeleted = adminGone
		stored, _ := svc.Get(ctx)
		file := writeBootstrapFile(t)
		res, out, err := NewBootstrapper(nil, svc).KeycloakAuto(ctx, kc.srv.URL, stored, file)
		if err != nil || !res.OK || out.Keycloak.Realm != "portico" || out.OIDC.ClientSecret != stored.OIDC.ClientSecret {
			t.Fatalf("admin gone %v: res=%+v err=%v", adminGone, res, err)
		}
		if !kc.masterDeleted {
			t.Fatal("temporary admin not deleted")
		}
		if _, err := os.Stat(file); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("admin gone %v: bootstrap file left", adminGone)
		}
	}
}

// Public names changed after the realm exists (no temporary administrator
// any more): the OIDC client's redirect and the issuer follow, through
// Portico's own admin client.
func TestKeycloakAutoNameChangeUsesTheAdminClient(t *testing.T) {
	ctx := context.Background()
	kc := newFakeKCAdmin(t)
	svc := newService(t)
	s := namedSettings(t, svc)
	if res, _, err := NewBootstrapper(nil, svc).KeycloakAuto(ctx, kc.srv.URL, s, writeBootstrapFile(t)); err != nil || !res.OK {
		t.Fatalf("first run: %+v %v", res, err)
	}
	cur, _ := svc.Get(ctx)
	cur.PortalHost, cur.IDHost = "portal.new.test", "id.new.test"
	if err := svc.Set(ctx, cur); err != nil {
		t.Fatal(err)
	}
	res, out, err := NewBootstrapper(nil, svc).KeycloakAuto(ctx, kc.srv.URL, cur, filepath.Join(t.TempDir(), "absent.env"))
	if err != nil || !res.OK || out.OIDC.Issuer != "https://id.new.test/realms/portico" {
		t.Fatalf("res=%+v out=%+v err=%v", res, out.OIDC, err)
	}
	if got := kc.client("portico"); got["redirectUris"].([]any)[0] != "https://portal.new.test/.portico/callback" {
		t.Fatalf("oidc client %v", got)
	}
	if got, _ := svc.Get(ctx); got.OIDC.Issuer != "https://id.new.test/realms/portico" {
		t.Fatalf("issuer not stored: %s", got.OIDC.Issuer)
	}
	// No realm and no temporary administrator: the operator types an existing realm.
	var se *StateError
	if _, _, err := NewBootstrapper(nil, nil).KeycloakAuto(ctx, kc.srv.URL, Settings{PortalHost: "a.test", IDHost: "b.test"}, filepath.Join(t.TempDir(), "absent.env")); !errors.As(err, &se) || !errors.Is(err, ErrNoTemporaryAdmin) {
		t.Fatalf("no file, no realm: %v", err)
	}
}

// A dropped connection is Keycloak not answering (it restarts after the
// names are saved), not an error it answered: the realm job waits.
func TestKeycloakAutoDroppedConnectionIsNotAnswering(t *testing.T) {
	kc := newFakeKCAdmin(t)
	kc.resetAdmin = true
	svc := newService(t)
	s := namedSettings(t, svc)
	res, _, err := NewBootstrapper(nil, svc).KeycloakAuto(context.Background(), kc.srv.URL, s, writeBootstrapFile(t))
	if err != nil || res.OK || !strings.HasPrefix(res.Message, "Keycloak did not answer") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}
