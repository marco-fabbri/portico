# Milestone 2d: Settings and Secrets at Rest Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every setting an MSP decides after installation lives in the
store, its secrets sealed with a master key that never leaves the
appliance; the admin sets them from setup pages with connectivity checks,
applies them with a controlled restart, and the lab runs from the store
with no secret file referenced by the YAML.

**Architecture:** `internal/secrets` owns the master key file and an
AES-256-GCM sealer. `internal/settings` is the typed runtime settings
document (hosts, Prism Central, Keycloak, OIDC, tenants) stored as JSON
under one key with its secret fields sealed; it overlays a loaded
`config.Config`, which keeps the process settings (listeners, TLS, store
and key paths, log). `portico settings import` copies a YAML plus its
secret files into the store once. `serve` reads the store first and the
YAML second, and can be asked to stop cleanly for a restart. The API gets
`GET/PUT /api/v1/settings`, `POST .../check` (connectivity) and
`POST .../apply`; the UI gets `/setup` pages over the same service. The
bootstrap paths are Milestone 2e.

**Tech Stack:** Go 1.27 standard library (`crypto/aes`, `crypto/cipher`,
`crypto/rand`), existing packages. Lab: NTNX-01, VM `portico-lab`.

**Spec:** `docs/superpowers/specs/2026-10-17-portico-design.md` sections
4.5 (pre-created path), 4.6 (secrets at rest), 4.7 (settings file and
store, apply as a restart), 5, milestone 2d in section 9; ADR 0003.

## Global Constraints

- All text on disk in English. Commit messages in English.
- Branch `m2d-settings` from `main` (already created, holds the design
  update); pull request merged by the executor.
- The master key never leaves the appliance: never logged, never in the
  API, never in a page, never in a backup instruction that copies it next
  to the store without saying so. Mode 0600, owned by the service user.
- Secrets are sealed one by one with a fresh nonce; a sealed value is
  `v1:<base64>`; a value that does not open stops `serve` with an error
  naming the field (never a silent default, never plain text).
- The API never returns a secret value; it reports whether one is set.
  The UI never prefills a secret input.
- Store settings win over the YAML; the YAML's runtime sections stay
  valid for installations without store settings (Milestone 1 to 2c
  behaviour). `config.Validate` splits into process checks (always) and
  runtime checks (after the overlay).
- Apply is a restart: `serve` returns `ErrRestart`, `main` exits 0, the
  unit has `Restart=always`. A running tenant operation is waited for
  (bounded by `TimeoutStopSec`), as today.
- `CGO_ENABLED=0`; `go vet ./...`, `gofmt -l cmd internal`,
  `go test ./...` clean at every commit; `lab/test_scripts.sh` green.
- Lab: Prism Central `https://10.23.210.175:9440`, VM `10.23.210.190`,
  secrets in `~/portico-secrets/`, the YAML of Milestone 2c in
  `lab/portico/portico.yaml` with its three secret files on the VM.

## Review Focus

Failure modes the spec implies but a single task's tests might miss; each
has a test in the task named:

1. The store holds settings sealed with another key (restored backup,
   replaced key file): expected `serve` refuses to start with an error
   naming the field, never starts with defaults or plain text (Task 3).
2. `GET /api/v1/settings` and the setup pages after a secret was set:
   expected `api_key_set: true` and an empty password input, never the
   value; the log of a PUT never contains it (Task 4).
3. `portico settings import` on a store that already holds settings:
   expected a refusal naming the command's `-force` flag, no change
   (Task 3).
4. A connectivity check with a wrong API key or an unreachable Prism
   Central: expected 200 with `ok: false` and a readable reason, and no change to
   the stored settings (Task 4).
5. Apply while a tenant is provisioning: expected a 409 naming the
   running operations (the restart closes the listeners before waiting
   for them, so it must not start), and after the operation Apply brings
   the health endpoint back within a minute (Task 5, measured in Task 6;
   wording corrected after the review: `TimeoutStopSec` does not bound a
   voluntary exit).

---

### Task 0: Design and plan already recorded; master key and sealer

**Files:**
- Create: `internal/secrets/secrets.go`, `internal/secrets/secrets_test.go`

**Interfaces:**
- Produces:

```go
package secrets

const KeySize = 32
const prefix = "v1:"

// LoadOrCreateKey reads the key file or creates it (32 random bytes,
// 0600). It refuses a file readable by group or others, or of a wrong
// size, with an error naming the path.
func LoadOrCreateKey(path string) ([]byte, error)

type Sealer struct{ /* aead */ }
func NewSealer(key []byte) (*Sealer, error)              // ErrKeySize when len != 32
func (s *Sealer) Seal(plaintext string) (string, error)  // "v1:" + base64(nonce || ciphertext), fresh nonce each call
func (s *Sealer) Open(sealed string) (string, error)     // ErrNotSealed when the prefix is missing; ErrOpen (wraps the AEAD error) when the key is wrong or the data tampered
func IsSealed(v string) bool
```

- [ ] **Step 1: Write the failing tests**

```go
func TestLoadOrCreateKeyCreates0600(t *testing.T)          // missing file → created, 32 bytes, mode 0600; second call returns the same bytes
func TestLoadOrCreateKeyRefusesWideMode(t *testing.T)      // 0644 file → error naming the path and "mode"
func TestLoadOrCreateKeyRefusesWrongSize(t *testing.T)     // 16-byte file → error
func TestSealOpenRoundTripAndFreshNonce(t *testing.T)      // Seal twice → different strings, both Open to the plaintext; prefix "v1:"
func TestOpenWrongKeyAndTamper(t *testing.T)               // other key → ErrOpen; flipped byte → ErrOpen; "plain" → ErrNotSealed
func TestSealedNeverContainsPlaintext(t *testing.T)        // Seal("hunter2-secret") does not contain the plaintext
```

- [ ] **Step 2: Run, implement, run**

Run: `go test ./internal/secrets/`
Expected: FAIL to compile, then PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/secrets
git commit -m "feat: master key file and AES-GCM sealer"
```

---

### Task 1: Settings document in the store

**Files:**
- Create: `internal/settings/settings.go` (types, JSON codec with sealed
  fields, Validate via config), `internal/settings/service.go`,
  `internal/settings/settings_test.go`
- Modify: `internal/config/config.go` (`ValidateProcess`, `ValidateRuntime`;
  `Validate` = both), `internal/config/config_test.go`,
  `cmd/portico/main.go` (nothing yet; Task 3)

**Interfaces:**
- Produces:

```go
package settings

const SettingKey = "settings"

type Secret string   // sealed in the store, never serialised by the API

type Settings struct {
    PrismHost, IDHost string
    PrismCentral struct {
        URL         string
        TLSVerify   bool
        CAPEM       string   // optional CA certificate in PEM; replaces config.PrismCentral.CAFile
        APIKey      Secret
        SAMLIdPName string
    }
    Keycloak struct {
        Realm             string
        AdminClientID     string
        AdminClientSecret Secret
    }
    OIDC struct {
        Issuer       string
        ClientID     string
        ClientSecret Secret
    }
    Tenants struct {
        ProjectPrefix, GroupPrefix, Role string
        ClusterExtID, StorageContainerExtID string
        OperationTimeout time.Duration
    }
}

// FromConfig builds the document from a loaded YAML config and the
// contents of its secret files (api key, admin secret, client secret, CA).
func FromConfig(cfg *config.Config, apiKey, adminSecret, clientSecret, caPEM string) Settings
// Overlay writes the document onto cfg's runtime sections and returns
// the three secrets and the CA PEM for the callers that need them.
func (s Settings) Overlay(cfg *config.Config) (Runtime, error)
type Runtime struct{ APIKey, AdminSecret, ClientSecret, CAPEM string }
func (s Settings) Validate() error   // builds a config.Default(), overlays, ValidateRuntime

type Service struct{ /* store, sealer */ }
func NewService(st store.Store, sealer *secrets.Sealer) *Service
func (s *Service) Stored(ctx context.Context) bool
func (s *Service) Get(ctx context.Context) (Settings, error)   // ErrNotStored; *secrets.OpenError when a field does not open (names the field)
func (s *Service) Set(ctx context.Context, in Settings) error   // Validate, seal every non-empty Secret, store
// Update merges: empty Secret fields keep the stored value, so a PUT
// without the password does not erase it.
func (s *Service) Update(ctx context.Context, in Settings) error
```

JSON on disk: the struct with `json` tags; `Secret` marshals as its
sealed string (`v1:…`) and unmarshals back; the service seals before
writing and opens after reading. `config.ValidateRuntime` holds the
checks that need the runtime sections (hosts, Prism Central, Keycloak
realm and client, OIDC, tenants placement, prefixes);
`ValidateProcess` keeps listeners, TLS pairing, session durations, store
path, key path, theme fields and the loopback/TLS rule.

- [ ] **Step 1: Write the failing config tests**

```go
func TestValidateProcessOnlyChecksProcessFields(t *testing.T)   // a Config with only process fields passes ValidateProcess and fails ValidateRuntime naming prism_central.url etc.
func TestValidateIsBoth(t *testing.T)
```

- [ ] **Step 2: Run, split `Validate`, run**

Run: `go test ./internal/config/`
Expected: FAIL, then PASS (every existing test still green).

- [ ] **Step 3: Write the failing settings tests**

```go
func TestFromConfigOverlayRoundTrip(t *testing.T)            // minimal.yaml + secrets → FromConfig → Overlay onto Default() → the runtime fields equal; Runtime carries the secrets
func TestServiceSetSealsSecrets(t *testing.T)                // Set then read the raw store value: contains "v1:" for the three secrets and never the plaintext; Get returns the plaintext
func TestServiceGetNotStored(t *testing.T)                   // empty store → ErrNotStored
func TestServiceGetWrongKeyNamesField(t *testing.T)          // Set with sealer A; Service with sealer B → Get error mentions "prism_central.api_key" (Review Focus 1)
func TestServiceUpdateKeepsSecretsWhenEmpty(t *testing.T)    // Set with key "k1"; Update with APIKey "" and a new URL → Get has "k1" and the new URL
func TestSetValidates(t *testing.T)                           // bad prefix → error names tenants.project_prefix, store unchanged
```

- [ ] **Step 4: Run, implement, run**

Run: `go test ./internal/settings/`
Expected: FAIL to compile, then PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/settings internal/config
git commit -m "feat: settings document in the store with sealed secrets"
```

---

### Task 2: `config.MasterKeyFile`, `serve` reads the store first

**Files:**
- Modify: `internal/config/config.go` (`Store.MasterKeyFile`, default
  `/etc/portico/master.key`, process check), `internal/config/config_test.go`
- Modify: `cmd/portico/serve.go`, `cmd/portico/serve_test.go`
- Modify: `deploy/portico.yaml.example`, `lab/portico/portico.yaml`,
  `lab/portico/deploy.sh` (the key file is created by the service; the
  deploy makes `/etc/portico` writable for it? No: `/etc/portico` is
  root:portico 0750 and the service cannot create files there. The key
  lives in `/var/lib/portico/master.key` by default instead; the design's
  `/etc/portico/master.key` is amended in Task 6's docs. Ruling for the
  executor: default `/var/lib/portico/master.key`.)

**Interfaces:**
- Produces: in `serve`, after the store opens: `key := secrets.LoadOrCreateKey(cfg.Store.MasterKeyFile)`,
  `sealer`, `settings.NewService(st, sealer)`; if `Stored` → `Get`
  (an error stops the start), `Overlay(cfg)`, `log.Info("settings
  source", "source", "store")`, else `"file"` and the secret files are
  read as today. `cfg.ValidateRuntime()` runs after the overlay in both
  cases. The `Runtime` secrets replace the three `readSecretFile` calls.

- [ ] **Step 1: Write the failing serve tests**

```go
func TestServeStartsFromStoreSettings(t *testing.T)   // YAML without runtime sections + a store seeded through settings.Service with sealer from the key file → runServe starts, admin healthz 200, log says source=store
func TestServeRefusesUnopenableSettings(t *testing.T) // store seeded with another key → runServe returns an error naming prism_central.api_key within 2 s, no listener bound (Review Focus 1)
func TestServeStillStartsFromFile(t *testing.T)       // the existing TestRunServeStartsAndStops keeps passing with secret files (source=file)
```

- [ ] **Step 2: Run, implement, run**

Run: `go test ./cmd/portico/ && go test ./...`
Expected: FAIL, then PASS.

- [ ] **Step 3: Settings files and commit**

`deploy/portico.yaml.example`: `store.master_key_file` with the
backup sentence; the runtime sections commented as "used only until
`portico settings import` or the setup pages have stored them".
`lab/portico/portico.yaml`: add `master_key_file: /var/lib/portico/master.key`.

```bash
git add internal/config cmd/portico deploy lab/portico
git commit -m "feat: serve reads the settings from the store, master key file"
```

---

### Task 3: `portico settings import` and `settings show`

**Files:**
- Modify: `cmd/portico/main.go`, `cmd/portico/main_test.go`

**Interfaces:**
- Produces:

```
portico settings import -config PATH [-force]
  Reads the YAML's runtime sections and its secret files, seals the secrets
  with the master key and stores the document. Refuses when the store already
  holds settings unless -force. Prints the fields moved (names only) and
  "the YAML's runtime sections and secret files can now be removed".
portico settings show -config PATH
  Prints the stored settings as YAML with secrets shown as "<set>" or "<unset>".
```

- [ ] **Step 1: Write the failing tests**

```go
func TestSettingsImportMovesValuesAndSeals(t *testing.T)   // YAML with secret files → import exit 0 → store has settings; raw value lacks the secrets; show prints "<set>" and the URL; stdout never contains a secret
func TestSettingsImportRefusesOverwrite(t *testing.T)      // second import → exit 2 with "-force"; with -force → exit 0 (Review Focus 3)
func TestSettingsShowWhenNotStored(t *testing.T)           // → exit 1 "no settings stored"
```

- [ ] **Step 2: Run, implement (the command runs as the service user like `admin set-password`), run**

Run: `go test ./cmd/portico/`
Expected: FAIL, then PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/portico
git commit -m "feat: portico settings import and show"
```

---

### Task 4: Settings API and setup pages with connectivity checks

**Files:**
- Create: `internal/api/settings.go`, `internal/api/settings_test.go`
- Create: `internal/web/setup.go`, `internal/web/templates/setup.html`,
  `internal/web/setup_test.go`; modify `internal/web/web.go` (routes,
  nav link "Setup", `Options.Settings`, `Options.Checker`),
  `internal/web/templates/layout.html`
- Create: `internal/settings/check.go` (`Checker` with the three checks),
  `internal/settings/check_test.go`
- Modify: `cmd/portico/serve.go` (wiring), `internal/api/api.go`
  (`New` gains `*settings.Service` and `settings.Checker`)

**Interfaces:**
- Produces:

```go
package settings
// Checker runs connectivity checks with candidate settings, without
// storing anything.
type Checker struct{ /* http client factory with the candidate CA, log */ }
type CheckResult struct {
    OK       bool     `json:"ok"`
    Message  string   `json:"message"`
    Clusters []Choice `json:"clusters,omitempty"`     // from Prism Central: {ExtID, Name}
    Containers []Choice `json:"containers,omitempty"`
}
type Choice struct{ ExtID, Name string }
func (c *Checker) PrismCentral(ctx context.Context, s Settings, apiKey string) CheckResult   // nutanix.New + ListClusters + ListStorageContainers; a 401 → "the API key was refused"
func (c *Checker) Keycloak(ctx context.Context, s Settings, adminSecret string) CheckResult  // keycloak.New + GET realm (FindGroup on a random name is enough: 200/404 both prove the token)
func (c *Checker) OIDC(ctx context.Context, keycloakURL string, s Settings) CheckResult      // discovery at keycloakURL + issuer path; issuer must match
```

API, behind `RequireAdmin`:

| route | body / answer |
|---|---|
| `GET /api/v1/settings` | 200 the document with `*_set` booleans instead of secrets (`api_key_set`, `admin_client_secret_set`, `client_secret_set`), `stored: true|false`, `source: "store"|"file"` |
| `PUT /api/v1/settings` | the document; secret fields optional (empty keeps the stored value); 400 on validation naming the field; 200 the stored document; the log line lists field names only |
| `POST /api/v1/settings/check` | `{"target": "prism_central"|"keycloak"|"oidc", ...candidate settings, secret optional (stored one used when empty)}` → 200 `CheckResult`; a failed check is 200 with `ok:false` and the message (Review Focus 4) |
| `POST /api/v1/settings/apply` | 202 `{"restarting": true}` when settings are stored; 409 when nothing is stored; triggers `serve`'s restart (Task 5) |

UI: `GET /setup` shows one page with four cards (Public names, Prism
Central, Identity, Tenants) and a "Check" button per card (HTMX POST to
`/setup/check?target=…`, result rendered inline, cluster and container
pickers filled from a successful Prism Central check), a "Save" per card
(POST `/setup/save?section=…`, CSRF), and an "Apply and restart" button
(POST `/setup/apply`, CSRF) that renders a waiting page polling
`/healthz` every 2 s and linking back to `/` when it answers. Secret
inputs are empty with the placeholder "unchanged" when set. When no
settings are stored and the YAML has runtime sections, the page says
"running from the file; save to move them into the store" and prefills
from the file.

- [ ] **Step 1: Write the failing checker tests** (httptest fakes)

```go
func TestCheckPrismCentralListsClustersAndContainers(t *testing.T)
func TestCheckPrismCentralWrongKey(t *testing.T)        // 401 → ok false, message mentions the key
func TestCheckPrismCentralUnreachable(t *testing.T)     // closed port → ok false, message mentions the URL
func TestCheckKeycloakTokenAndRealm(t *testing.T)
func TestCheckOIDCIssuerMismatch(t *testing.T)
```

- [ ] **Step 2: Run, implement, run**

- [ ] **Step 3: Write the failing API tests**

```go
func TestSettingsGetHidesSecrets(t *testing.T)                 // after Set: api_key_set true, no "v1:", no plaintext (Review Focus 2)
func TestSettingsPutKeepsSecretsWhenOmitted(t *testing.T)
func TestSettingsPutValidationNamesField(t *testing.T)
func TestSettingsPutNeverLogsSecret(t *testing.T)
func TestSettingsCheckDoesNotStore(t *testing.T)               // a failing check leaves the store unchanged (Review Focus 4)
func TestSettingsApplyNeedsStoredSettings(t *testing.T)        // 409 then 202; the restart hook was called once
```

- [ ] **Step 4: Run, implement (`api.New` gains the settings service, checker and a `Restart func()`), run**

- [ ] **Step 5: Write the failing web tests**

```go
func TestSetupPagePrefillsFromFileWhenNotStored(t *testing.T)
func TestSetupSaveSectionAndSecretsStayHidden(t *testing.T)     // save Prism Central with a key → redirect; page shows placeholder "unchanged", not the key
func TestSetupCheckRendersResultAndPickers(t *testing.T)        // HTMX check with the fake → cluster options rendered
func TestSetupApplyRendersWaitingPage(t *testing.T)             // POST apply → 200 waiting page with the healthz poll; restart hook called
func TestSetupRequiresCSRF(t *testing.T)
```

- [ ] **Step 6: Run, implement, wire `serve`, run the suite**

Run: `go test ./... && go vet ./... && gofmt -l cmd internal`
Expected: PASS, clean.

- [ ] **Step 7: Commit**

```bash
git add internal cmd
git commit -m "feat: settings API and setup pages with connectivity checks"
```

---

### Task 5: Apply as a controlled restart

**Files:**
- Modify: `cmd/portico/serve.go` (`ErrRestart`, a `restart` channel the
  API's hook closes; `runServe` shuts down, waits for tenant operations,
  returns `ErrRestart`), `cmd/portico/main.go` (`ErrRestart` → exit 0 with
  the log line "restarting on request"), `cmd/portico/serve_test.go`
- Modify: `deploy/portico.service` (`Restart=always`, `RestartSec=2`)

- [ ] **Step 1: Write the failing test**

```go
func TestServeRestartsOnRequest(t *testing.T)   // runServe; call the restart hook (exposed through the API: POST /api/v1/settings/apply with Basic, after seeding store settings) → runServe returns ErrRestart within 3 s; main's run returns 0 for it
```

- [ ] **Step 2: Run, implement, run; unit change; commit**

```bash
git add cmd/portico deploy
git commit -m "feat: apply restarts the service"
```

---

### Task 6: Lab: run from the store, write-up 14, documentation

**Files:**
- Modify: `lab/portico/deploy.sh` (runs `portico settings import` once
  when the store has no settings, then installs a trimmed YAML
  `lab/portico/portico.store.yaml` without runtime sections and without
  the three secret files; keeps `lab/portico/portico.yaml` as the
  file-based variant for a fresh VM), `lab/portico/portico.store.yaml`
  (new), `lab/test_scripts.sh` (M2d checks: the store YAML names no
  secret file; deploy imports before trimming)
- Create: `docs/lab/2026-10-18-14-settings-in-the-store.md`
- Modify: `docs/lab/README.md`, `docs/lab/lab-inventory.md`, `lab/README.md`,
  `README.md` (Status), design §4.6 (key path `/var/lib/portico/master.key`),
  §9 row 2d done, `deploy/portico.yaml.example`

- [ ] **Step 1: Deploy with import**

Run: `source lab/env.sh && lab/portico/deploy.sh`
Expected: `settings imported` once, `portico ready`, journal says
`settings source=store`, `/etc/portico/portico.yaml` on the VM has no
`api_key_file`, `admin_client_secret_file`, `client_secret_file`; the
three secret files are removed from `/etc/portico/` by the deploy;
`lab/auth/probe-local-login.sh` PASS; `lab/tenants/api.sh GET /api/v1/settings`
shows `source: store` and `api_key_set: true`.

- [ ] **Step 2: Browser: setup page**

`/setup` as admin: cards prefilled from the store, secrets as
"unchanged"; "Check" on Prism Central lists the cluster and the
container; change `tenants.operation_timeout` to `12m`, Save; "Apply and
restart": the waiting page, Portico back within a minute (journal:
"restarting on request", a new "listening" line); `GET /api/v1/settings`
shows `12m`. Then the Milestone 2a acceptance still passes:
`lab/tenants/acceptance.sh && lab/tenants/acceptance.sh --delete`.

- [ ] **Step 3: Wrong key test**

On the VM: stop Portico, move `/var/lib/portico/master.key` aside, start
Portico: the journal shows the error naming `prism_central.api_key`,
the service stays down (`Restart=always` retries, each attempt fails
the same way); put the key back, start, healthy. Record the lines.

- [ ] **Step 4: Write-up 14, docs, commit**

Run: `go test ./... && go vet ./... && gofmt -l cmd internal && lab/test_scripts.sh && source lab/env.sh && lab/auth/probe-local-login.sh`
Expected: all green.

```bash
git add lab docs README.md deploy
git commit -m "lab: settings in the store, master key, apply with restart (write-up 14)"
```

---

## Self-review notes

- Spec coverage: §4.6 encryption at rest, master key at first boot,
  0600, service-user owned → Task 0 and 2 (path amended to
  `/var/lib/portico`, recorded in Task 6); backup = store + key →
  example YAML sentence. §4.7 file/store precedence, import, apply as
  restart → Tasks 2, 3, 5; setup pages over the API → Task 4. §4.5
  pre-created path (paste the key) → Task 4; bootstrap → 2e. §5 "never
  stores a human administrator's credentials" → nothing in 2d takes one.
- Interfaces: `settings.Service` consumed by `serve`, the API, the UI and
  the import command; `secrets.Sealer` by the service only;
  `config.ValidateProcess/ValidateRuntime` by `serve` and `settings`.
- Review Focus 1–5 each name their task's test.
- Proportion: signatures, routes and test names; no bodies.
