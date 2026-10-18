# Milestone 2e: Bootstrap and Setup Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A fresh appliance (process YAML, empty store) starts in setup
mode and is configured from the setup page alone: Portico creates the
Keycloak realm and its clients, the Prism Central service account with
its API key and policy membership, and the SAML trust between the two,
from one-time administrator credentials it never stores; Apply then
brings the service up in running mode.

**Architecture:** `internal/settings` gets a draft notion (an incomplete
document is storable; `Missing()` names what Apply still needs) and a
`Bootstrapper` next to the `Checker`, with one method per bootstrap
(Keycloak, Prism Central, SAML) that calls the clients and returns a
result plus the updated document; the API handler stores it. The Keycloak
client gains a password-grant token source (master realm, `admin-cli`),
realm creation and client management; the Prism Central client gains a
Basic-auth constructor for the bootstrap call, IAM user/key/policy calls,
IdP creation and SP metadata parsing. `serve` starts in setup mode (admin
listener only) when the document is incomplete. The setup page gets three
bootstrap forms whose results come back as HTMX fragments.

**Tech Stack:** Go 1.27 standard library (`encoding/xml`,
`encoding/base64`), existing packages, HTMX 2.0.4 vendored. Lab: NTNX-01,
VM `portico-lab`.

**Spec:** `docs/superpowers/specs/2026-10-17-portico-design.md` sections
4.4 (SAML trust details), 4.5 (service account, Keycloak bootstrap), 4.7
(setup mode and drafts), 5, 9 row 2e; ADR 0003 (the API is the boundary).
Write-ups 04 (IdP with `redirectURL`, per-IdP SP metadata), 07 (service
account, key, `Project Manager_acp`), 13 (no SAML single logout), 14.

## Global Constraints

- All text on disk in English. Commit messages in English.
- Branch `m2e-bootstrap` from `main` (created, holds the design update
  and this plan); pull request merged by the executor, tag `m2e-bootstrap`.
- One-time credentials (Prism Central administrator, Keycloak
  administrator) exist in the request body and in the client built for
  that call only: never stored, never logged (the username may be
  logged, the password never), never echoed in a response or a page, and
  the password inputs of the setup page are rendered empty after every
  round trip.
- Generated secrets (API key, client secrets) go to the store sealed, as
  in 2d; a bootstrap response and the setup page report `*_set` booleans
  and names, never values.
- Every Keycloak client Portico creates has `frontchannelLogout: false`.
- The public path fails closed: in setup mode the public and pass-through
  listeners are not started.
- `settings import` from a YAML still requires a complete document (the
  file path keeps the Milestone 1 rule); only the store may hold a draft.
- `CGO_ENABLED=0`; `go vet ./...`, `gofmt -l cmd internal`,
  `go test ./...` clean at every commit; `lab/test_scripts.sh` green.
- Lab: Prism Central `https://10.23.210.175:9440`, VM `10.23.210.190`,
  secrets in `~/portico-secrets/` (`prism-pw`, `keycloak-admin-pw`,
  `portico-admin-pw`). The lab reset in Task 6 deletes the lab IdP, the
  service account, the Keycloak realm and the Portico store on the VM
  (backed up first): it is announced in the handoff and run once.

## Review Focus

Failure modes the spec implies but a single task's tests might miss; each
has a test in the task named:

1. Prism Central bootstrap with wrong credentials: expected 200 with
   `ok: false` and "Prism Central refused the credentials (401)", nothing
   stored, no account created, and the log never contains the password
   (Task 3).
2. Prism Central bootstrap failing after the key was created (policy not
   found): expected the key deleted again, `ok: false` naming the policy,
   nothing stored (Task 3).
3. SAML bootstrap while another identity provider exists: expected 409
   naming it and no Keycloak client created (Task 4).
4. Apply in setup mode with fields missing: expected 409 listing them and
   the public listener still off; with a complete document Apply returns
   `ErrRestart` and the next start is in running mode (Task 1).
5. Bootstrap responses and the setup page after a bootstrap: expected no
   generated key or secret anywhere in the body, and empty password inputs
   (Tasks 2 and 5).

---

### Task 0: Design and plan recorded

**Files:**
- Modify: `docs/superpowers/specs/2026-10-17-portico-design.md` (done:
  4.4, 4.5, 4.7, 8, 9)
- Create: `docs/superpowers/plans/2026-10-18-milestone-2e-bootstrap.md`
  (this file)

- [ ] **Step 1: Commit**

```bash
git add docs/superpowers/specs/2026-10-17-portico-design.md docs/superpowers/plans/2026-10-18-milestone-2e-bootstrap.md
git commit -m "docs: Milestone 2e design (bootstrap, setup mode) and plan"
```

---

### Task 1: Drafts in the store and setup mode in `serve`

**Files:**
- Modify: `internal/settings/settings.go`, `internal/settings/service.go`
- Modify: `internal/api/settings.go`, `internal/api/api.go`
- Modify: `internal/web/web.go`, `internal/web/tenants.go`, `internal/web/setup.go`, `internal/web/templates/setup.html`
- Modify: `cmd/portico/serve.go`
- Test: `internal/settings/settings_test.go`, `internal/api/settings_test.go`, `internal/web/setup_test.go`, `internal/web/tenants_test.go`, `cmd/portico/serve_test.go`

**Interfaces:**
- Produces: `settings.Defaults() Settings` (tenants prefix `portico`,
  group prefix `tenant-`, role `Project Admin`, operation timeout `10m`,
  Prism Central `tls_verify` true, Keycloak admin client id
  `portico-admin`, OIDC client id `portico`).
- Produces: `(Settings) Missing() []string` in document order:
  `prism_host, id_host, prism_central.url, prism_central.api_key,
  prism_central.saml_idp_name, keycloak.realm, keycloak.admin_client_id,
  keycloak.admin_client_secret, oidc.issuer, oidc.client_id,
  oidc.client_secret, tenants.project_prefix, tenants.group_prefix,
  tenants.role, tenants.cluster_ext_id, tenants.storage_container_ext_id,
  tenants.operation_timeout` (a zero timeout counts as missing).
- Produces: `(Settings) ValidateFormat() error` (prefix regex and
  timeout sign as `config.ValidateRuntime` applies them, only on
  non-empty values; URL fields must parse with a host when non-empty).
- Produces: `type IncompleteError struct{ Missing []string }`, message
  `settings incomplete: missing <a>, <b>`; `(Settings) Validate() error`
  = `ValidateFormat` then `*IncompleteError` when `Missing()` is not
  empty. `Service.Set` and `Service.Update` run `ValidateFormat` only.
- Produces: `api.SettingsOptions.Mode string` (`"setup"` or
  `"running"`, set by `serve`); `Current` returns `settings.Defaults()`
  with source `"none"` when nothing is stored and no file is given;
  `Apply` returns the stored document's `*settings.IncompleteError`
  before the busy check.
- Produces: `GET /api/v1/settings` adds `mode`, `complete` (bool) and
  `missing` ([]string, `[]` when complete); `POST .../apply` answers 409
  `{"error":"settings incomplete: missing ..."}`.
- Produces: `api.New(nil, ...)` and `web.New` with `Service: nil` are
  valid (setup mode): tenant API routes answer 503
  `{"error":"Portico is in setup mode: complete the settings and apply them"}`,
  tenant pages render that sentence with a link to `/setup`, `GET /`
  redirects (303) to `/setup`.
- Produces: `runServe` starts in setup mode when the stored document is
  incomplete, or nothing is stored and `cfg.ValidateRuntime()` fails:
  logs `setup mode: the public and pass-through listeners are off`
  with `missing`, serves the admin listener only, honours Apply
  (`ErrRestart`). The branding hook in setup mode writes the theme files
  only.

- [ ] **Step 1: Write the failing settings tests**

In `internal/settings/settings_test.go` (replace the test asserting that
`Set` rejects an incomplete document, if any, by these):

```go
func TestMissingListsEmptyRequiredFields(t *testing.T) {
	var s Settings
	s.PrismHost = "prism.example.test"
	got := s.Missing()
	if len(got) != 16 || got[0] != "id_host" || got[len(got)-1] != "tenants.operation_timeout" {
		t.Fatalf("missing = %v", got)
	}
	full := sample() // the complete document helper of this file
	if m := full.Missing(); len(m) != 0 {
		t.Fatalf("complete document missing %v", m)
	}
}

func TestValidateNamesMissingFieldsAndFormat(t *testing.T) {
	var s Settings
	s.Tenants.ProjectPrefix = "Bad Prefix"
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "project_prefix") {
		t.Fatalf("format error: %v", err)
	}
	s = Defaults()
	var inc *IncompleteError
	if err := s.Validate(); !errors.As(err, &inc) || len(inc.Missing) == 0 || !strings.Contains(err.Error(), "missing prism_host, id_host") {
		t.Fatalf("incomplete error: %v", err)
	}
	if s.ValidateFormat() != nil {
		t.Fatal("defaults do not pass the format check")
	}
}

func TestServiceStoresADraft(t *testing.T) {
	svc := newService(t) // this file's helper over a temp store and a sealer
	d := Defaults()
	d.PrismHost, d.IDHost = "prism.example.test", "id.example.test"
	if err := svc.Set(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(context.Background())
	if err != nil || got.PrismHost != "prism.example.test" || len(got.Missing()) != 14 {
		t.Fatalf("draft = %+v %v", got, err)
	}
}
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/settings/ -run 'TestMissing|TestValidateNames|TestServiceStoresADraft' 2>&1 | tail -5`
Expected: FAIL (undefined: `Defaults`, `Missing`, `IncompleteError`).

- [ ] **Step 3: Implement `Defaults`, `Missing`, `ValidateFormat`, `IncompleteError`, `Validate` in `internal/settings/settings.go`; `Set`/`Update` call `ValidateFormat`**

`Validate` keeps its name because `settings import` relies on it meaning
"complete and well formed".

- [ ] **Step 4: Run the settings package**

Run: `go test ./internal/settings/ 2>&1 | tail -3`
Expected: PASS (the `check_test.go` tests untouched).

- [ ] **Step 5: Write the failing API and web tests**

`internal/api/settings_test.go`:

```go
func TestGetSettingsReportsModeAndMissing(t *testing.T) {
	e := newSettingsEnv(t, false) // nothing stored, no file
	e.h.so.Mode = "setup"
	rec, out := e.do("GET", "/api/v1/settings", "", true)
	if rec.Code != 200 || out["mode"] != "setup" || out["complete"] != false || out["stored"] != false {
		t.Fatalf("GET: %d %v", rec.Code, out)
	}
	missing, _ := out["missing"].([]any)
	if len(missing) != 17 || missing[0] != "prism_host" {
		t.Fatalf("missing = %v", missing)
	}
	if tenants, _ := out["tenants"].(map[string]any); tenants["project_prefix"] != "portico" || tenants["operation_timeout"] != "10m0s" {
		t.Fatalf("defaults not offered: %v", out["tenants"])
	}
}

func TestPutDraftThenApplyRefusedUntilComplete(t *testing.T) {
	e := newSettingsEnv(t, false)
	rec, out := e.do("PUT", "/api/v1/settings", `{"prism_host":"prism.example.test","id_host":"id.example.test","tenants":{"project_prefix":"portico","group_prefix":"tenant-","role":"Project Admin","operation_timeout":"10m"}}`, true)
	if rec.Code != 200 || out["stored"] != true || out["complete"] != false {
		t.Fatalf("PUT draft: %d %v", rec.Code, out)
	}
	rec, out = e.do("POST", "/api/v1/settings/apply", "", true)
	if rec.Code != 409 || !strings.Contains(out["error"].(string), "settings incomplete: missing prism_central.url") || *e.restarted != 0 {
		t.Fatalf("apply on a draft: %d %v restarted=%d", rec.Code, out, *e.restarted)
	}
}

func TestTenantRoutesAnswer503InSetupMode(t *testing.T) {
	e := newEnv(t)
	e.h = BasicAuth(e.st, "portico", nil)(New(nil, e.st, e.brand, nil, nil))
	for _, c := range []struct{ method, path string }{{"GET", "/api/v1/tenants"}, {"POST", "/api/v1/tenants"}, {"GET", "/api/v1/tenants/x/visibility"}} {
		rec, out := e.do(c.method, c.path, `{"slug":"x"}`, true)
		if rec.Code != 503 || out["error"] != "Portico is in setup mode: complete the settings and apply them" {
			t.Fatalf("%s %s: %d %v", c.method, c.path, rec.Code, out)
		}
	}
}
```

(`newSettingsEnv` is this file's 2d helper, whatever its name; `File`
stays nil for `stored == false` here, so add a parameter or a second
helper.)

`internal/web/setup_test.go`:

```go
func TestSetupPageInSetupModeListsMissingAndDisablesApply(t *testing.T) {
	e := newEnv(t, true)
	withSetup(t, e, false)
	e.web.setup.File, e.web.setup.Mode = nil, "setup"
	e.login(t)
	rec := e.do("GET", "/setup", nil, true)
	body := rec.Body.String()
	for _, want := range []string{"Setup mode", "Missing: prism_host, id_host", `value="portico"`, `disabled`} {
		if !strings.Contains(body, want) {
			t.Fatalf("setup page lacks %q: %s", want, body)
		}
	}
	rec = e.do("POST", "/setup/save", e.form("section", "hosts", "prism_host", "prism.example.test", "id_host", "id.example.test"), true)
	if rec.Code != 303 {
		t.Fatalf("save hosts on a fresh store: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do("GET", "/setup", nil, true)
	if body := rec.Body.String(); strings.Contains(body, "Missing: prism_host") || !strings.Contains(body, "Missing: prism_central.url") {
		t.Fatalf("missing list after save: %s", body)
	}
	rec = e.do("POST", "/setup/apply", e.form(), true)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "settings incomplete") {
		t.Fatalf("apply on a draft: %d", rec.Code)
	}
}
```

`internal/web/tenants_test.go`:

```go
func TestTenantPagesInSetupModePointToSetup(t *testing.T) {
	e := newEnv(t, true)
	e.web.svc = nil
	e.login(t)
	rec := e.do("GET", "/", nil, true)
	if rec.Code != 303 || rec.Header().Get("Location") != "/setup" {
		t.Fatalf("home: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = e.do("GET", "/tenants/new", nil, true)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "setup mode") || !strings.Contains(rec.Body.String(), `href="/setup"`) {
		t.Fatalf("tenants/new: %d %s", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 6: Run them**

Run: `go test ./internal/api/ ./internal/web/ -run 'SetupMode|Draft|ModeAndMissing' 2>&1 | tail -8`
Expected: FAIL (no `Mode` field, no `missing` key, nil service panics or 200).

- [ ] **Step 7: Implement**

- `api.SettingsOptions`: `Mode` field; `Current` falls back to
  `settings.Defaults()`; `Apply` checks `Validate()` of the stored
  document and returns its `*IncompleteError` (409 in both handlers:
  `applySettings` and `setupApply`).
- `settingsJSON`: `Mode string json:"mode"`, `Complete bool
  json:"complete"`, `Missing []string json:"missing"` (never null:
  `[]string{}` when complete); `toSettingsJSON(s, source, mode)`.
- `api.New`: a `running` wrapper on every tenant route returning 503 when
  `h.svc == nil`.
- `web`: `home` redirects to `/setup` when `h.svc == nil`; `routes`
  wraps the tenant handlers with `h.running`, which renders page
  `notfound`-style content "Portico is in setup mode: complete the
  settings and apply them. <a href="/setup">Open the setup page</a>" with
  status 503. `setupData` gains `Mode`, `Missing []string`, `Complete`;
  the template shows the banner `Setup mode: the public listener is off
  until every card is saved and applied.` when `Mode == "setup"`, the
  line `Missing: <comma list>` in the Apply card when not complete, and
  disables the Apply button unless `Stored && Complete`.
- `setupSave` with nothing stored and no file: `current()` returns the
  defaults; `Update` stores the draft.

- [ ] **Step 8: Run the API and web packages**

Run: `go test ./internal/api/ ./internal/web/ 2>&1 | tail -4`
Expected: PASS. If `TestSetupApplyWithoutStoredSettingsIsRefused` now
sees "settings incomplete" instead of "save them first", keep the
`ErrNotStored` branch first in `Apply` (nothing stored → "save them
first"; stored but incomplete → the missing list).

- [ ] **Step 9: Write the failing serve test**

`cmd/portico/serve_test.go`:

```go
func TestServeSetupModeWithEmptyStore(t *testing.T) {
	cfg := config.Default()
	cfg.Public.Listen, cfg.Internal.AdminListen, cfg.Internal.PassthroughListen = freeAddr(t), freeAddr(t), freeAddr(t)
	cfg.Store.Path, cfg.Store.MasterKeyFile = filepath.Join(t.TempDir(), "portico.db"), filepath.Join(t.TempDir(), "master.key")
	if err := cfg.ValidateProcess(); err != nil {
		t.Fatal(err)
	}
	st, _ := store.OpenSQLite(cfg.Store.Path)
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw-12345678"), bcrypt.MinCost)
	st.SetAdminPassword(context.Background(), "admin", string(hash))
	st.Close()
	var logBuf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, &cfg, slog.New(slog.NewTextHandler(&logBuf, nil))) }()
	waitHealthy(t, cfg.Internal.AdminListen)
	if _, err := net.DialTimeout("tcp", cfg.Public.Listen, 200*time.Millisecond); err == nil {
		t.Fatal("the public listener is up in setup mode")
	}
	if !strings.Contains(logBuf.String(), "setup mode") {
		t.Fatalf("log: %s", logBuf.String())
	}
	req, _ := http.NewRequest("GET", "http://"+cfg.Internal.AdminListen+"/api/v1/settings", nil)
	req.SetBasicAuth("admin", "pw-12345678")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != 200 || out["mode"] != "setup" {
		t.Fatalf("settings: %d %v", resp.StatusCode, out)
	}
	// A complete document stored through the API, then Apply → ErrRestart.
	body := `{"prism_host":"prism.test","id_host":"id.test","prism_central":{"url":"https://127.0.0.1:1","tls_verify":false,"api_key":"k","saml_idp_name":"idp"},"keycloak":{"realm":"x","admin_client_id":"portico-admin","admin_client_secret":"s"},"oidc":{"issuer":"https://id.test/realms/x","client_id":"portico","client_secret":"s"},"tenants":{"project_prefix":"portico","group_prefix":"tenant-","role":"Project Admin","cluster_ext_id":"cl","storage_container_ext_id":"sc","operation_timeout":"10m"}}`
	req, _ = http.NewRequest("PUT", "http://"+cfg.Internal.AdminListen+"/api/v1/settings", strings.NewReader(body))
	req.SetBasicAuth("admin", "pw-12345678")
	req.Header.Set("Content-Type", "application/json")
	if resp, err = http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("PUT: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()
	req, _ = http.NewRequest("POST", "http://"+cfg.Internal.AdminListen+"/api/v1/settings/apply", nil)
	req.SetBasicAuth("admin", "pw-12345678")
	if resp, err = http.DefaultClient.Do(req); err != nil || resp.StatusCode != 202 {
		t.Fatalf("apply: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRestart) {
			t.Fatalf("runServe returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runServe did not restart")
	}
}
```

- [ ] **Step 10: Run it**

Run: `go test ./cmd/portico/ -run TestServeSetupModeWithEmptyStore 2>&1 | tail -5`
Expected: FAIL (`runServe` returns the `ValidateRuntime` error; no health).

- [ ] **Step 11: Implement setup mode in `cmd/portico/serve.go`**

After the store, key and settings service are open: compute `doc` and
`source`; `setupMode := (stored && doc.Validate() is *IncompleteError) ||
(!stored && cfg.ValidateRuntime() != nil)`. In setup mode: log the
warning with the missing list (or the runtime validation error for the
file case), build `setup` with `Mode: "setup"`, the branding service with
a hook that only writes the theme files, `web.New` with `Service: nil`,
`api.New(nil, ...)`, the admin listener only; the select loop is the
existing one (restart, ctx). Extract the admin-listener construction and
the listener loop into helpers so the two modes share them. In running
mode set `setup.Mode = "running"`.

- [ ] **Step 12: Run the package and the suite**

Run: `go test ./cmd/portico/ 2>&1 | tail -3 && go vet ./... && gofmt -l cmd internal && go test ./... 2>&1 | tail -15`
Expected: PASS, no vet output, no gofmt output.

- [ ] **Step 13: Commit**

```bash
git add internal/settings internal/api internal/web cmd/portico
git commit -m "feat: settings drafts and setup mode"
```

---

### Task 2: Keycloak bootstrap (realm, admin client, OIDC client)

**Files:**
- Modify: `internal/idp/keycloak/client.go` (token source, root paths)
- Create: `internal/idp/keycloak/admin.go` (realm and client management)
- Create: `internal/settings/bootstrap.go`, `internal/settings/bootstrap_keycloak.go`
- Modify: `internal/api/settings.go`, `internal/api/api.go`
- Test: `internal/idp/keycloak/admin_test.go`, `internal/settings/bootstrap_keycloak_test.go`, `internal/api/settings_test.go`

**Interfaces:**
- Consumes: `settings.Settings`, `Service.Update`, `api.SettingsOptions`.
- Produces (keycloak): `NewWithPassword(baseURL, realm, username, password string, httpc *http.Client, log *slog.Logger) *Client`
  (password grant on `/realms/master/protocol/openid-connect/token`,
  `client_id=admin-cli`; the client administers `realm`);
  `(*Client) CreateRealm(ctx, rep map[string]any) error` (POST
  `/admin/realms`, 201); `(*Client) FindClient(ctx, clientID string)
  (map[string]any, error)` (`/clients?clientId=`, exact match, `ErrNotFound`);
  `(*Client) EnsureClient(ctx, rep map[string]any) (id string, created bool, err error)`
  (find by `rep["clientId"]`, POST or PUT); `(*Client) ClientSecret(ctx, id string) (string, error)`
  (`/clients/{id}/client-secret` → `value`); `(*Client) ServiceAccountUserID(ctx, clientUUID string) (string, error)`;
  `(*Client) EnsureClientRoles(ctx, userID, targetClientID string, roles []string) (added []string, err error)`
  (target client found by `clientId`, mapped roles read from
  `/users/{uid}/role-mappings/clients/{cid}`, missing ones POSTed as an
  array of role representations read from `/clients/{cid}/roles/{name}`);
  `(*Client) EnsureProtocolMappers(ctx, clientUUID string, mappers []map[string]any) (added []string, err error)`
  (by `name` on `/clients/{id}/protocol-mappers/models`);
  `(*Client) SAMLDescriptor(ctx) ([]byte, error)` (GET
  `<base>/realms/<realm>/protocol/saml/descriptor`, no token, 1 MiB cap).
- Produces (settings): `type Bootstrapper struct` with
  `NewBootstrapper(log *slog.Logger) *Bootstrapper`;
  `type StateError struct{ Msg string }` (precondition failed, 409);
  `type KeycloakInput struct{ AdminUsername, AdminPassword, Realm, DisplayName string }`;
  `type KeycloakResult struct{ OK bool; Message, Realm, AdminClientID, ClientID, Issuer string; Created []string }`;
  `(*Bootstrapper) Keycloak(ctx, keycloakURL string, s Settings, in KeycloakInput) (KeycloakResult, Settings, error)`.
  Preconditions (StateError): `s.PrismHost` and `s.IDHost` set ("save
  the public names first: the clients need them"), `in.Realm` matching
  `^[a-z0-9][a-z0-9-]*$`, username and password given. Remote failures
  → `OK: false` with "Keycloak refused the administrator credentials" (a
  token error) or "Keycloak did not answer: <class>"; never a Go error
  for them. On success the returned Settings has `Keycloak.Realm`,
  `Keycloak.AdminClientID = "portico-admin"`, `Keycloak.AdminClientSecret`,
  `OIDC.Issuer = "https://" + s.IDHost + "/realms/" + realm`,
  `OIDC.ClientID = "portico"`, `OIDC.ClientSecret`; `Created` lists
  `realm`, `client portico-admin`, `client portico` for what was created
  (not updated).
- Produces (API): `POST /api/v1/settings/bootstrap/keycloak` body
  `{"admin_username","admin_password","realm","display_name"}` → 200
  `KeycloakResult` plus `"stored": true` when OK (the handler calls
  `Service.Update` with the returned document), 409 `{"error": StateError}`,
  400 on a malformed body, 500 on a store error. Log:
  `bootstrap keycloak` with `realm`, `user`, `ok`, `created`.
- `api.SettingsOptions.Bootstrapper *settings.Bootstrapper` (defaulted
  in `api.New` like `Checker`).

Client representations (exact values; `frontchannelLogout: false` on
all):

- realm: `{"realm": r, "enabled": true, "displayName": d (r when empty),
  "loginWithEmailAllowed": true, "registrationEmailAsUsername": true}`.
- admin client: `{"clientId":"portico-admin","protocol":"openid-connect","enabled":true,"name":"Portico admin","publicClient":false,"serviceAccountsEnabled":true,"standardFlowEnabled":false,"directAccessGrantsEnabled":false,"implicitFlowEnabled":false,"frontchannelLogout":false,"redirectUris":[],"webOrigins":[]}`;
  roles on `realm-management`: `manage-users view-users query-users
  query-groups view-realm manage-realm manage-clients view-clients`.
- OIDC client: `{"clientId":"portico","protocol":"openid-connect","enabled":true,"name":"Portico","publicClient":false,"standardFlowEnabled":true,"directAccessGrantsEnabled":false,"implicitFlowEnabled":false,"serviceAccountsEnabled":false,"frontchannelLogout":false,"redirectUris":["https://<prism_host>/.portico/callback"],"webOrigins":[],"attributes":{"pkce.code.challenge.method":"S256","post.logout.redirect.uris":"https://<prism_host>/"}}`.

- [ ] **Step 1: Write the failing keycloak client tests**

`internal/idp/keycloak/admin_test.go`, extending `fakeKC` (same file or
`client_test.go`): a master token endpoint
`/realms/master/protocol/openid-connect/token` accepting
`grant_type=password&client_id=admin-cli&username=kcadmin&password=<testAdminPassword>`
(401 otherwise), `POST /admin/realms` (records the body, 201),
`GET /admin/realms/<new>` (404 until created), `/clients` list by
`clientId`, `POST /clients` (201 with `Location`), `PUT /clients/{id}`,
`/clients/{id}/client-secret`, `/clients/{id}/service-account-user`,
`/users/{id}/role-mappings/clients/{cid}` GET/POST,
`/clients/{cid}/roles/{name}`, `/clients/{id}/protocol-mappers/models`
GET/POST, and `GET /realms/lab/protocol/saml/descriptor` (XML with
`entityID="https://id.example.test/realms/lab"`).

```go
func TestPasswordGrantUsesMasterRealmAndAdminCli(t *testing.T)      // token form fields; the password never appears in a log line
func TestCreateRealmPostsToAdminRoot(t *testing.T)                   // POST /admin/realms with the representation; Realm() 404 before, 200 after
func TestEnsureClientCreatesThenUpdates(t *testing.T)                // first call POST + created=true, second call PUT on the id + created=false
func TestEnsureClientRolesAddsOnlyMissing(t *testing.T)              // two of five mapped → POST carries three role representations
func TestEnsureProtocolMappersByName(t *testing.T)                   // existing "email" skipped, "groups" added
func TestSAMLDescriptorNeedsNoToken(t *testing.T)                    // no Authorization header on the request, body returned
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/idp/keycloak/ -run 'PasswordGrant|CreateRealm|EnsureClient|EnsureProtocol|SAMLDescriptor' 2>&1 | tail -5`
Expected: FAIL (undefined functions).

- [ ] **Step 3: Implement the token source and `admin.go`**

Refactor `accessToken` to use a `tokenURL` and a `form url.Values` kept
on the client (client credentials or password grant); `doOnce` treats a
path starting with `/admin/` as absolute under the base, otherwise
prefixes `/admin/realms/<realm>`. Error bodies stay unlogged (they may
echo the password).

- [ ] **Step 4: Run the package**

Run: `go test ./internal/idp/keycloak/ 2>&1 | tail -3`
Expected: PASS.

- [ ] **Step 5: Write the failing bootstrapper test**

`internal/settings/bootstrap_keycloak_test.go` with an inline httptest
Keycloak (`fakeKCAdmin`) answering the calls above, recording created
representations:

```go
func TestKeycloakBootstrapCreatesRealmAndClients(t *testing.T) {
	kc := newFakeKCAdmin(t)
	b := NewBootstrapper(nil)
	s := Defaults()
	s.PrismHost, s.IDHost = "prism.example.test", "id.example.test"
	res, out, err := b.Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "adm-pw-1", Realm: "msp"})
	if err != nil || !res.OK || out.Keycloak.Realm != "msp" || out.OIDC.Issuer != "https://id.example.test/realms/msp" || out.Keycloak.AdminClientSecret == "" || out.OIDC.ClientSecret == "" {
		t.Fatalf("res=%+v out=%+v err=%v", res, out, err)
	}
	if got := kc.client("portico"); got["frontchannelLogout"] != false || got["redirectUris"].([]any)[0] != "https://prism.example.test/.portico/callback" || got["attributes"].(map[string]any)["pkce.code.challenge.method"] != "S256" {
		t.Fatalf("oidc client = %v", got)
	}
	if roles := kc.mappedRoles("portico-admin"); len(roles) != 8 || !slices.Contains(roles, "manage-clients") {
		t.Fatalf("roles = %v", roles)
	}
	if res.Created[0] != "realm" || len(res.Created) != 3 {
		t.Fatalf("created = %v", res.Created)
	}
	// Second run: nothing created, same secrets, still OK.
	res2, _, _ := b.Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "adm-pw-1", Realm: "msp"})
	if !res2.OK || len(res2.Created) != 0 {
		t.Fatalf("second run = %+v", res2)
	}
}

func TestKeycloakBootstrapPreconditionsAndRefusal(t *testing.T) {
	kc := newFakeKCAdmin(t)
	b := NewBootstrapper(nil)
	var se *StateError
	if _, _, err := b.Keycloak(context.Background(), kc.srv.URL, Defaults(), KeycloakInput{AdminUsername: "a", AdminPassword: "b", Realm: "msp"}); !errors.As(err, &se) || !strings.Contains(err.Error(), "public names") {
		t.Fatalf("no hosts: %v", err)
	}
	s := Defaults()
	s.PrismHost, s.IDHost = "prism.example.test", "id.example.test"
	if _, _, err := b.Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "a", AdminPassword: "b", Realm: "Bad Realm"}); !errors.As(err, &se) {
		t.Fatalf("bad realm: %v", err)
	}
	res, _, err := b.Keycloak(context.Background(), kc.srv.URL, s, KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "wrong", Realm: "msp"})
	if err != nil || res.OK || res.Message != "Keycloak refused the administrator credentials" || kc.realms() != 0 {
		t.Fatalf("wrong password: %+v %v realms=%d", res, err, kc.realms())
	}
}
```

- [ ] **Step 6: Run them**

Run: `go test ./internal/settings/ -run KeycloakBootstrap 2>&1 | tail -5`
Expected: FAIL (undefined `NewBootstrapper`).

- [ ] **Step 7: Implement `bootstrap.go` (types, `StateError`, `errClass` reuse) and `bootstrap_keycloak.go`**

Order of calls: token (a token failure → refusal result); `Realm()`
→ `CreateRealm` on `ErrNotFound`; `EnsureClient(admin)` →
`ServiceAccountUserID` → `EnsureClientRoles(uid, "realm-management",
roles)` → `ClientSecret`; `EnsureClient(oidc)` → `ClientSecret`. Log one
line per created object (name only).

- [ ] **Step 8: Run the package**

Run: `go test ./internal/settings/ 2>&1 | tail -3`
Expected: PASS.

- [ ] **Step 9: Write the failing API test**

`internal/api/settings_test.go` (the env gets a fake Keycloak admin
server; reuse the settings fake by exporting a tiny copy or by an
`httptest` in this file):

```go
func TestBootstrapKeycloakStoresAndHidesSecrets(t *testing.T) {
	e := newSettingsEnv(t, false)
	e.putHosts(t) // PUT prism_host/id_host as a draft
	rec, out := e.do("POST", "/api/v1/settings/bootstrap/keycloak", `{"admin_username":"kcadmin","admin_password":"adm-pw-1","realm":"msp"}`, true)
	if rec.Code != 200 || out["ok"] != true || out["stored"] != true || out["issuer"] != "https://id.example.test/realms/msp" {
		t.Fatalf("bootstrap: %d %v", rec.Code, out)
	}
	for _, secret := range []string{"adm-pw-1", e.kcAdmin.adminSecret, e.kcAdmin.oidcSecret} {
		if strings.Contains(rec.Body.String(), secret) || strings.Contains(e.log.String(), secret) {
			t.Fatalf("secret %q leaked in the body or the log", secret)
		}
	}
	rec, out = e.do("GET", "/api/v1/settings", "", true)
	kc, _ := out["keycloak"].(map[string]any)
	if kc["realm"] != "msp" || kc["admin_client_secret_set"] != true || out["oidc"].(map[string]any)["client_secret_set"] != true {
		t.Fatalf("stored: %v", out)
	}
	rec, out = e.do("POST", "/api/v1/settings/bootstrap/keycloak", `{"admin_username":"kcadmin","admin_password":"x","realm":"msp"}`, true)
	if rec.Code != 200 || out["ok"] != false || out["stored"] != nil {
		t.Fatalf("refused bootstrap: %d %v", rec.Code, out)
	}
}

func TestBootstrapKeycloakNeedsHosts(t *testing.T) {
	e := newSettingsEnv(t, false)
	rec, out := e.do("POST", "/api/v1/settings/bootstrap/keycloak", `{"admin_username":"kcadmin","admin_password":"adm-pw-1","realm":"msp"}`, true)
	if rec.Code != 409 || !strings.Contains(out["error"].(string), "public names") {
		t.Fatalf("%d %v", rec.Code, out)
	}
}
```

- [ ] **Step 10: Run, implement the handler and route, run again**

Run: `go test ./internal/api/ -run BootstrapKeycloak 2>&1 | tail -5`
Expected: FAIL (404), then PASS after `bootstrapKeycloak` in
`internal/api/settings.go` and the route in `api.go`.

- [ ] **Step 11: Suite and commit**

Run: `go vet ./... && gofmt -l cmd internal && go test ./... 2>&1 | tail -15`
Expected: PASS, no output from vet and gofmt.

```bash
git add internal/idp/keycloak internal/settings internal/api
git commit -m "feat: Keycloak bootstrap (realm, admin client, OIDC client)"
```

---

### Task 3: Prism Central bootstrap (service account, key, policy)

**Files:**
- Modify: `internal/nutanix/client.go` (auth function), `internal/nutanix/iam.go`
- Create: `internal/nutanix/bootstrap.go`
- Create: `internal/settings/bootstrap_prism.go`
- Modify: `internal/api/settings.go`, `internal/api/api.go`
- Test: `internal/nutanix/bootstrap_test.go` (+ `fakepc_test.go` routes), `internal/settings/bootstrap_prism_test.go`, `internal/api/settings_test.go`

**Interfaces:**
- Consumes: `Bootstrapper`, `StateError`, `CheckResult`, `Checker.PrismCentral` (the verification step).
- Produces (nutanix): `NewWithPassword(baseURL, username, password string, tlsCfg *tls.Config, log *slog.Logger) (*Client, error)`
  (Basic auth; `New` keeps the API key; both set an `auth func(*http.Request)`);
  `type User struct{ ExtID, Username, UserType string }`;
  `(*Client) FindUser(ctx, username string) (*User, error)` (GET
  `PathIAM0/authn/users` with `$filter=username eq '<x>'`, exact match,
  `notFound` otherwise);
  `(*Client) CreateServiceAccount(ctx, username, displayName, description, email string) (*User, error)`
  (POST `PathIAM1/authn/users` `{username, userType:"SERVICE_ACCOUNT", displayName, description, emailId}`);
  `type APIKey struct{ ExtID, Name, Key string; ExpiryTime time.Time }`;
  `(*Client) CreateAPIKey(ctx, userExtID, name, description string) (*APIKey, error)`
  (POST `PathIAM1/authn/users/{id}/keys` `{name, keyType:"API_KEY", description}`,
  `data.keyDetails.apiKey`, `data.expiryTime`);
  `(*Client) DeleteAPIKey(ctx, userExtID, keyExtID string) error` (ETag + DELETE);
  `type AuthorizationPolicy struct{ ExtID, DisplayName string }`;
  `(*Client) FindAuthorizationPolicy(ctx, displayName string) (*AuthorizationPolicy, error)`;
  `(*Client) AddPolicyUser(ctx, policyExtID, userExtID string) (added bool, err error)`
  (GET with ETag; already present → false; else strip `$reserved`,
  `$objectType`, `links`, `tenantId`, `createdBy`, `createdTime`,
  `lastUpdatedTime`, `extId`, `isSystemDefined`, `clientName`,
  `assignedUserGroupsCount`, `assignedUsersCount` and each entity's
  `$reserved`, append to the first `identities[].identityFilter.user.uuid.anyof`,
  PUT with `If-Match`; no such identity → error "policy has no user
  identity filter").
- Produces (settings): `type PrismInput struct{ Username, Password, URL string; TLSVerify bool; CAPEM, ServiceAccount string }`
  (`ServiceAccount` defaults to `portico`);
  `type PrismResult struct{ CheckResult; ServiceAccount, ServiceAccountExtID, KeyName string; KeyExpires time.Time; Created []string }`;
  `(*Bootstrapper) PrismCentral(ctx, s Settings, in PrismInput) (PrismResult, Settings, error)`.
  Preconditions (StateError): username, password and URL given, URL
  parses with a host, service account name matches `^[a-z0-9][a-z0-9._-]*$`.
  Remote failures → `OK: false`: 401/403 on the first call → "Prism
  Central refused the credentials (<status>)"; policy not found →
  "authorization policy \"Project Manager_acp\" not found"; unreachable →
  `prismError` wording. Steps: `FindUser` or `CreateServiceAccount`
  (display name "Portico service account", description "Portico: tenant
  provisioning", email `<name>@<prism_host>` or `<name>@portico.invalid`
  when the host is empty); `CreateAPIKey` named
  `portico-<yyyymmdd-hhmmss>` (UTC), description "Portico appliance";
  `FindAuthorizationPolicy("Project Manager_acp")` → `AddPolicyUser`;
  verify with `Checker.PrismCentral` using the new key (its
  clusters/containers fill the result); any failure after the key exists
  → `DeleteAPIKey` best effort, `OK: false`. On success the returned
  Settings has `PrismCentral.URL/TLSVerify/CAPEM/APIKey`; `Created` lists
  `service account <name>`, `api key <keyname>`, `policy member`.
- Produces (API): `POST /api/v1/settings/bootstrap/prism-central` body
  `{"username","password","url","tls_verify","ca_pem","service_account"}`
  → 200 `PrismResult` (+ `"stored": true` when OK and the document was
  updated), 409 StateError, 400 malformed. Log `bootstrap prism central`
  with `url`, `user`, `service_account`, `ok`, `created`, and
  `key_expires` on success.

- [ ] **Step 1: Write the failing nutanix tests**

Extend `fakePC`: Basic auth accepted as `admin:pc-pw-1` on the IAM
routes below (the API key stays valid too); `GET PathIAM0/authn/users`
(filter by username, returns `[]` or the recorded user);
`POST PathIAM1/authn/users` (201, extId `sa-1`);
`POST PathIAM1/authn/users/sa-1/keys` (201, `{"extId":"key-1","name":...,"keyDetails":{"apiKey":"k-new-456"},"expiryTime":"2126-10-18T00:00:00Z"}`);
`GET/DELETE PathIAM1/authn/users/sa-1/keys/key-1`;
`GET PathIAM1/authz/authorization-policies/pol-pm` (ETag, full body with
read-only fields and one user identity) and `PUT` on it (records the
body); the policy list gains `{"extId":"pol-pm","displayName":"Project Manager_acp"}`.

```go
func TestBasicAuthClientSendsNoAPIKey(t *testing.T)                    // Authorization: Basic base64(admin:pc-pw-1), no X-Ntnx-Api-Key header
func TestFindUserFiltersByUsername(t *testing.T)                        // $filter value, notFound on empty list
func TestCreateServiceAccountAndKey(t *testing.T)                       // bodies as pinned above; APIKey.Key == "k-new-456", ExpiryTime parsed
func TestAddPolicyUserStripsReadOnlyFieldsAndIsIdempotent(t *testing.T) // PUT body has no extId/links/$reserved, anyof grew by one, If-Match set; second call → added=false, no PUT
func TestDeleteAPIKeyUsesETag(t *testing.T)
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/nutanix/ -run 'BasicAuth|FindUser|CreateServiceAccount|AddPolicyUser|DeleteAPIKey' 2>&1 | tail -5`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement `bootstrap.go` and the auth function**

- [ ] **Step 4: Run the package**

Run: `go test ./internal/nutanix/ 2>&1 | tail -3`
Expected: PASS.

- [ ] **Step 5: Write the failing bootstrapper tests**

`internal/settings/bootstrap_prism_test.go` with an inline fake Prism
Central (`fakePCAdmin`) covering the IAM routes plus
`/config/clusters` and `/config/storage-containers` for the API key it
issues:

```go
func TestPrismBootstrapCreatesAccountKeyAndPolicyMember(t *testing.T)   // OK, Created has 3 entries, out.PrismCentral.APIKey == issued key, clusters filled, KeyName prefix "portico-"
func TestPrismBootstrapExistingAccountGetsNewKey(t *testing.T)          // user exists, in the policy → Created == ["api key ..."], OK
func TestPrismBootstrapWrongCredentials(t *testing.T)                   // 401 → OK false "Prism Central refused the credentials (401)", no POST recorded, returned Settings unchanged
func TestPrismBootstrapPolicyMissingDeletesTheKey(t *testing.T)         // policy list without Project Manager_acp → OK false naming it, DELETE on the key recorded
func TestPrismBootstrapPreconditions(t *testing.T)                      // empty password, bad URL, bad account name → StateError
```

- [ ] **Step 6: Run, implement `bootstrap_prism.go`, run the package**

Run: `go test ./internal/settings/ -run PrismBootstrap 2>&1 | tail -5` (FAIL), then `go test ./internal/settings/ 2>&1 | tail -3` (PASS).

- [ ] **Step 7: Write the failing API test**

```go
func TestBootstrapPrismCentralStoresKeyAndNeverLogsPassword(t *testing.T) {
	e := newSettingsEnv(t, false)
	e.putHosts(t)
	rec, out := e.do("POST", "/api/v1/settings/bootstrap/prism-central", `{"username":"admin","password":"pc-pw-1","url":"`+e.pcAdmin.URL+`","tls_verify":false}`, true)
	if rec.Code != 200 || out["ok"] != true || out["stored"] != true || out["service_account"] != "portico" || out["clusters"] == nil {
		t.Fatalf("bootstrap: %d %v", rec.Code, out)
	}
	for _, secret := range []string{"pc-pw-1", e.pcAdmin.issuedKey()} {
		if strings.Contains(rec.Body.String(), secret) || strings.Contains(e.log.String(), secret) {
			t.Fatalf("%q leaked", secret)
		}
	}
	rec, out = e.do("GET", "/api/v1/settings", "", true)
	if pc := out["prism_central"].(map[string]any); pc["api_key_set"] != true || pc["url"] != e.pcAdmin.URL {
		t.Fatalf("stored: %v", out)
	}
	rec, out = e.do("POST", "/api/v1/settings/bootstrap/prism-central", `{"username":"admin","password":"wrong","url":"`+e.pcAdmin.URL+`","tls_verify":false}`, true)
	if rec.Code != 200 || out["ok"] != false || !strings.Contains(out["message"].(string), "refused the credentials") {
		t.Fatalf("wrong password: %d %v", rec.Code, out)
	}
}
```

- [ ] **Step 8: Run, implement the handler and route, run again, suite, commit**

Run: `go test ./internal/api/ -run BootstrapPrism 2>&1 | tail -5` (FAIL then PASS); `go vet ./... && gofmt -l cmd internal && go test ./... 2>&1 | tail -15` (PASS).

```bash
git add internal/nutanix internal/settings internal/api
git commit -m "feat: Prism Central bootstrap (service account, API key, policy)"
```

---

### Task 4: SAML trust (identity provider in Prism Central, SAML client in Keycloak)

**Files:**
- Modify: `internal/nutanix/iam.go` (`SAMLIdP.EntityIssuer`, `CreateSAMLIdP`, `SPMetadata`, raw GET)
- Create: `internal/settings/bootstrap_saml.go`
- Modify: `internal/api/settings.go`, `internal/api/api.go`
- Test: `internal/nutanix/iam_test.go` (or `bootstrap_test.go`), `internal/settings/bootstrap_saml_test.go`, `internal/api/settings_test.go`

**Interfaces:**
- Consumes: Task 2 `SAMLDescriptor`, `EnsureClient`,
  `EnsureProtocolMappers`, `FindClient`; Task 3 `NewWithPassword`.
- Produces (nutanix): `SAMLIdP.EntityIssuer string json:"entityIssuer"`;
  `type SAMLIdPSpec struct{ Name string; MetadataXML []byte; RedirectURL string }`;
  `(*Client) CreateSAMLIdP(ctx, spec SAMLIdPSpec) (*SAMLIdP, error)`
  (POST `PathIAM1/authn/saml-identity-providers` with
  `{name, idpMetadataXml: base64(std), usernameAttribute:"email", emailAttribute:"email", groupsAttribute:"groups", isSignedAuthnReqEnabled:false, redirectURL}`);
  `(*Client) SPMetadata(ctx, idpExtID string) (entityID, acsURL string, err error)`
  (raw GET on `.../saml-identity-providers/{id}/sp-metadata`, body is
  XML, not the JSON envelope: `EntityDescriptor@entityID` and the first
  `SPSSODescriptor/AssertionConsumerService@Location`, measured
  2026-10-18 as `https://<prism_host>/api/iam/authn` and
  `.../api/iam/authn/callback`).
- Produces (settings): `type SAMLInput struct{ Name, Username, Password string }`
  (name defaults to `portico-keycloak`; credentials optional);
  `type SAMLResult struct{ OK bool; Message, IdPExtID, EntityID, ACSURL, KeycloakClientID string; UsedCredentials bool; Created []string }`;
  `(*Bootstrapper) SAML(ctx, keycloakURL string, s Settings, in SAMLInput) (SAMLResult, Settings, error)`.
  Preconditions (StateError): `PrismHost`, `IDHost`, `Keycloak.Realm`,
  `Keycloak.AdminClientSecret`, `PrismCentral.URL`, and
  `PrismCentral.APIKey` or both credentials ("the public names, the
  Keycloak realm with its admin client and the Prism Central connection
  must be set first"); name matches `^[a-z0-9][a-z0-9._-]*$`. Steps:
  `SAMLDescriptor` (refuse with `OK:false` "the identity provider's
  descriptor names <x>, expected https://<id_host>/realms/<realm>: check
  Keycloak's hostname setting" when the `entityID` attribute differs);
  Prism Central client = API key, or Basic when credentials are given;
  `ListSAMLIdPs`: one with another name → `StateError` "an identity
  provider named <x> already exists: Portico does not adopt an existing
  one"; one with our name → reuse (`Created` without `identity
  provider`); none → `CreateSAMLIdP` with `RedirectURL =
  "https://"+PrismHost`; a 403 from Prism Central on the list or the
  create, without credentials → `OK:false` "Prism Central refused the
  service account (403): give the credentials of an account that can
  manage identity providers"; `SPMetadata`; Keycloak realm client
  (`keycloak.New` with the stored admin client) → `EnsureClient(saml)` →
  `EnsureProtocolMappers`. On success the returned Settings has
  `PrismCentral.SAMLIdPName = name`; `UsedCredentials` says which auth
  the Prism Central side used.
- SAML client representation (write-up 04, lab script values):
  `{"clientId": entityID, "protocol":"saml","enabled":true,"name":"Prism Central","redirectUris":[acs],"baseUrl":acs,"adminUrl":acs,"frontchannelLogout":false,"attributes":{"saml.assertion.signature":"true","saml.server.signature":"true","saml.client.signature":"false","saml.force.post.binding":"true","saml.authnstatement":"true","saml_name_id_format":"email","saml_force_name_id_format":"true","saml.signature.algorithm":"RSA_SHA256","saml_assertion_consumer_url_post":acs,"saml_single_logout_service_url_post":"","saml_single_logout_service_url_redirect":""}}`;
  mappers `email` (`saml-user-property-mapper`, `user.attribute=email`,
  `attribute.name=email`, `attribute.nameformat=Basic`) and `groups`
  (`saml-group-membership-mapper`, `attribute.name=groups`,
  `attribute.nameformat=Basic`, `single=true`, `full.path=false`).
- Produces (API): `POST /api/v1/settings/bootstrap/saml` body
  `{"name","username","password"}` → 200 `SAMLResult` (+ `"stored": true`
  when OK), 409 StateError, 400 malformed. Log `bootstrap saml` with
  `name`, `ok`, `used_credentials`, `created`.

- [ ] **Step 1: Write the failing nutanix tests**

`fakePC`: `POST PathIAM1/authn/saml-identity-providers` (201 with
`extId: idp-new`, `entityIssuer`), `GET .../idp-new/sp-metadata`
answering `Content-Type: application/xml` with the measured XML shape,
and a switch so the list answers `[]` (`f.noIdP = true`).

```go
func TestCreateSAMLIdPEncodesMetadata(t *testing.T)   // body fields as pinned; idpMetadataXml decodes to the input XML; redirectURL present
func TestSPMetadataParsesEntityIDAndACS(t *testing.T) // "https://prism.example.test/api/iam/authn", ".../callback"
```

- [ ] **Step 2: Run (FAIL), implement, run the package (PASS)**

Run: `go test ./internal/nutanix/ 2>&1 | tail -3`

- [ ] **Step 3: Write the failing bootstrapper tests**

`internal/settings/bootstrap_saml_test.go` (reusing `fakePCAdmin` and
`fakeKCAdmin`, the latter also answering the realm admin client token
and the descriptor):

```go
func TestSAMLBootstrapCreatesIdPAndKeycloakClient(t *testing.T)      // OK, IdP POST recorded with redirectURL https://prism.example.test, Keycloak client clientId == entityID, redirectUris == [acs], two mappers, out.PrismCentral.SAMLIdPName == "portico-keycloak", UsedCredentials false
func TestSAMLBootstrapRefusesAnotherIdP(t *testing.T)                 // list has "corp-adfs" → StateError naming it, no Keycloak client call
func TestSAMLBootstrapDescriptorMismatch(t *testing.T)                // descriptor entityID on another host → OK false "check Keycloak's hostname setting", no Prism Central call
func TestSAMLBootstrapForbiddenAsksForCredentials(t *testing.T)       // 403 on create with the key → OK false with the message; with credentials → OK, UsedCredentials true
func TestSAMLBootstrapIsIdempotent(t *testing.T)                      // IdP with our name exists → reused, client updated, Created without "identity provider"
```

- [ ] **Step 4: Run (FAIL), implement `bootstrap_saml.go`, run the package (PASS)**

- [ ] **Step 5: Write the failing API test, implement, run**

```go
func TestBootstrapSAMLStoresIdPName(t *testing.T) {
	e := newSettingsEnv(t, true) // stored sample pointing at the fakes
	rec, out := e.do("POST", "/api/v1/settings/bootstrap/saml", `{"name":"portico-keycloak"}`, true)
	if rec.Code != 200 || out["ok"] != true || out["stored"] != true || out["entity_id"] != "https://prism.example.test/api/iam/authn" {
		t.Fatalf("bootstrap: %d %v", rec.Code, out)
	}
	rec, out = e.do("GET", "/api/v1/settings", "", true)
	if out["prism_central"].(map[string]any)["saml_idp_name"] != "portico-keycloak" {
		t.Fatalf("stored: %v", out)
	}
	e.pcAdmin.addIdP("corp-adfs")
	rec, out = e.do("POST", "/api/v1/settings/bootstrap/saml", `{"name":"other"}`, true)
	if rec.Code != 409 || !strings.Contains(out["error"].(string), "corp-adfs") {
		t.Fatalf("second IdP: %d %v", rec.Code, out)
	}
}
```

- [ ] **Step 6: Suite and commit**

Run: `go vet ./... && gofmt -l cmd internal && go test ./... 2>&1 | tail -15`
Expected: PASS.

```bash
git add internal/nutanix internal/settings internal/api
git commit -m "feat: SAML trust bootstrap (identity provider and Keycloak SAML client)"
```

---

### Task 5: Setup page: bootstrap forms and the SAML trust card

**Files:**
- Modify: `internal/web/setup.go`, `internal/web/templates/setup.html`, `internal/web/static/portico.css`
- Test: `internal/web/setup_test.go`

**Interfaces:**
- Consumes: `Bootstrapper` methods through `api.SettingsOptions`
  (`so.Bootstrapper`, `so.KeycloakURL`), `Service.Update`.
- Produces: route `POST /setup/bootstrap/{target}` with target
  `keycloak`, `prism-central`, `saml` (CSRF required; unknown target →
  404). Each answers an HTML fragment (template `bootstrap_<target>`:
  a banner with the message, a fresh copy of that bootstrap form so the
  password fields come back empty, and on success out-of-band swaps of
  the inputs it filled). Page changes: the `saml_idp_name` field moves
  from the Prism Central card to a new card `SAML trust` (section `saml`
  in `formSettings`, saving `PrismCentral.SAMLIdPName` only); the Prism
  Central section no longer reads `saml_idp_name` (keeps the current
  value).

Page layout (ids are the OOB anchors):

- Prism Central card: the connection form (`id="form-prism_central"`,
  fields `url`, `tls_verify`, `ca_pem`, `api_key` with `id="api_key"`)
  and below it `<div id="boot-prism_central">` holding the form
  `hx-post="/setup/bootstrap/prism-central" hx-include="#form-prism_central" hx-target="#boot-prism_central"`
  with `username`, `password` (`type="password" autocomplete="off"`),
  `service_account` (value `portico`) and the button `Create the service
  account`; the text above it: "Or let Portico create it: enter once the
  credentials of an account that can create users and edit authorization
  policies. They are used for this call and not stored." On success the
  fragment swaps `#api_key` (placeholder `unchanged`) and the two pickers
  (as the check fragment does).
- Identity card: form `id="form-keycloak"` (inputs with ids `realm`,
  `admin_client_id`, `admin_client_secret`, `issuer`, `client_id`,
  `client_secret`) and `<div id="boot-keycloak">` with the form
  `hx-post="/setup/bootstrap/keycloak" hx-include="#form-keycloak"` fields
  `admin_username`, `admin_password`, `display_name`, button `Create the
  realm and clients` (the realm name comes from `#form-keycloak`). On
  success the fragment swaps the six inputs (values and placeholders).
- SAML trust card (new, after Identity): form `section=saml` with
  `saml_idp_name` (`id="saml_idp_name"`, value from the document, default
  `portico-keycloak` when empty) and Save; `<div id="boot-saml">` with the
  form `hx-post="/setup/bootstrap/saml" hx-include="#form-saml"` fields
  `username`, `password` (optional, text: "needed only when Prism Central
  refuses the service account") and the button `Create the SAML trust`.
  On success the fragment swaps `#saml_idp_name`.
- Every bootstrap form carries the CSRF hidden input; the handler maps
  `StateError` to a 409 banner, `OK:false` to an error banner (status
  200), store errors to 500, and renders the same fragment in every case.

- [ ] **Step 1: Write the failing web tests**

```go
func TestSetupBootstrapKeycloakFragmentFillsIdentityCard(t *testing.T) {
	e := newEnv(t, true)
	_, _, _ = withSetup(t, e, false)
	e.web.setup.File = nil
	withBootstrapFakes(t, e) // fake Keycloak admin + fake Prism Central admin wired into e.web.setup
	e.login(t)
	e.do("POST", "/setup/save", e.form("section", "hosts", "prism_host", "prism.example.test", "id_host", "id.example.test"), true)
	rec := e.do("POST", "/setup/bootstrap/keycloak", e.form("admin_username", "kcadmin", "admin_password", "adm-pw-1", "realm", "msp"), true)
	body := rec.Body.String()
	for _, want := range []string{`id="issuer"`, `value="https://id.example.test/realms/msp"`, `hx-swap-oob`, `placeholder="unchanged"`, `name="admin_password" value=""`} {
		if rec.Code != 200 || !strings.Contains(body, want) {
			t.Fatalf("fragment lacks %q: %d %s", want, rec.Code, body)
		}
	}
	if strings.Contains(body, "adm-pw-1") || strings.Contains(body, "<html") {
		t.Fatal("password echoed or full page rendered")
	}
}

func TestSetupBootstrapPrismCentralFragmentAndRefusal(t *testing.T)   // success swaps #api_key and the pickers; wrong password → 200 banner error, api key still unset on GET /setup
func TestSetupBootstrapSAMLNeedsPreconditions(t *testing.T)           // fresh store → 409 fragment naming the preconditions
func TestSetupSAMLCardSavesTheIdPName(t *testing.T)                   // POST /setup/save section=saml → stored name; section=prism_central without saml_idp_name keeps it
func TestSetupBootstrapRequiresCSRF(t *testing.T)                     // three targets without token → 403, no backend call
```

Ruling to apply in the existing tests: `TestSetupSaveSectionAndSecretsStayHidden`
saves `saml_idp_name` through section `saml` now.

- [ ] **Step 2: Run them**

Run: `go test ./internal/web/ -run 'SetupBootstrap|SAMLCard' 2>&1 | tail -6`
Expected: FAIL (404 on the route).

- [ ] **Step 3: Implement `setupBootstrap`, the templates and the CSS for the bootstrap block (`.bootstrap` with a top border and muted text)**

- [ ] **Step 4: Run the web package and the suite**

Run: `go test ./internal/web/ 2>&1 | tail -3 && go vet ./... && gofmt -l cmd internal && go test ./... 2>&1 | tail -15`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web
git commit -m "feat: bootstrap forms and SAML trust card on the setup page"
```

---

### Task 6: Lab: fresh-appliance bootstrap on NTNX-01, write-up 15, documentation

**Files:**
- Modify: `lab/portico/deploy.sh` (`--fresh`: no import, no secret files needed)
- Create: `lab/portico/reset-for-bootstrap.sh`, `lab/portico/bootstrap-acceptance.sh`
- Modify: `lab/saml/keycloak-realm.sh` (admin-client roles gain `manage-clients view-clients`), `lab/tenants/acceptance.sh` (`PC_API_KEY` only when the file exists), `lab/test_scripts.sh`, `lab/README.md`
- Modify: `deploy/portico.yaml.example` (pre-created path: the roles, and a note that the setup page can create everything)
- Create: `docs/lab/2026-10-18-15-bootstrap-and-setup-mode.md`
- Modify: `docs/lab/README.md` (row 15), `docs/lab/lab-inventory.md`, `README.md` (Status), `docs/superpowers/specs/2026-10-17-portico-design.md` §9 row 2e "done"

**Interfaces:**
- Consumes: the three bootstrap endpoints, `GET /api/v1/settings`,
  `lab/tenants/api.sh`, `lab/saml/kc.sh`, `lab/saml/pc-idp.sh`,
  `lab/auth/create-service-account.sh delete`, `lab/projects/lib.sh`.

Scripts:

- `reset-for-bootstrap.sh --yes` (refuses without the flag, prints what
  it will do): stops `portico.service`; on the VM moves
  `/var/lib/portico/portico.db` to `portico.db.pre-2e-<timestamp>`
  (master key kept); removes tenant A's role membership and SAML group
  and unshares the IdP from `portico-lab-tenant-a` (the project and its
  VM stay), then `pc-idp.sh delete`; `create-service-account.sh delete`;
  `kc.sh DELETE /portico-lab` (realm with alice, carol, groups, clients,
  theme setting); removes `~/portico-secrets/portico-sa-key`,
  `portico-oidc-secret`, `portico-kc-admin-secret`; prints the inventory
  rows that changed.
- `deploy.sh --fresh`: builds and installs as today but skips the import
  branch and does not require or stage the three secret files; the
  break-glass password is set as today. Portico then starts in setup
  mode (`journalctl` shows `setup mode`).
- `bootstrap-acceptance.sh`: before the reset is not its business; it
  drives the API with `lab/tenants/api.sh`, credentials in JSON files
  built with `jq -n --rawfile` in a `umask 077` temp dir, never on a
  command line. Lines (got vs expected): GET settings → `mode=setup`;
  PUT hosts draft → 200 `stored=true complete=false`; apply → 409;
  bootstrap keycloak (kcadmin, `~/portico-secrets/keycloak-admin-pw`,
  realm `portico-lab`, display name `Portico lab`) → `ok=true`,
  `created` has 3 entries; bootstrap prism-central (admin,
  `~/portico-secrets/prism-pw`, URL, `tls_verify=false`) → `ok=true`,
  `clusters` non-empty; PUT tenants card (prefix `portico-lab`, cluster
  and container from the response, `15m`); bootstrap saml
  (`portico-lab-keycloak`, first without credentials; on `ok=false`
  with "refused the service account" retry with the credentials and
  record which worked); GET settings → `complete=true`, `missing=[]`;
  checks keycloak/oidc/prism_central → `ok=true`; apply → 202; health
  back within 60 s; GET settings → `mode=running`; branding re-uploaded
  (`PUT /api/v1/settings/branding` as in write-up 14); then
  `lab/tenants/acceptance.sh` is run by the executor (tenant B, carol)
  and the browser check as in write-up 11.
- A measurement line before the reset, while the old IdP still exists:
  `bootstrap/saml` with name `portico-e2e` → 409 naming
  `portico-lab-keycloak` (review focus 3 on the real Prism Central).

- [ ] **Step 1: Write the script checks in `lab/test_scripts.sh`**

```bash
# Milestone 2e Task 6: fresh-appliance bootstrap.
# M2e-1: the bootstrap acceptance builds credential bodies in private files, never on a command line.
if grep -q 'umask 077' lab/portico/bootstrap-acceptance.sh && grep -q -- '--rawfile' lab/portico/bootstrap-acceptance.sh && ! grep -nE 'password=\$|--arg (p|pw|password) "\$\(cat' lab/portico/bootstrap-acceptance.sh | grep -q .; then ok "bootstrap-acceptance.sh keeps credentials in files"; else ko "bootstrap-acceptance.sh exposes a credential"; fi
# M2e-2: the reset refuses to run without --yes.
out=$(LAB_VM_IP=x PC_URL=https://invalid.test PC_USER=x PC_PASS=x bash lab/portico/reset-for-bootstrap.sh 2>&1); rc=$?
if [ "$rc" -eq 2 ] && grep -q -- '--yes' <<<"$out"; then ok "reset-for-bootstrap.sh needs --yes"; else ko "reset-for-bootstrap.sh ran without --yes (rc=$rc)"; fi
# M2e-3: deploy.sh --fresh skips the import and needs no secret file.
if grep -q 'fresh' lab/portico/deploy.sh && grep -q 'import skipped' lab/portico/deploy.sh; then ok "deploy.sh has a --fresh path"; else ko "deploy.sh lacks --fresh"; fi
# M2e-4: the lab admin client has the client-management roles the SAML bootstrap needs.
if grep -q 'manage-clients' lab/saml/keycloak-realm.sh && grep -q 'manage-clients' deploy/portico.yaml.example; then ok "admin-client roles include manage-clients"; else ko "manage-clients missing from the lab script or the example"; fi
```

- [ ] **Step 2: Run them**

Run: `lab/test_scripts.sh 2>&1 | grep -E 'M2e|FAIL|bootstrap|reset|fresh|manage-clients'`
Expected: the four new lines FAIL.

- [ ] **Step 3: Write the scripts and the example change; run `lab/test_scripts.sh` (all ok) and `shellcheck` on the new scripts**

- [ ] **Step 4: Measure the refusal on the live lab, then reset, deploy fresh, bootstrap**

Run (in order, each output read):

```bash
source lab/env.sh
lab/portico/deploy.sh                      # current binary with the new endpoints, settings still stored
lab/portico/bootstrap-acceptance.sh --refusal-only   # 409 naming portico-lab-keycloak
lab/portico/reset-for-bootstrap.sh --yes
lab/portico/deploy.sh --fresh              # journal: setup mode
lab/portico/bootstrap-acceptance.sh        # every line ok
lab/tenants/acceptance.sh                  # tenant B active, carol created
```

Expected: every `line` prints `ok`; the journal of the fresh start has
`setup mode`; after Apply `mode=running` and `portico-lab-keycloak` is
listed by `lab/saml/pc-idp.sh list` with the new extId. Record whether
the service account could create the IdP (write-up 07 open item).

- [ ] **Step 5: Browser check (Chrome)**

Open `https://prism.lab.portico.test:8443/setup` after the fresh deploy
(setup-mode banner and the missing list; screenshot), and after the
acceptance open `https://prism.lab.portico.test/` as carol (temporary
password from `~/portico-secrets/carol-tmp-pw`, forced update, console
with tenant B only). Then `lab/tenants/acceptance.sh --delete`.

- [ ] **Step 6: Write-up 15 and documentation**

`docs/lab/2026-10-18-15-bootstrap-and-setup-mode.md` from `TEMPLATE.md`:
what was measured (setup mode start, the three bootstraps with timings,
the IdP refusal, whether `Project Manager_acp` may create an IdP, the
descriptor on the backchannel, the SP metadata shape), implications,
re-measure procedure (`reset-for-bootstrap.sh --yes`, `deploy.sh --fresh`,
`bootstrap-acceptance.sh`). `docs/lab/README.md` row 15 (and the
header "Milestones 0 to 2" unchanged). `docs/lab/lab-inventory.md`: the
IdP, service account, realm and store rows now say "recreated by the
2e bootstrap" with the new extIds; alice's row says removed with the
realm; the backup file row. `README.md` Status: Milestone 2e. Design §9
row 2e → done with the write-up number. `lab/README.md`: the three
scripts and `--fresh`. `deploy/portico.yaml.example`: the pre-created
path remains documented, the setup page is the default route.

- [ ] **Step 7: Run everything and commit**

Run: `lab/test_scripts.sh 2>&1 | grep -c '^ok' && go test ./... 2>&1 | tail -3`
Expected: every check ok, suite PASS.

```bash
git add lab deploy docs README.md
git commit -m "lab: fresh-appliance bootstrap on NTNX-01 (write-up 15)"
```

---

## Self-review notes

- Spec coverage: 4.5 bootstrap (Task 3), 4.5 Keycloak bootstrap (Task 2),
  4.4 SAML trust details (Task 4), 4.7 setup mode and drafts (Task 1),
  UI over the API (Task 5, ADR 0003), lab evidence and the open item of
  write-up 07 (Task 6). Key rotation and brownfield adoption stay step 2.
- Type consistency: `Bootstrapper` methods return `(Result, Settings,
  error)` in all three tasks; the API handlers share one `storeBootstrap`
  helper (Update + `"stored": true`); the web handler calls the same
  methods and `Service.Update`.
- Review Focus: 1 and 2 in Task 3 tests, 3 in Task 4 and on the live lab
  (Task 6), 4 in Task 1 (`TestServeSetupModeWithEmptyStore`,
  `TestPutDraftThenApplyRefusedUntilComplete`), 5 in Task 2 and Task 5
  (bodies and logs scanned for the secrets).
- Proportion: the plan pins representations and messages because the lab
  scripts are the measured source; bodies of the Go functions are left to
  the implementer.
