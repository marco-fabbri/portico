# Milestone 2f: Minor Sweep Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the deferred minors of Milestones 2d and 2e that an
administrator or a tenant meets (password policy, login restart, theme
logo, URL with credentials, setup page details, error classification,
service restart limit) and record the older ones in a backlog file.

**Architecture:** Small, independent changes in the packages that own
each finding: a typed status error in the Keycloak client replaces
substring matching in the bootstrap and the checker; the Prism Central
bootstrap result hides a revoked key; the settings service types its
validation errors so the API can tell a bad document (400) from a broken
store (500); the setup templates keep typed fields, show empty pickers
honestly and stop password managers; the Keycloak theme draws the logo as
a block above the title; the OIDC callback restarts the login once when
its state cookie is gone; the systemd unit never gives up restarting.
Every task is one commit with its own tests.

**Tech Stack:** Go 1.27 standard library, existing packages, HTMX 2.0.4
vendored. Lab: NTNX-01, VM `portico-lab`, Chrome on the Mac for the
three visual checks (Marco).

**Spec:** `docs/superpowers/specs/2026-10-17-portico-design.md` sections
4.3 (public path flow), 4.4 (tenant users, MFA available), 4.5 (service
account), 4.7 (setup page), 5; write-ups 14 and 15 (the deferred
minors); the ledger lines quoted in each task.

## Global Constraints

- All text on disk in English. Commit messages in English, ending with
  `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.
- Branch `m2f-minor-sweep` from `main` (tag `m2e-bootstrap`); pull
  request merged by the executor at the end, tag `m2f-minor-sweep`.
- Never log, print or return a password, API key, client secret, token,
  session id or CSRF token; Keycloak and Prism Central error bodies are
  not logged (they may echo request fields).
- `go test ./...`, `go vet ./...`, `gofmt -l cmd internal` clean, and
  `lab/test_scripts.sh` PASS before every commit that touches them.
- Lab secrets stay in `~/portico-secrets`; browser logins that need a
  password are Marco's (the auto-mode classifier denies reading the
  files).
- Adoption of a realm Portico did not create stays step 2: the password
  policy is set on the realm the bootstrap creates, never on an existing
  one.

## Review Focus

1. A realm created by the bootstrap must refuse `nutanix4u` and accept
   the 20-character temporary passwords `tenant.CreateUser` generates
   (upper, lower, digits): Task 2 asserts the policy string and that the
   generator's alphabet and length satisfy it.
2. A browser whose cookies are blocked must not loop between `/`,
   Keycloak and the callback: Task 8 asserts the second missing-state
   callback answers 400, not another redirect.
3. A Prism Central URL typed as `https://admin:pw@10.0.0.10:9440` must be
   refused before any call and never stored or logged: Task 3 asserts the
   precondition and the format check.
4. A store that cannot be written while an administrator saves a card
   must answer 500, not 400 with the SQLite text as if the input were
   wrong: Task 5 asserts it.
5. A failed bootstrap must give the administrator back the non-secret
   fields they typed and never the password: Task 6 asserts both.

---

### Task 1: Typed Keycloak status errors

Ledger: "Keycloak error classification by substring on 'token: status';
a typed status error would also name a 2d-era admin client lacking
manage-clients/view-clients" and "realm typo reported as 'refused the
admin client secret' (token 404 matches the 'token' substring before
ErrNotFound); check the status instead".

**Files:**
- Modify: `internal/idp/keycloak/client.go:88-116` (token), `:176-183` (doOnce)
- Modify: `internal/settings/bootstrap_keycloak.go:64-74`
- Modify: `internal/settings/check.go:138-147`
- Test: `internal/idp/keycloak/client_test.go`, `internal/settings/check_test.go`, `internal/settings/bootstrap_keycloak_test.go`

**Interfaces:**
- Produces: `type StatusError struct { Op string; Status int }` in
  package `keycloak`, `Op` is `"token"` for the token endpoint and
  `"<METHOD> <path>"` for an admin call; `Error()` keeps today's texts
  (`keycloak: token: status 401`, `keycloak: GET /x: status 403`).
  `ErrNotFound` is still returned for a 404 on an admin call.

- [ ] **Step 1: Write the failing tests**

```go
// client_test.go
func TestTokenRefusalIsAStatusError(t *testing.T) {
	// fake token endpoint answering 401
	_, err := c.Realm(ctx)
	var se *StatusError
	if !errors.As(err, &se) || se.Op != "token" || se.Status != 401 { t.Fatalf("%v", err) }
}
// check_test.go
func TestCheckKeycloakRealmTypoNamesTheRealm(t *testing.T) {
	// fake: token endpoint answers 404 for realm "portico-typo", 200 otherwise
	s := sample(); s.Keycloak.Realm = "portico-typo"
	res := NewChecker(nil).Keycloak(ctx, srv.URL, s, "good-secret")
	if res.OK || !strings.Contains(res.Message, `realm "portico-typo" not found`) { t.Fatalf("%+v", res) }
}
// bootstrap_keycloak_test.go
func TestKeycloakBootstrapNamesTheTokenStatus(t *testing.T) {
	// fake master token endpoint answers 503
	res, _, _ := NewBootstrapper(nil, nil).Keycloak(ctx, srv.URL, Defaults(), KeycloakInput{AdminUsername: "kcadmin", AdminPassword: "x", Realm: "portico"})
	if res.OK || res.Message != "Keycloak answered status 503 at the token endpoint" { t.Fatalf("%+v", res) }
}
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/idp/keycloak/ ./internal/settings/ -run 'TestTokenRefusalIsAStatusError|TestCheckKeycloakRealmTypoNamesTheRealm|TestKeycloakBootstrapNamesTheTokenStatus' -v`
Expected: FAIL (StatusError undefined; message "refused the admin client secret"; message "refused the administrator credentials").

- [ ] **Step 3: Implement**

`client.go`: `token()` returns `&StatusError{Op: "token", Status: resp.StatusCode}` on a non-200; `doOnce` returns `&StatusError{Op: method + " " + path, Status: resp.StatusCode}` on a non-2xx other than 404.
`bootstrap_keycloak.go` `remote`: `errors.As(err, &se)` and `se.Op == "token"`: 401 → "Keycloak refused the administrator credentials", any other status → `fmt.Sprintf("Keycloak answered status %d at the token endpoint", se.Status)`; a non-status error on the token call (`strings.HasPrefix(err.Error(), "keycloak: token:")` is replaced by `errors.As` failing and `errClass`) → "Keycloak did not answer: " + errClass(err); other `StatusError` → "Keycloak answered an error: " + err text without the `keycloak: ` prefix.
`check.go`: token 404 → `realm %q not found in Keycloak`; token 401 → the current "refused the admin client secret" text; other token status → `Keycloak answered status %d at the token endpoint`; `ErrNotFound` on the realm call → the current "not found" text.

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/idp/keycloak/ ./internal/settings/`
Expected: PASS (the three new tests and the existing refusal tests).

- [ ] **Step 5: Commit**

```bash
git add internal/idp/keycloak internal/settings
git commit -m "keycloak: typed status errors; the checker names a mistyped realm"
```

---

### Task 2: Password policy on the realm the bootstrap creates

Ledger: "the Keycloak bootstrap creates the realm with no password
policy: a tenant user can set a weak password at first login".

**Files:**
- Modify: `internal/settings/bootstrap_keycloak.go:85`
- Modify: `internal/tenant/slug.go:24-40` (no change expected; the test pins it)
- Test: `internal/settings/bootstrap_keycloak_test.go`, `internal/tenant/slug_test.go`
- Modify: `docs/lab/lab-inventory.md` (realm row: policy set by hand on the lab realm), `docs/superpowers/specs/2026-10-17-portico-design.md` §4.4 (one sentence: the bootstrap sets the policy below; MFA is a realm setting the MSP turns on in Keycloak).

**Interfaces:**
- Produces: `const RealmPasswordPolicy = "length(12) and notUsername and notEmail"` in package `settings` (exported so the `tenant` test can pin the generator against it).

- [ ] **Step 1: Write the failing tests**

```go
// bootstrap_keycloak_test.go: the fake keeps the realm body it received (field realmBody map[string]any)
func TestKeycloakBootstrapSetsAPasswordPolicy(t *testing.T) {
	// run the create path as TestKeycloakBootstrapCreatesRealmAndClients does
	if f.realmBody["passwordPolicy"] != "length(12) and notUsername and notEmail" { t.Fatalf("realm body %v", f.realmBody) }
}
// slug_test.go
func TestTemporaryPasswordSatisfiesTheRealmPolicy(t *testing.T) {
	for i := 0; i < 50; i++ {
		p := temporaryPassword() // the generator in slug.go
		if len(p) < 12 || strings.ContainsAny(p, "@") || strings.Contains(strings.ToLower(p), "carol") { t.Fatal(p) }
	}
	if settings.RealmPasswordPolicy != "length(12) and notUsername and notEmail" { t.Fatal("policy changed: re-check the generator") }
}
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/settings/ ./internal/tenant/ -run 'TestKeycloakBootstrapSetsAPasswordPolicy|TestTemporaryPasswordSatisfiesTheRealmPolicy' -v`
Expected: FAIL (`passwordPolicy` missing; `RealmPasswordPolicy` undefined).

- [ ] **Step 3: Implement**

Add `"passwordPolicy": RealmPasswordPolicy` to the realm representation on the create path only; export the constant. If `tenant` importing `settings` creates an import cycle, copy the string into the tenant test instead and say so in the ledger.

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/settings/ ./internal/tenant/`
Expected: PASS.

- [ ] **Step 5: Lab: set the policy on the existing lab realm**

The lab realm was created by the 2e bootstrap before this task. Run, from the repo root:

```bash
source lab/env.sh
printf '{"passwordPolicy":"length(12) and notUsername and notEmail"}' > "$TMPDIR/policy.json"
lab/saml/kc.sh PUT /portico-lab "$TMPDIR/policy.json"
lab/saml/kc.sh GET /portico-lab | jq -r .passwordPolicy
```

Expected: `HTTP 204` then `length(12) and notUsername and notEmail`. Record it in `docs/lab/lab-inventory.md` (realm row).

- [ ] **Step 6: Commit**

```bash
git add internal/settings internal/tenant docs/lab/lab-inventory.md docs/superpowers/specs/2026-10-17-portico-design.md
git commit -m "bootstrap: password policy on the realm Portico creates"
```

---

### Task 3: Prism Central bootstrap result and URL hygiene

Ledger: "failure result still lists 'api key …' in created/key_name
after the revocation", "key_expires renders the zero time on non-OK
results (use omitzero)", "a Prism Central URL with userinfo
(https://user:pw@host) would be stored and logged verbatim; refuse
u.User != nil in ValidateFormat and the bootstrap precondition",
"TestPrismBootstrapPolicyMissingDeletesTheKey does not assert
out.PrismCentral.APIKey == \"\"".

**Files:**
- Modify: `internal/settings/bootstrap_prism.go:38` (tag), `:63-66` (precondition), `:139-143` (`failed`)
- Modify: `internal/settings/settings.go:170-177` (`ValidateFormat`)
- Test: `internal/settings/bootstrap_prism_test.go`, `internal/settings/settings_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// bootstrap_prism_test.go: extend TestPrismBootstrapPolicyMissingDeletesTheKey
	if out.PrismCentral.APIKey != "" || res.KeyName != "" || !res.KeyExpires.IsZero() { t.Fatalf("revoked key still in the result: %+v", res) }
	for _, c := range res.Created { if strings.HasPrefix(c, "api key") { t.Fatalf("created lists the revoked key: %v", res.Created) } }
	js, _ := json.Marshal(res)
	if strings.Contains(string(js), "key_expires") { t.Fatalf("zero key_expires rendered: %s", js) }

func TestPrismBootstrapRefusesURLWithCredentials(t *testing.T) {
	_, _, err := NewBootstrapper(nil, nil).PrismCentral(ctx, Defaults(), PrismInput{Username: "admin", Password: "x", URL: "https://admin:pw@10.0.0.10:9440"})
	var se *StateError
	if !errors.As(err, &se) || !strings.Contains(se.Msg, "must not carry credentials") || strings.Contains(se.Msg, "pw") { t.Fatalf("%v", err) }
}
// settings_test.go
func TestValidateFormatRefusesUserinfoURL(t *testing.T) {
	s := Defaults(); s.PrismCentral.URL = "https://admin:pw@10.0.0.10:9440"
	err := s.ValidateFormat()
	if err == nil || !strings.Contains(err.Error(), "prism_central.url must not carry credentials") || strings.Contains(err.Error(), "pw") { t.Fatalf("%v", err) }
}
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/settings/ -run 'TestPrismBootstrapPolicyMissingDeletesTheKey|TestPrismBootstrapRefusesURLWithCredentials|TestValidateFormatRefusesUserinfoURL' -v`
Expected: FAIL on each assertion named above.

- [ ] **Step 3: Implement**

`KeyExpires time.Time \`json:"key_expires,omitzero"\``. In `failed`, after `revoke()`: `res.KeyName, res.KeyExpires = "", time.Time{}` and drop every `Created` entry starting with `"api key"`. Precondition: `u.User != nil` → `&StateError{Msg: "the Prism Central URL must not carry credentials"}` (the URL is not echoed). `ValidateFormat`: `u.User != nil` → `settings: %s must not carry credentials` with the key only.

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/settings/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/settings
git commit -m "settings: hide a revoked key in the bootstrap result, refuse URLs with credentials"
```

---

### Task 4: SAML bootstrap logs the effective identity-provider name

Ledger: "SAML API log line now logs in.Name; the defaulted name is not
logged when in.Name is empty".

**Files:**
- Modify: `internal/settings/bootstrap_saml.go:28-38` (`SAMLResult`), the function body where the default name is chosen
- Modify: `internal/api/settings.go:487`
- Test: `internal/settings/bootstrap_saml_test.go`, `internal/api/bootstrap_test.go`

**Interfaces:**
- Produces: `SAMLResult.Name string \`json:"name"\``, the name used (input or default).

- [ ] **Step 1: Write the failing tests**

```go
// bootstrap_saml_test.go: in the success test with an empty input name
	if res.Name != "portico-keycloak" { t.Fatalf("name %q", res.Name) }
// api/bootstrap_test.go: extend TestBootstrapSAMLStoresIdPName, posting {} without a name and reading the log buffer the test already captures
	if !strings.Contains(logs.String(), "name=portico-keycloak") { t.Fatalf("log: %s", logs.String()) }
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/settings/ ./internal/api/ -run 'SAML' -v`
Expected: FAIL (`res.Name` undefined; log has `name=`).

- [ ] **Step 3: Implement**

Set `res.Name` where the default is applied; the handler logs `"name", res.Name`.

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/settings/ ./internal/api/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/settings internal/api
git commit -m "bootstrap saml: result carries the identity-provider name used"
```

---

### Task 5: Store failures on a settings save answer 500

Ledger (2d): "every non-OpenError Update failure answers 400, including
store I/O errors (should be 500)" and the 2d test gaps "Get on a store
holding a plaintext secret; import -force refusal asserting the stored
document unchanged".

**Files:**
- Modify: `internal/settings/service.go:97-100` (`set`), new type next to `OpenError`
- Modify: `internal/api/settings.go:275-283`, and the card save path in `internal/web/setup.go:119-156` if it maps errors to a status (same rule)
- Test: `internal/api/settings_test.go`, `internal/settings/settings_test.go`, `cmd/portico/main_test.go`

**Interfaces:**
- Produces: `type ValidationError struct{ Err error }` in package `settings` with `Error()` = `Err.Error()` and `Unwrap()`; `set` wraps the `ValidateFormat` error in it.

- [ ] **Step 1: Write the failing tests**

```go
// api/settings_test.go: failingStore{store.Store} whose SetSetting returns errors.New("disk full") (same shape as the one in internal/settings/bootstrap_prism_test.go); build the env on it
func TestSettingsPutStoreFailureIs500(t *testing.T) {
	rec := e.do("PUT", "/api/v1/settings", validBody, ...)
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "disk full") { t.Fatalf("%d %s", rec.Code, rec.Body.String()) }
}
// settings_test.go
func TestServiceGetNamesAPlaintextSecret(t *testing.T) {
	// store a document by hand with "prism_central":{"api_key":"plain-text"} (not sealed)
	_, err := svc.Get(ctx)
	var oe *OpenError
	if !errors.As(err, &oe) || oe.Field != "prism_central.api_key" { t.Fatalf("%v", err) }
}
// cmd/portico/main_test.go: extend TestSettingsImportRefusesOverwrite
	// after the refused second import (file edited to another prism_central.url), settings show must still print the first URL
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/api/ ./internal/settings/ ./cmd/portico/ -run 'TestSettingsPutStoreFailureIs500|TestServiceGetNamesAPlaintextSecret|TestSettingsImportRefusesOverwrite' -v`
Expected: `TestSettingsPutStoreFailureIs500` FAIL with 400; the other two may already pass (they close test gaps): if one passes before any change, keep it and note it in the ledger as a gap closed, not a fix.

- [ ] **Step 3: Implement**

`set` returns `&ValidationError{Err: err}`. API Update: `errors.As(err, &ve)` → 400; everything else → log "settings not stored" and 500 (the `OpenError` branch merges into this one). Apply the same two-way mapping in the web card save if it has one.

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/api/ ./internal/settings/ ./internal/web/ ./cmd/portico/`
Expected: PASS; `TestSettingsPutValidationNamesField` still 400.

- [ ] **Step 5: Commit**

```bash
git add internal/settings internal/api internal/web cmd/portico
git commit -m "settings: typed validation errors; a store failure on save is a 500"
```

---

### Task 6: Setup page: typed fields, empty pickers, password managers

Ledger: "a failed bootstrap fragment resets the typed non-secret fields
(username, service_account, display_name) to defaults"; (2d) "OOB
pickers drop `required` and render empty on zero clusters/containers;
keep required and show a disabled 'none found' option"; (2d) "secret
inputs use autocomplete=\"off\"; \"new-password\" is what stops password
managers".

**Files:**
- Modify: `internal/web/setup.go:224-230` (`bootData`), `:235-300`
- Modify: `internal/web/templates/setup.html:105-176`
- Test: `internal/web/bootstrap_test.go`, `internal/web/setup_test.go`

**Interfaces:**
- Produces: `bootData.Form map[string]string` with the non-secret fields of the request (`username`, `service_account`, `admin_username`, `display_name`), filled on every outcome; the templates render `value="{{index .Form "username"}}"` and keep `value=""` on the password inputs. A `pickers` template used by both the `bootstrap_prism_central` and `check` fragments renders `<select … required>`; with zero choices it renders `<option value="" disabled selected>none found</option>`.

- [ ] **Step 1: Write the failing tests**

```go
// bootstrap_test.go
func TestSetupBootstrapFailureKeepsTypedFields(t *testing.T) {
	// wrong password against the bootstrap fakes, service_account "svc-x"
	for _, want := range []string{`name="username" value="admin"`, `name="service_account" value="svc-x"`, `name="password" value=""`} { if !strings.Contains(body, want) { t.Fatalf("lacks %q: %s", want, body) } }
	if strings.Contains(body, "wrong") { t.Fatal("the password reached the page") }
}
// setup_test.go
func TestSetupCheckRendersEmptyPickers(t *testing.T) {
	// fake Prism Central answering zero clusters and containers (withSetup gets a toggle)
	if !strings.Contains(body, "none found") || !regexp.MustCompile(`<select name="cluster_ext_id"[^>]*required`).MatchString(body) { t.Fatalf("%s", body) }
}
func TestSetupSecretInputsStopPasswordManagers(t *testing.T) {
	page := e.do("GET", "/setup", nil, true).Body.String()
	if regexp.MustCompile(`type="password"[^>]*autocomplete="off"`).MatchString(page) { t.Fatal("a password input still says autocomplete=off") }
	if n := strings.Count(page, `autocomplete="new-password"`); n < 6 { t.Fatalf("%d new-password inputs", n) }
}
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/web/ -run 'TestSetupBootstrapFailureKeepsTypedFields|TestSetupCheckRendersEmptyPickers|TestSetupSecretInputsStopPasswordManagers' -v`
Expected: FAIL on each.

- [ ] **Step 3: Implement**

Fill `d.Form` from `v(...)` before the switch; templates read it. Six password inputs (`api_key`, `admin_client_secret`, `client_secret`, `admin_password`, the two `password`) get `autocomplete="new-password"`; the username inputs keep `autocomplete="off"`. Extract the two `<select>` blocks into `{{define "pickers"}}` taking `.Clusters`, `.Containers`, `.ClusterExtID`, `.StorageContainer`, with `required` and the disabled option.

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/web/`
Expected: PASS (including `TestSetupCheckRendersResultAndPickers` and the bootstrap fragment tests).

- [ ] **Step 5: Commit**

```bash
git add internal/web
git commit -m "setup page: keep typed fields on failure, honest empty pickers, new-password inputs"
```

---

### Task 7: Keycloak theme: logo above the title

Ledger: "Keycloak login page: the realm display name 'Portico Lab MSP'
is drawn across the logo (Marco, Chrome 2026-10-18)". Cause: the logo is
a background of `#kc-header-wrapper` with a top padding that
keycloak.v2's own header rules override.

**Files:**
- Modify: `internal/branding/theme.go:74-80`
- Test: `internal/branding/theme_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestThemeCSSDrawsTheLogoAboveTheTitle(t *testing.T) {
	css := themeCSS(Branding{AccentColor: "#8b1f5f", LogoType: "image/svg+xml"}) // whatever makes HasLogo() true
	for _, want := range []string{"#kc-header-wrapper::before", "content: \"\"", "display: block", "url(../img/logo.svg)"} { if !strings.Contains(css, want) { t.Fatalf("css lacks %q:\n%s", want, css) } }
	if strings.Contains(css, "padding-top") { t.Fatal("the padding hack is still there") }
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/branding/ -run TestThemeCSSDrawsTheLogoAboveTheTitle -v`
Expected: FAIL (no `::before`, `padding-top` present).

- [ ] **Step 3: Implement**

Replace the `HasLogo` rule with:
`#kc-header-wrapper::before { content: ""; display: block; height: 96px; margin: 0 auto 12px; background: url(../img/logo.<ext>) no-repeat center; background-size: contain; }`
A generated block in normal flow sits above the text whatever the wrapper's padding or line-height. Keep the comment about write-up 13.

- [ ] **Step 4: Run the package tests, then redeploy and check in the browser**

Run: `go test ./internal/branding/` → PASS.
Lab: `source lab/env.sh && lab/portico/deploy.sh` (existing store: binary only), then re-save the branding so the theme files are regenerated: `curl` the branding PUT as `lab/portico/bootstrap-acceptance.sh` does (same body), expected 200. Ask Marco to open `https://prism.lab.portico.test/` (no login needed: the page itself is the check) and confirm the title sits under the logo.

- [ ] **Step 5: Commit**

```bash
git add internal/branding
git commit -m "branding: draw the Keycloak logo as a block above the title"
```

---

### Task 8: OIDC callback restarts the login once when its state is gone

Ledger: "/.portico/callback answers a bare 'login state missing or
expired, start again' (400) when the login took longer than the
10-minute state cookie; redirect to / to restart the flow instead".

**Files:**
- Modify: `internal/auth/handlers.go:140-147`, cookie helpers near `setOIDCCookie`
- Test: `internal/auth/oidc_test.go`

**Interfaces:**
- Produces: cookie `portico_restart` (value `1`, `Max-Age` 60, `Path`
  `/.portico/`, `Secure`, `HttpOnly`, `SameSite=Lax`) set with a `303`
  to `/` when the callback finds no state cookie and no restart cookie;
  with the restart cookie present the callback answers the current 400
  text (cookies blocked or a real loop) and clears the restart cookie.

- [ ] **Step 1: Write the failing test**

```go
func TestCallbackWithoutStateRestartsOnce(t *testing.T) {
	// client with no state cookie (fresh jar), CheckRedirect = ErrUseLastResponse
	resp := get("https://prism.test/.portico/callback?code=x&state=y")
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/" { t.Fatalf("first: %d %s", resp.StatusCode, resp.Header.Get("Location")) }
	// restart cookie now in the jar
	resp = get("https://prism.test/.portico/callback?code=x&state=y")
	if resp.StatusCode != 400 { t.Fatalf("second: %d", resp.StatusCode) }
	if len(st.sessions) != 0 { t.Error("a session was created") }
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/auth/ -run TestCallbackWithoutStateRestartsOnce -v`
Expected: FAIL (first answer is 400).

- [ ] **Step 3: Implement**

In `Callback`, when `readOIDCCookie` fails: if the request carries `portico_restart`, clear it and answer the 400 as today; else set it and `http.Redirect(w, r, "/", http.StatusSeeOther)` with `Cache-Control: no-store`. Log at Info `callback: login state missing, restarting`.

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/auth/`
Expected: PASS (`TestCallbackRejectsBadState`, `TestCallbackRejectsReplay` unchanged: both carry a state cookie).

- [ ] **Step 5: Commit**

```bash
git add internal/auth
git commit -m "auth: restart the login once when the callback finds no state"
```

---

### Task 9: portico.service never stops restarting

Ledger (2d): "portico.service start limit after an unopenable store
needs `systemctl reset-failed`; consider StartLimitIntervalSec=0 or a
larger burst, and say it in the unit comment".

**Files:**
- Modify: `deploy/portico.service` (`[Unit]`)
- Modify: `lab/test_scripts.sh` (new check M2f-1 after M2d-3)
- Modify: `docs/lab/lab-inventory.md` if it describes the unit's behaviour

- [ ] **Step 1: Add the check, run it, watch it fail**

`lab/test_scripts.sh`: `if grep -q '^StartLimitIntervalSec=0' deploy/portico.service; then ok "portico.service has no start limit"; else ko "portico.service still has the default start limit"; fi`
Run: `lab/test_scripts.sh` → Expected: one `ko` line, exit non-zero.

- [ ] **Step 2: Implement**

In `[Unit]`: comment "An appliance recovers by itself: a store that cannot open makes the process exit at once, and the default start limit (5 in 10 s) would leave the unit failed until `systemctl reset-failed`. With no limit it retries every RestartSec until the cause is fixed." then `StartLimitIntervalSec=0`.

- [ ] **Step 3: Run the script, deploy on the lab**

Run: `lab/test_scripts.sh` → PASS. `source lab/env.sh && lab/portico/deploy.sh` → the unit reloaded, `systemctl show portico -p StartLimitIntervalUSec` over SSH prints `StartLimitIntervalUSec=0`.

- [ ] **Step 4: Commit**

```bash
git add deploy/portico.service lab/test_scripts.sh docs/lab/lab-inventory.md
git commit -m "deploy: no start limit on portico.service"
```

---

### Task 10: Backlog file, docs, write-up, close-out

**Files:**
- Create: `docs/backlog.md`
- Create: `docs/lab/2026-10-18-16-minor-sweep.md`
- Modify: `docs/lab/README.md` (row 16), `README.md` (Status), `docs/superpowers/specs/2026-10-17-portico-design.md` §9 (row 2f: "Minor sweep after 2d/2e — done when the deferred minors listed in write-ups 14 and 15 are closed or in docs/backlog.md")

- [ ] **Step 1: Write `docs/backlog.md`**

Title "Backlog: deferred findings". One section per milestone (2a, 2b, 2c), one line per deferred minor from the final recaps of those milestones (the ledger lines quoted in the PRs #3, #6, #7), each with the file or script it concerns, no severity. Add a closing section "Closed in 2f" listing the 2d/2e items this plan closed. A line says that items move out of this file when a milestone closes them.

- [ ] **Step 2: Write the write-up**

Sections as in write-up 15: Question (which deferred minors touch a person, and what the browser shows after the fixes), Measured on (Portico version from `git describe`, Keycloak 26.3.5, pc.7.6.0.6), Method (unit tests per task; lab: policy PUT, deploy, branding re-save; Chrome: login page logo, a weak password refused at carol's first login, a callback with an expired state landing on the login again), Result (one MEASURED line per browser check, from Marco), How to re-measure.

The browser checks need tenant-b and carol: `lab/tenants/acceptance.sh` before, `--delete` after, as in write-up 15. The expired-state check: open the login page, wait eleven minutes, log in; expected: the landing page without the 400 text.

- [ ] **Step 3: Full verification**

Run: `gofmt -l cmd internal; go vet ./... && go test ./... && lab/test_scripts.sh`
Expected: no gofmt output, every package `ok`, `PASS`.

- [ ] **Step 4: Commit**

```bash
git add docs README.md
git commit -m "docs: backlog of deferred findings, write-up 16, status 2f"
```

Then the final review, the fix pass, the pull request, the tag.
