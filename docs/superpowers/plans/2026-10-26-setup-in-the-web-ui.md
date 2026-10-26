# Setup in the Web UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A new appliance is set up from the browser alone: the console
goes back to network, break-glass password and status (design 4.8); the
certificate, the self-signed trial and the Cloudflare tunnel move to the
admin UI; the Keycloak realm is created by Portico without anyone
copying credentials.

**Architecture:** The admin UI validates what it receives and writes a
request into `/var/lib/portico/exposure/`, a directory owned by the
unprivileged `portico` user. A root systemd path unit
(`portico-exposure.path`) runs `portico exposure apply`. That command
installs the pair or the token, edits the process YAML, switches
`cloudflared` and restarts Portico, but only when the mode changes. It
then writes `result.json`. This is the pattern `keycloak-hostname.path`
already uses. The public listener reloads its certificate when the file
changes, so renewing the certificate does not end the tenants' sessions.
The realm is created in the background once the public names are saved,
with the temporary Keycloak administrator of the first boot. Portico gets
it from a copy readable only by `portico`. After the bootstrap Portico
deletes that administrator in Keycloak and deletes its copy of the
credentials.

**Tech Stack:** Go 1.x standard library (`crypto/x509`, `crypto/tls`),
HTMX templates, systemd path units, bash installer.

**Spec:** `docs/superpowers/specs/2026-10-17-portico-design.md` (§4.2,
§4.6, §4.8). This plan changes §4.8 back to its first wording and records
the change in a new ADR.

## Global Constraints

- Everything on disk in English; no "lab" in anything the product shows
  (names, texts, paths).
- The public path fails closed; local Prism Central login never from
  outside; Portico never stores a human administrator's credentials; the
  master key never leaves the appliance.
- Secrets (private key, tunnel token, Keycloak passwords) are never
  logged, echoed back to the page, put in a URL or kept after use.
  Request files are 0600 `portico:portico`, deleted by `apply`.
- The private key is installed as `/etc/portico/tls/public.key` 0640
  `root:portico`, the certificate as `public.crt` 0644, as the installer
  does today. The internal 8443 pair is never touched by the upload.
- Uploads are limited to 64 KiB per file.
- The CI's shellcheck is old: no `A && B || C`.

## Review Focus

1. A certificate for the wrong names, expired, with the chain in the
   wrong order, with a key that does not match, an encrypted key, or a
   PKCS#8, RSA or EC key: the page says which one, nothing is installed.
2. The first boot: Keycloak is still pulling its image for minutes when
   the names are saved. The Identity card waits and creates the realm
   when Keycloak answers, it never ends in a permanent failure.
3. A restart between "secrets stored" and "temporary administrator
   deleted": the next start finishes the job, it does not lose the realm
   or ask for credentials.
4. A wrong tunnel token: `cloudflared` does not start, Portico keeps the
   exposure it had, and the page shows `apply`'s message.
5. Renewing the certificate in direct mode: the new one is served
   without a restart, and existing sessions survive.

---

### Task 1: The exposure package (validation and the root apply)

**Files:**
- Create: `internal/exposure/validate.go`, `internal/exposure/apply.go`, `internal/exposure/exposure_test.go`
- Modify: `internal/console/exposure.go` (setMode, SelfSigned and installDirect move out; the console keeps only adminPassword and the secret helpers)

**Interfaces:**
- Produces:
  - `type Info struct { Names []string; NotAfter time.Time; Fingerprint string; Issuer string; SelfSigned bool }`
  - `func Validate(certPEM, keyPEM []byte, hosts []string, now time.Time) (Info, error)`: the leaf must come first, the SANs must cover every host, the EKU must be serverAuth (or absent), the certificate must be valid at `now`, the key must match. Accepts PKCS#1, PKCS#8 and EC keys. An encrypted key is refused with "the key is encrypted: export it without a passphrase". The error text names the failing check.
  - `func SelfSigned(names []string) (certPEM, keyPEM []byte, err error)` (moved from console).
  - `type Request struct { Mode string /* "direct" | "tunnel" */ ; SelfSigned bool }`, read from `request.json`; the pair is in `public.crt`/`public.key`, the token in `token`, all in the same directory.
  - `type Result struct { OK bool; Message string; Mode string; At time.Time }`, written to `result.json`.
  - `type Host interface { ReadFile; WriteFile; Remove; Exec }` (the console's System subset), so tests use a fake.
  - `func Apply(h Host, dir, configPath string) Result`: reads the request, installs, deletes the request files, and returns (and writes) the result. Restarts `portico` only when the mode changes. In tunnel mode it starts `cloudflared` first and leaves everything as it was if that fails (the console's order).

- [ ] **Step 1: Write the failing tests** in `exposure_test.go`: `TestValidateNamesTheFailingCheck` (cases: wrong SAN → "does not name id.msp.example"; expired; key mismatch; chain reversed → "the first certificate must be the server's"; encrypted key; RSA PKCS#1, PKCS#8 and EC keys accepted), `TestApplyDirectSameModeDoesNotRestart`, `TestApplyModeChangeRestartsPortico`, `TestApplyTunnelFailureKeepsExposure`, `TestApplyDeletesRequestFiles` (token and key gone even on failure), and `TestSetModeEditsTheYAML` (the console's current setMode test, moved).
- [ ] **Step 2: Run** `go test ./internal/exposure/` → FAIL (package missing).
- [ ] **Step 3: Implement** both files. Move the console code instead of copying it. `console` imports nothing from `exposure` once Task 5 removes the menu entries.
- [ ] **Step 4: Run** `go test ./internal/exposure/ ./internal/console/` → PASS.
- [ ] **Step 5: Commit** `feat(exposure): validation of a public pair and the root apply of an exposure request`.

### Task 2: `portico exposure apply`, the path unit, the installer

**Files:**
- Modify: `cmd/portico/main.go` (subcommand `exposure apply -config PATH`, run as root, `exposure.Apply` on `/var/lib/portico/exposure`)
- Create: `deploy/portico-exposure.path` (`PathChanged=/var/lib/portico/exposure/request.json`), `deploy/portico-exposure.service` (oneshot, `ExecStart=/usr/local/bin/portico exposure apply -config /etc/portico/portico.yaml`)
- Modify: `deploy/install.sh`:
  - installs and enables the path unit;
  - creates `/var/lib/portico/exposure` 0700 `portico:portico`;
  - at the first boot, next to `keycloak.env`, writes `/var/lib/portico/keycloak-bootstrap.env` (0600 `portico:portico`, `KC_BOOTSTRAP_ADMIN_USERNAME` and `_PASSWORD` only, no database password).
- Test: `lab/test_scripts.sh`, `cmd/portico/main_test.go`

- [ ] **Step 1: Failing checks.** In `test_scripts.sh` (render-only install):
  - the path unit and the service are installed, and the path unit is enabled;
  - the exposure directory is 0700;
  - `keycloak-bootstrap.env` is 0600 and holds no `KC_DB_PASSWORD`;
  - the file is not rewritten by a second run.

  In `main_test.go`: `TestExposureApplyUsage` (exit 2 without `apply`).
- [ ] **Step 2: Run** both → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `bash lab/test_scripts.sh` and `go test ./cmd/portico/` → PASS.
- [ ] **Step 5: Commit** `feat(deploy): the exposure request is applied by a root path unit`.

### Task 3: The public certificate reloads without a restart

**Files:**
- Create: `cmd/portico/certreload.go`, `cmd/portico/certreload_test.go`
- Modify: `cmd/portico/serve.go:34-43` (the public listener uses `GetCertificate`; the internal listener stays as it is)

**Interfaces:**
- Produces: `func newCertReloader(certFile, keyFile string) (*certReloader, error)` and `func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error)`. It reloads when the certificate's mtime or size changes, checked at most once a second. If the new pair fails to load it keeps serving the old one and logs the error once (with no key material in it).

- [ ] **Step 1: Failing test** `TestCertReloaderServesTheNewPair`: start with pair A, then write pair B over the files, and after the check interval the reloader serves B. `TestCertReloaderKeepsTheOldPairOnABadWrite`.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./cmd/portico/` → PASS.
- [ ] **Step 5: Commit** `feat(serve): the public certificate reloads when its file changes`.

### Task 4: Exposure in the admin UI (setup card and the Certificate page)

**Files:**
- Modify:
  - `internal/web/setup.go`;
  - `internal/web/templates/setup.html` (the first card becomes "Public names and exposure");
  - `internal/web/templates/settings.html` (an "Exposure" section after the setup);
  - `internal/web/web.go` (routes).
- Create: `internal/web/exposure.go`, `internal/web/exposure_test.go`

**Interfaces:**
- Consumes: `exposure.Validate`, `exposure.SelfSigned`, `exposure.Request`, `exposure.Result` (Task 1).
- Produces the routes:
  - `POST /exposure` (multipart): `mode` = `certificate` | `selfsigned` | `tunnel`, the fields `cert`, `key` and `token`.
  - `GET /exposure/status` (HTMX fragment): the last `result.json`, and the current certificate's names, expiry and SHA-256 fingerprint read from `/etc/portico/tls/public.crt`.
  - Both are behind the admin login and the existing CSRF check.

- [ ] **Step 1: Failing tests:**
  - `TestExposureUploadRefusesAMismatchedPair` (400, the Validate message, nothing written);
  - `TestExposureUploadWritesARequest` (`public.crt`, `public.key` and `request.json` written 0600; `request.json` written last, since the path unit watches it);
  - `TestExposureUploadLimits` (a 65 KiB file → 413);
  - `TestExposureTunnelTokenIsNotEchoed` (the token appears in no response body and no log line);
  - `TestExposureNeedsPublicNames` (409 before the names are saved);
  - `TestExposureStatusShowsTheResult`.
- [ ] **Step 2: Run** `go test ./internal/web/` → FAIL.
- [ ] **Step 3: Implement.** The setup card shows the two name fields, then three choices:
  - "Upload a certificate" (two file inputs);
  - "Self-signed, for a first try" (a warning that browsers will refuse it);
  - "Cloudflare tunnel" (one token field, type password).

  After a submit the card polls `/exposure/status` every 2 s until a newer result arrives. A mode change restarts Portico, so the existing `/setup/ready` page takes over. On the Settings page the same form sits under the current certificate's details, with the line "Renewing the certificate keeps tenant sessions; changing the mode restarts Portico and ends them."
- [ ] **Step 4: Run** `go test ./internal/web/` → PASS.
- [ ] **Step 5: Commit** `feat(web): public certificate and tunnel from the admin UI`.

### Task 5: The realm is created by Portico

**Files:**
- Modify:
  - `internal/settings/bootstrap_keycloak.go`;
  - `internal/idp/keycloak/admin.go` (`DeleteMasterUser`);
  - `internal/web/setup.go` and `setup.html` (Identity card);
  - `cmd/portico/serve.go` (start the realm job).
- Create: `cmd/portico/realm_job.go`, `cmd/portico/realm_job_test.go`
- Test: `internal/settings/bootstrap_keycloak_test.go`

**Interfaces:**
- Produces:
  - `func (b *Bootstrapper) KeycloakAuto(ctx context.Context, keycloakURL string, s Settings, bootstrapFile string) (KeycloakResult, Settings, error)`:
    - with the temporary administrator from `bootstrapFile` when the file exists, or with the stored admin client when the realm is already set (a name change before Apply);
    - realm `portico`, display name `Portico MSP`;
    - updates the OIDC client's redirect URIs from the current names;
    - after the settings are stored, deletes the temporary administrator in the master realm, then the file.
  - `func (c *Client) DeleteMasterUser(ctx context.Context, username string) error`.
  - `type realmJob struct` with `Start(ctx)`, `Kick()` and `State() (phase string, msg string)`. The phases are `waiting-names`, `waiting-keycloak`, `creating`, `ready` and `failed`. A failure is retried every 30 s and by the card's Retry button.
- The job starts at serve start in setup mode. The setup save of the names calls `Kick()`. The Identity card renders `State()` and polls it every 3 s. The typed fields for an existing realm stay, folded under "Use an existing realm".

- [ ] **Step 1: Failing tests:**
  - `TestKeycloakAutoCreatesTheRealmAndDeletesTheTemporaryAdmin` (fake Keycloak: realm and clients created, master user deleted after the store, file removed);
  - `TestKeycloakAutoAfterARestartBeforeTheDelete` (settings already stored, file still there: it finishes the delete);
  - `TestKeycloakAutoNameChangeUsesTheAdminClient` (no file; redirect URI updated);
  - `TestRealmJobWaitsForKeycloak` (Keycloak refusing connections, then answering: ends `ready`, never `failed` while it is waiting);
  - `TestDeleteMasterUser`.
- [ ] **Step 2: Run** `go test ./internal/settings/ ./internal/idp/keycloak/ ./cmd/portico/` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same → PASS.
- [ ] **Step 5: Commit** `feat(setup): Portico creates the Keycloak realm once the public names are saved`.

### Task 6: The console back to design 4.8, the setup card order, the docs

**Files:**
- Modify:
  - `internal/console/console.go` (menu: `1) Network  2) Admin password  5) Refresh  0) Log out`; the second line stays the service status; under it "Open https://<address>:8443/ to continue the setup");
  - `internal/console/console_test.go`.
- Modify: `internal/web/templates/setup.html` (order: Public names and exposure, Identity, Sites, Tenants, Apply; each card's title is the name the docs use).
- Modify:
  - `docs/install.md`:
    - "What you need" gets the VM size;
    - the SSH key is optional;
    - steps 3 to 5 rewritten, with the console for the network only and the rest in the browser;
    - the Certificates section says "upload the two files";
  - `docs/operating.md` (renewing the certificate);
  - `deploy/README.md`;
  - `README.md` (if it mentions the console exposure);
  - design §4.2 and §4.8;
  - `docs/backlog.md` (Node 20 actions; `NetworkManager-wait-online` waits 60 s at the first boot for the unused "Wired connection 1").
- Create: `docs/adr/NNNN-setup-in-the-web-ui.md` (next number). It records:
  - why: Marco's install test of 2026-10-26;
  - what moved;
  - the root path unit;
  - the temporary Keycloak administrator deleted after use, and why that respects "never stores a human administrator's credentials".

- [ ] **Step 1: Failing test** `TestMenuIsNetworkPasswordStatus`: the menu has no "Exposure" and no "Keycloak", and choices 3 and 4 answer "unknown choice".
- [ ] **Step 2: Run** `go test ./internal/console/` → FAIL.
- [ ] **Step 3: Implement**, templates and docs.
- [ ] **Step 4: Run** `go test ./...`, `bash lab/test_scripts.sh` → PASS. Run `grep -rni 'Exposure (direct\|keycloak card\|show once' internal docs/install.md` → nothing.
- [ ] **Step 5: Commit** `feat(console): network, password and status only; setup docs for the browser path`.

### Task 7: The release again, and the reinstall from zero

- [ ] Review of the branch (one reviewer, most capable model), fix pass, one PR per topic or one PR for the whole rework, Marco's OK, merge.
- [ ] Marco deletes the `v0.1.0` draft and the tag (`gh release delete v0.1.0 --yes --cleanup-tag`, then `git tag -d v0.1.0`) and pushes `v0.1.0` again on the new main.
- [ ] I tear the appliance down and deploy the new qcow2 (VM only). Marco does the rest from the console and the browser, following `docs/install.md` only. Every friction point goes in write-up 30, part 2.
- [ ] acme back on both sites, the branding back, the logins checked; Marco presses Publish.
