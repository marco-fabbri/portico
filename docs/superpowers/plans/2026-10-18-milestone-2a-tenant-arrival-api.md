# Milestone 2a: Tenant Arrival API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `POST /api/v1/tenants` on the internal listener creates a tenant
end to end (Keycloak group, Projects 2.0 project with resource group, SAML
group, IdP sharing, role membership, static visibility check), records
every step, and a user created for that tenant logs in through the public
URL and sees only that project.

**Architecture:** Three new packages behind one service. `internal/store`
is an interface with a SQLite implementation (pure Go driver, WAL) holding tenants, their provisioning steps
and the break-glass account. `internal/nutanix` is the Prism Central client
(API key, the v4 calls measured in write-ups 05 and 07, ETag and task
handling). `internal/idp/keycloak` is the Keycloak admin client (client
credentials on the loopback). `internal/tenant` runs the idempotent steps
in order, in the background, and `internal/api` exposes them as JSON on the
admin listener behind HTTP Basic with the break-glass account. The admin UI,
the landing page, the Keycloak theme and the setup wizard with secrets at
rest are Milestone 2b: nothing in this plan puts a secret in the store, so
the master key is not needed yet.

**Tech Stack:** Go 1.27 standard library, `modernc.org/sqlite` (pure Go,
the binary is built with `CGO_ENABLED=0`), `golang.org/x/crypto/bcrypt`,
existing `gopkg.in/yaml.v3`. Lab: NTNX-01 pc.7.6.0.6, lab VM
`portico-lab`, Keycloak 26.3.

**Spec:** `docs/superpowers/specs/2026-10-17-portico-design.md` sections
4.4, 4.5, 4.7 (API boundary), 5, 6.3, 6.7, milestone 2 in section 9; ADR
0003. Evidence: `docs/lab/2026-10-17-05-projects2-api.md`,
`docs/lab/2026-10-17-07-service-account-role.md`,
`docs/lab/2026-10-17-04-saml-hostname.md`, payloads in `docs/lab/payloads/`,
working calls in `lab/projects/*.sh` and `lab/saml/keycloak-realm.sh`.

## Global Constraints

- All text on disk in English (CLAUDE.md). Commit messages in English.
- Work on branch `m2-tenant-arrival` from `main` (already created, holds
  the design update); integrate through a pull request merged by the
  executor (Marco's delegation of 2026-10-17).
- The binary stays `CGO_ENABLED=0` (`lab/portico/deploy.sh` cross-compiles
  from the Mac): the SQLite driver is `modernc.org/sqlite`, never
  `mattn/go-sqlite3`.
- Portico addresses only Prism Central objects it created and recorded:
  a project is created and deleted only when its name is
  `<project_prefix>-<slug>`; `_internal` (`isDefault`) is never read for
  writing and a project found with `isDefault=true` aborts provisioning.
- API versions pinned as measured on pc.7.6.0.6: projects and resource
  groups `/api/multidomain/v4.4.b1`, user groups and roles
  `/api/iam/v4.0`, SAML IdPs, role memberships and authorization policies
  `/api/iam/v4.1.b3`, tasks `/api/prism/v4.0`, clusters
  `/api/clustermgmt/v4.0`. Every v4 write carries `NTNX-Request-Id`
  (random UUID v4); every v4 update, delete and `$actions` carries
  `If-Match` with the ETag of a GET made just before.
- Prism Central authentication is only the API key header
  `X-ntnx-api-key`, read from a file with mode 0600 (spec §4.5, write-up
  07). Never Basic, never a password.
- Never log the value of `X-ntnx-api-key`, `Authorization`, any Keycloak
  token, any password or temporary password. The API key, the Keycloak
  client secret and the break-glass password reach the binary only through
  files with mode 0600 or stdin, never flags or environment.
- A temporary password is returned once in the API response and never
  stored, logged or returned again.
- Role mappings are always inside a project (`scopeTemplateName:
  "ProjectsScopeTemplate"`, `projectExtId` scope value); Portico never
  creates a global role binding (spec §5).
- `go vet ./...`, `gofmt -l cmd internal` and `go test ./...` are clean at
  every commit; `lab/test_scripts.sh` stays green.
- Lab: Prism Central `https://10.23.210.175:9440`, lab VM `10.23.210.190`,
  hosts `prism.lab.portico.test` and `id.lab.portico.test`, secrets in
  `~/portico-secrets/`. Lab project names start with `portico-lab-`
  (`project_prefix: portico-lab` in the lab settings).

## Review Focus

Failure modes the spec implies but a single task's tests might miss; each
has a test in the task named:

1. A slug with upper-case letters, a space, 40 characters, a leading `-`
   or empty: expected 400 from the API and zero calls to Prism Central or
   Keycloak (Task 5).
2. Provisioning fails at the role-membership step; the operator retries:
   expected the earlier steps are found and not duplicated (one project,
   one resource group, one SAML group in Prism Central) and the tenant
   ends `active` (Task 4).
3. Two requests for the same slug, or two operations on the same tenant at
   once: expected one proceeds and the other gets 409, never two
   provisioning runs for one tenant (Task 4 and Task 5).
4. Deleting a tenant whose Prism Central objects were already removed by
   hand: expected the deletion continues past the 404s and the row is
   removed (Task 4).
5. The admin API with no credentials, a wrong password or a Basic header
   on `/healthz`: expected 401 with `WWW-Authenticate` for the API, 200
   for `/healthz`, and no password in the log (Task 5).

---

### Task 0: Lab measurements deferred from the Milestone 1 review

**Files:**
- Create: `lab/auth/probe-bearer-and-v3-users.sh`
- Create: `docs/lab/2026-10-18-10-bearer-and-v3-users.md`
- Modify: `docs/lab/README.md` (row 10)
- Modify (conditional, see Step 4): `internal/proxy/policy.go`,
  `internal/proxy/policy_test.go`, `lab/auth/probe-local-login.sh`

**Interfaces:**
- Consumes: `lab/env.sh`, `lab/pc.sh`, `PrismPolicy.Deny` from Milestone 1.
- Produces: measured facts the policy depends on; possibly two new deny
  rules `deny-bearer-auth` and `deny-v3-user-listing`.

Two questions the Milestone 1 reviewer left open. Both are measured on the
internal pass-through (`https://10.23.210.190:9440`), which needs no
Portico session, because the question is what Prism Central accepts, not
what Portico forwards.

- [ ] **Step 1: Write `lab/auth/probe-bearer-and-v3-users.sh`**

Prints one `got/expected` line per probe like `probe-local-login.sh`, exit
1 on any mismatch. Probes, all with `portico-lab-bob` (Basic, password
`~/portico-secrets/bob-pw`, only on the pass-through):

1. Local login via the pass-through with curl: `GET /console/` (cookie
   jar) → follow the `iam-sessionid` flow of write-up 06 with
   `validate-credentials` → follow `authCodeUri` → record every cookie set
   and any JSON field named `token`, `access_token` or `iam-token`.
2. For each token-like value found: `GET /api/nutanix/v3/vms/list`
   (POST with `{"kind":"vm"}`) with `Authorization: Bearer <value>` and no
   cookies, on the pass-through. Record the status.
3. `POST /api/nutanix/v3/users/list` `{"kind":"user"}` with bob's Basic:
   record the status and `metadata.total_matches`.
4. `GET /api/iam/v4.0/authn/users` with bob's Basic: record the count
   (expected: all users, as in write-up 05).

The script prints the facts; the expected values are filled in after the
first run (the script's `expected` column is the measured value, so the
script becomes the re-measure procedure).

- [ ] **Step 2: Run it**

Run: `source lab/env.sh && lab/auth/probe-bearer-and-v3-users.sh`
Expected: the four facts printed; the exit status is irrelevant on the
first run.

- [ ] **Step 3: Write `docs/lab/2026-10-18-10-bearer-and-v3-users.md`**

Use `docs/lab/TEMPLATE.md`. Record: whether Prism Central issues a token a
client can replay as `Authorization: Bearer`, and whether `v3 users/list`
enumerates all users for a Project Admin. Add row 10 to
`docs/lab/README.md`.

- [ ] **Step 4: Apply the measured rule, test first**

Two outcomes per question, each with one concrete change:

- Bearer accepted by Prism Central for a local account (any 200 in probe
  2): add to `policy_test.go` the case `{"bearer header", header
  Authorization: "Bearer x", want "deny-bearer-auth"}`, run it (FAIL),
  then extend the `Authorization` loop in `PrismPolicy.Deny` to deny
  `bearer` as well as `basic` with rule `deny-bearer-auth`. Bearer not
  accepted (every probe 2 is 401): no code change; the write-up says why
  Bearer stays allowed.
- `v3 users/list` returns more than bob's own user: add the test case
  `{"v3 user listing", POST /api/nutanix/v3/users/list, want
  "deny-v3-user-listing"}`, run it (FAIL), add
  `reV3UserListing = regexp.MustCompile(`^/api/nutanix/v3/users/list$`)`
  matched for any method, rule `deny-v3-user-listing`. Otherwise no change.

For each rule added: a line in `lab/auth/probe-local-login.sh` expecting
403 on the public name.

- [ ] **Step 5: Run the suite and the probes**

Run: `go test ./internal/proxy/ && source lab/env.sh && lab/auth/probe-local-login.sh`
Expected: PASS; probe exit 0 (after Task 6 redeploys; until then the new
probe lines are expected to fail and the ledger says so).

- [ ] **Step 6: Commit**

```bash
git add lab/auth/probe-bearer-and-v3-users.sh docs/lab/2026-10-18-10-bearer-and-v3-users.md docs/lab/README.md internal/proxy lab/auth/probe-local-login.sh
git commit -m "lab: measure Bearer replay and v3 user enumeration (write-up 10)"
```

---

### Task 1: SQLite store and the break-glass password command

**Files:**
- Create: `internal/store/store.go`, `internal/store/tenant.go`,
  `internal/store/admin.go`, `internal/store/migrations/0001_init.sql`
  (embedded with `embed`)
- Create: `internal/store/store_test.go`
- Modify: `cmd/portico/main.go` (subcommand `admin set-password`)
- Modify: `go.mod` (`modernc.org/sqlite` latest v1, `golang.org/x/crypto`)

**Interfaces:**
- Produces:

```go
package store

var ErrNotFound = errors.New("store: not found")
var ErrExists = errors.New("store: already exists")

type Status string
const (
    StatusCreating Status = "creating"
    StatusActive   Status = "active"
    StatusFailed   Status = "failed"
    StatusDisabled Status = "disabled"
    StatusDeleting Status = "deleting"
)

type Tenant struct {
    ID, DisplayName, Contacts string
    Status Status
    ProjectID, ProjectExtID, ResourceGroupExtID string
    SAMLGroupExtID, KeycloakGroupID, RoleMembershipExtID string
    IdPShared bool
    LastError string
    CreatedAt, UpdatedAt time.Time
}

type Step struct {
    TenantID, Name string
    OK bool
    Detail string
    At time.Time
}

// Store is the interface every consumer (tenant service, API, serve) uses.
// SQLite is the first implementation; PostgreSQL is added for the pair
// topology (spec §4.11) without touching the consumers.
type Store interface {
    Close() error
    CreateTenant(ctx context.Context, t *Tenant) error   // ErrExists on duplicate ID; sets CreatedAt/UpdatedAt
    GetTenant(ctx context.Context, id string) (*Tenant, error)   // ErrNotFound
    ListTenants(ctx context.Context) ([]Tenant, error)   // ordered by ID
    UpdateTenant(ctx context.Context, t *Tenant) error   // ErrNotFound; bumps UpdatedAt
    DeleteTenant(ctx context.Context, id string) error   // also deletes its steps
    AddStep(ctx context.Context, st Step) error          // sets At when zero
    ListSteps(ctx context.Context, tenantID string) ([]Step, error)  // in insertion order
    SetAdminPassword(ctx context.Context, name, bcryptHash string) error  // upsert
    AdminPasswordHash(ctx context.Context, name string) (string, error)  // ErrNotFound
}

type SQLite struct{ /* *sql.DB */ }   // implements Store
func OpenSQLite(path string) (*SQLite, error)   // creates the file, journal_mode=WAL, busy_timeout=5000, runs migrations
var _ Store = (*SQLite)(nil)
```

Schema (`0001_init.sql`): `tenants` (id TEXT PRIMARY KEY, display_name,
contacts, status, project_id, project_ext_id, resource_group_ext_id,
saml_group_ext_id, keycloak_group_id, role_membership_ext_id, idp_shared
INTEGER, last_error, created_at, updated_at); `tenant_steps` (seq INTEGER
PRIMARY KEY AUTOINCREMENT, tenant_id REFERENCES tenants ON DELETE CASCADE,
name, ok INTEGER, detail, at); `admin_accounts` (name TEXT PRIMARY KEY,
password_hash, updated_at); `schema_migrations` (version INTEGER PRIMARY
KEY). Timestamps as RFC 3339 UTC text. `PRAGMA foreign_keys=ON` on open.

- [ ] **Step 1: Write the failing tests in `store_test.go`**

```go
func TestOpenCreatesSchemaAndIsIdempotent(t *testing.T)   // OpenSQLite twice on the same temp path; second open succeeds; file exists
func TestTenantCRUD(t *testing.T)                          // Create → Get equal (ignoring timestamps); Create again → ErrExists; Update changes Status; List has 1; Delete → Get ErrNotFound
func TestStepsAreOrderedAndCascade(t *testing.T)           // AddStep ×3 → ListSteps order; DeleteTenant → ListSteps empty
func TestAdminPassword(t *testing.T)                       // AdminPasswordHash → ErrNotFound; Set → hash returned; Set again overwrites
func TestOpenSQLiteRefusesUnwritableDir(t *testing.T)            // path under a non-existent dir → error mentions the path
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/store/`
Expected: FAIL to compile (package missing).

- [ ] **Step 3: Implement the store**

`go get modernc.org/sqlite@latest golang.org/x/crypto@latest`. DSN
`file:<path>?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)`
with `sql.Open("sqlite", dsn)`. Migrations: read embedded files sorted,
apply those whose version is above `MAX(version)` inside one transaction
each. `db.SetMaxOpenConns(1)` (one writer; SQLite serialises anyway).

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/store/`
Expected: PASS (5 tests).

- [ ] **Step 5: Write the failing test for the command in `cmd/portico/main_test.go`**

```go
func TestAdminSetPasswordReadsStdin(t *testing.T)
// config file with store.path under t.TempDir(); run(["admin","set-password","-config",cfg], stdin "s3cret\n")
// → exit 0; store.AdminPasswordHash("admin") verifies with bcrypt.CompareHashAndPassword
func TestAdminSetPasswordRefusesEmpty(t *testing.T)   // stdin "\n" → exit 2, stderr "password is empty"
```

(`run` gains an `stdin io.Reader` parameter; `main` passes `os.Stdin`.)
This test needs `store.path` in the config: Task 6 adds the `store`
section; for this task add only the field `Store struct{ Path string }`
with default `/var/lib/portico/portico.db` to `config.Config` and keep
Validate unchanged.

- [ ] **Step 6: Run it**

Run: `go test ./cmd/portico/ -run TestAdminSetPassword`
Expected: FAIL ("unknown command admin" or compile error).

- [ ] **Step 7: Implement `admin set-password`**

In `main.go`: subcommand `admin` with sub-subcommand `set-password -config
PATH`; reads the first line of stdin, trims `\r\n`, rejects empty,
`bcrypt.GenerateFromPassword(pw, 12)`, `store.OpenSQLite(cfg.Store.Path)`,
`SetAdminPassword(ctx, "admin", hash)`, prints `password set for admin`.
Usage text documents: `printf '%s\n' "$PW" | portico admin set-password
-config /etc/portico/portico.yaml` run as the `portico` user.

- [ ] **Step 8: Run the suite**

Run: `go test ./... && go vet ./... && gofmt -l cmd internal`
Expected: PASS, no vet output, no files listed.

- [ ] **Step 9: Commit**

```bash
git add go.mod go.sum internal/store internal/config cmd/portico
git commit -m "feat: SQLite store for tenants, steps and the break-glass account"
```

---

### Task 2: Prism Central client

**Files:**
- Create: `internal/nutanix/client.go` (transport, `do`, errors, tasks),
  `internal/nutanix/projects.go`, `internal/nutanix/iam.go`
- Create: `internal/nutanix/client_test.go`, `internal/nutanix/fakepc_test.go`
  (an `httptest` Prism Central that records requests and answers from the
  shapes in `docs/lab/payloads/` and write-up 05)

**Interfaces:**
- Produces:

```go
package nutanix

const (
    PathMultidomain = "/api/multidomain/v4.4.b1"
    PathIAM0        = "/api/iam/v4.0"
    PathIAM1        = "/api/iam/v4.1.b3"
    PathTasks       = "/api/prism/v4.0/config/tasks"
    ScopeTemplate   = "ProjectsScopeTemplate"
)

type APIError struct{ Status int; Message string }   // Error(): "prism central: <status> <message>"
func IsNotFound(err error) bool                      // APIError with 404

type Client struct{ /* base URL, api key, *http.Client, log */ }
func New(baseURL, apiKey string, tlsCfg *tls.Config, log *slog.Logger) (*Client, error)
func (c *Client) WaitTask(ctx context.Context, taskExtID string) error   // poll every 5 s until SUCCEEDED; FAILED/CANCELED → APIError with errorMessages; ctx deadline → ctx.Err()

type Project struct{ ExtID, ID, Name string; IsDefault bool }
func (c *Client) ListProjects(ctx context.Context) ([]Project, error)
func (c *Client) FindProject(ctx context.Context, name string) (*Project, error)              // ErrNotFound-style APIError 404 when absent
func (c *Client) CreateProject(ctx context.Context, id, name, description string) (*Project, error)  // POST, WaitTask, FindProject
func (c *Client) DeleteProject(ctx context.Context, extID string) error                         // GET for ETag, DELETE with If-Match, WaitTask

type ResourceGroup struct{ ExtID, Name, ProjectExtID string }
func (c *Client) ListResourceGroups(ctx context.Context, projectExtID string) ([]ResourceGroup, error)
func (c *Client) CreateResourceGroup(ctx context.Context, name, projectExtID, clusterExtID, containerExtID string) (*ResourceGroup, error)
func (c *Client) DeleteResourceGroup(ctx context.Context, extID string) error

type SAMLIdP struct{ ExtID, Name string; SharedWithAll bool; SharedWithProjects []string }
func (c *Client) ListSAMLIdPs(ctx context.Context) ([]SAMLIdP, error)
func (c *Client) GetSAMLIdP(ctx context.Context, extID string) (*SAMLIdP, error)
func (c *Client) ShareIdP(ctx context.Context, idpExtID, projectExtID string) error     // POST .../authn/config/saml-identity-provider/{id}/$actions/share {projectExtId} with If-Match
func (c *Client) UnshareIdP(ctx context.Context, idpExtID, projectExtID string) error

type UserGroup struct{ ExtID, Name string }
func (c *Client) FindUserGroup(ctx context.Context, name string) (*UserGroup, error)
func (c *Client) CreateUserGroup(ctx context.Context, name, idpExtID string) (*UserGroup, error)   // {name, groupType:"SAML", idpId}
func (c *Client) DeleteUserGroup(ctx context.Context, extID string) error

type Role struct{ ExtID, DisplayName string }
func (c *Client) FindRole(ctx context.Context, displayName string) (*Role, error)   // $filter=displayName eq '<name>'

type RoleMembership struct{ ExtID, IdentityExtID, IdentityType, ProjectExtID, RoleExtID, ScopeTemplateName string }
func (c *Client) ListRoleMemberships(ctx context.Context) ([]RoleMembership, error)
func (c *Client) CreateRoleMembership(ctx context.Context, groupExtID, idpExtID, projectExtID, roleExtID string) (*RoleMembership, error)  // body as docs/lab/payloads/role-membership-create.json
func (c *Client) DeleteRoleMembership(ctx context.Context, extID string) error

func (c *Client) PoliciesMentioning(ctx context.Context, extID string) ([]string, error)
// displayName of every entry of GET /api/iam/v4.1.b3/authz/authorization-policies whose raw JSON contains extID
```

`do(ctx, method, path string, body any, ifMatch string, out any) (etag
string, err error)`: headers `Accept`/`Content-Type: application/json`,
`X-ntnx-api-key`, `NTNX-Request-Id` (random v4 from `crypto/rand`) on
POST/PUT/DELETE, `If-Match` when non-empty; decodes the `{"data": ...}`
envelope into `out`; non-2xx → `APIError` with `data.error`,
`data.message` or the first 200 bytes of the body (the log line carries
method, path, status, duration only). `http.Client` timeout 60 s per call
(the lab Prism Central answered one call in 48 s on 2026-10-18).

- [ ] **Step 1: Write the fake and the failing tests**

```go
func TestCreateProjectPostsWaitsAndFinds(t *testing.T)     // fake records POST body {id,name,description}, NTNX-Request-Id present, X-ntnx-api-key == key; task SUCCEEDED after 2 polls; returned Project has the fake's extId
func TestDeleteProjectSendsIfMatch(t *testing.T)           // fake GET returns ETag "abc"; DELETE carries If-Match: abc
func TestShareIdPUsesSingularProjectExtId(t *testing.T)    // body == {"projectExtId": "<p>"}, If-Match set
func TestCreateRoleMembershipBodyShape(t *testing.T)       // scopeTemplateName == "ProjectsScopeTemplate", scopeTemplateNameValues[0] == {name:"projectExtId", value:<p>}, identityType "GROUP"
func TestFindRoleEscapesFilter(t *testing.T)               // request query $filter=displayName eq 'Project Admin' (URL-encoded)
func TestAPIErrorFromBody(t *testing.T)                    // 400 {"data":{"error":"missing property 'id'"}} → APIError{400, "missing property 'id'"}; IsNotFound on 404
func TestWaitTaskFailure(t *testing.T)                     // FAILED with errorMessages → error contains the message
func TestKeyNeverLogged(t *testing.T)                      // slog to a buffer; after a failed call the buffer does not contain the key
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/nutanix/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement the client**

`ListProjects` paginates with `$page`/`$limit=100` until `metadata.totalAvailableResults`
is reached (the fake serves one page). ETag: the value of the `ETag`
response header of the GET immediately before the write.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/nutanix/`
Expected: PASS (8 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/nutanix
git commit -m "feat: Prism Central client for projects, IAM and tasks"
```

---

### Task 3: Keycloak admin client and its lab client registration

**Files:**
- Create: `internal/idp/keycloak/client.go`, `internal/idp/keycloak/client_test.go`
- Modify: `lab/saml/keycloak-realm.sh` (new subcommand `admin-client`)
- Modify: `lab/README.md` (one line for the new subcommand)

**Interfaces:**
- Produces:

```go
package keycloak

var ErrNotFound = errors.New("keycloak: not found")

type Client struct{ /* base URL (loopback), realm, client id, secret, http, token cache, log */ }
func New(baseURL, realm, clientID, clientSecret string, httpc *http.Client, log *slog.Logger) *Client

type Group struct{ ID, Name string }
func (c *Client) FindGroup(ctx context.Context, name string) (*Group, error)   // GET /admin/realms/{r}/groups?search=<name>&exact=true; ErrNotFound
func (c *Client) CreateGroup(ctx context.Context, name string) (*Group, error) // POST, then FindGroup
func (c *Client) DeleteGroup(ctx context.Context, id string) error             // 404 → nil

type User struct{ ID, Email, FirstName, LastName string; Enabled bool }
func (c *Client) FindUserByEmail(ctx context.Context, email string) (*User, error)   // ?email=<e>&exact=true
func (c *Client) CreateUser(ctx context.Context, u User, temporaryPassword string) (*User, error)
// body: username=email, email, firstName, lastName, enabled:true, emailVerified:true,
// requiredActions:["UPDATE_PASSWORD"], credentials:[{type:"password", value, temporary:true}]
func (c *Client) AddUserToGroup(ctx context.Context, userID, groupID string) error    // PUT /users/{u}/groups/{g}
func (c *Client) GroupMembers(ctx context.Context, groupID string) ([]User, error)   // GET /groups/{g}/members?max=1000
func (c *Client) SetUserEnabled(ctx context.Context, userID string, enabled bool) error  // GET user, set enabled, PUT
```

Token: `POST {base}/realms/{realm}/protocol/openid-connect/token` with
`grant_type=client_credentials`, cached until 30 s before `expires_in`.
The admin API path is `{base}/admin/realms/{realm}`. The lab client
`portico-admin` is a confidential client of realm `portico-lab` with
service accounts enabled and the `realm-management` roles `manage-users`,
`view-users`, `query-users`, `query-groups`, `view-realm` mapped to its
service-account user.

- [ ] **Step 1: Write the failing tests with an `httptest` Keycloak**

```go
func TestTokenIsRequestedOnceWhileValid(t *testing.T)   // two FindGroup calls → one token request; Authorization: Bearer <token> on both
func TestCreateUserBody(t *testing.T)                    // username == email; requiredActions == ["UPDATE_PASSWORD"]; credentials[0].temporary == true
func TestFindGroupNotFound(t *testing.T)                 // [] → ErrNotFound
func TestDeleteGroupTolerates404(t *testing.T)
func TestSecretAndPasswordNeverLogged(t *testing.T)      // failing call; log buffer has neither the secret nor the temporary password
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/idp/keycloak/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement the client**

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/idp/keycloak/`
Expected: PASS (5 tests).

- [ ] **Step 5: Add `admin-client` to `lab/saml/keycloak-realm.sh`**

`lab/saml/keycloak-realm.sh admin-client`: creates or updates client
`portico-admin` (`publicClient:false, serviceAccountsEnabled:true,
standardFlowEnabled:false, directAccessGrantsEnabled:false`), finds its
service-account user (`GET /clients/{id}/service-account-user`), the
`realm-management` client id, the five role representations
(`GET /clients/{rm}/roles/{name}`), maps them
(`POST /users/{sa}/role-mappings/clients/{rm}` with the array), and saves
the client secret to `~/portico-secrets/portico-kc-admin-secret` (mode
0600, only when the file is missing). Idempotent.

- [ ] **Step 6: Run it against the lab and verify a token works**

Run: `source lab/env.sh && lab/saml/keycloak-realm.sh admin-client && lab/test_scripts.sh`
Expected: `admin client created` (or `updated`), secret file present;
`lab/test_scripts.sh` green. Then on the VM loopback (over SSH, secret via
stdin as `kc.sh` does): a `client_credentials` token for `portico-admin`
lists the realm groups with 200.

- [ ] **Step 7: Commit**

```bash
git add internal/idp lab/saml/keycloak-realm.sh lab/README.md
git commit -m "feat: Keycloak admin client; lab admin-client registration"
```

---

### Task 4: Tenant service: provisioning, deletion, users, visibility

**Files:**
- Create: `internal/tenant/service.go` (types, Create, Retry, Delete,
  Disable, Enable, CreateUser, run/lock), `internal/tenant/provision.go`
  (the steps), `internal/tenant/visibility.go`, `internal/tenant/slug.go`
- Create: `internal/tenant/service_test.go`, `internal/tenant/fakes_test.go`
  (in-memory `PrismCentral` and `Keycloak` fakes that count creates and
  can fail a named step)

**Interfaces:**
- Consumes: the `store.Store` interface, `nutanix.Client` methods, `keycloak.Client`
  methods, through two interfaces declared in this package:

```go
type PrismCentral interface {
    FindProject(ctx context.Context, name string) (*nutanix.Project, error)
    CreateProject(ctx context.Context, id, name, description string) (*nutanix.Project, error)
    DeleteProject(ctx context.Context, extID string) error
    ListResourceGroups(ctx context.Context, projectExtID string) ([]nutanix.ResourceGroup, error)
    CreateResourceGroup(ctx context.Context, name, projectExtID, clusterExtID, containerExtID string) (*nutanix.ResourceGroup, error)
    DeleteResourceGroup(ctx context.Context, extID string) error
    ListSAMLIdPs(ctx context.Context) ([]nutanix.SAMLIdP, error)
    GetSAMLIdP(ctx context.Context, extID string) (*nutanix.SAMLIdP, error)
    ShareIdP(ctx context.Context, idpExtID, projectExtID string) error
    UnshareIdP(ctx context.Context, idpExtID, projectExtID string) error
    FindUserGroup(ctx context.Context, name string) (*nutanix.UserGroup, error)
    CreateUserGroup(ctx context.Context, name, idpExtID string) (*nutanix.UserGroup, error)
    DeleteUserGroup(ctx context.Context, extID string) error
    FindRole(ctx context.Context, displayName string) (*nutanix.Role, error)
    ListRoleMemberships(ctx context.Context) ([]nutanix.RoleMembership, error)
    CreateRoleMembership(ctx context.Context, groupExtID, idpExtID, projectExtID, roleExtID string) (*nutanix.RoleMembership, error)
    DeleteRoleMembership(ctx context.Context, extID string) error
    PoliciesMentioning(ctx context.Context, extID string) ([]string, error)
}
type Keycloak interface {
    FindGroup(ctx context.Context, name string) (*keycloak.Group, error)
    CreateGroup(ctx context.Context, name string) (*keycloak.Group, error)
    DeleteGroup(ctx context.Context, id string) error
    FindUserByEmail(ctx context.Context, email string) (*keycloak.User, error)
    CreateUser(ctx context.Context, u keycloak.User, temporaryPassword string) (*keycloak.User, error)
    AddUserToGroup(ctx context.Context, userID, groupID string) error
    GroupMembers(ctx context.Context, groupID string) ([]keycloak.User, error)
    SetUserEnabled(ctx context.Context, userID string, enabled bool) error
}
```

- Produces:

```go
package tenant

var ErrInvalidSlug = errors.New("tenant: invalid slug")   // ^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$
var ErrBusy = errors.New("tenant: operation in progress")
var ErrState = errors.New("tenant: operation not allowed in this status")

type Config struct {
    ProjectPrefix         string   // "portico" default; project name/id = prefix + "-" + slug
    GroupPrefix           string   // "tenant-" default; Keycloak group and SAML group = GroupPrefix + slug
    RoleName              string   // "Project Admin" default
    SAMLIdPName           string   // the IdP in Prism Central to share; required
    ClusterExtID          string   // required
    StorageContainerExtID string   // required
    OperationTimeout      time.Duration   // 10 min default: context of a background run
}

type Service struct{ /* store, pc, kc, cfg, log, mu, running map[string]bool */ }
func NewService(st store.Store, pc PrismCentral, kc Keycloak, cfg Config, log *slog.Logger) *Service

type CreateRequest struct{ Slug, DisplayName, Contacts string }
func (s *Service) Create(ctx context.Context, req CreateRequest) (*store.Tenant, error)
// validates, inserts status creating (store.ErrExists passes through), starts Provision in the background, returns the row
func (s *Service) Provision(ctx context.Context, id string) error   // synchronous steps; exported for tests and Retry
func (s *Service) Retry(ctx context.Context, id string) error       // status failed only → background Provision
func (s *Service) Delete(ctx context.Context, id string, deleteProject bool) error   // status deleting, background reverse steps, row removed at the end
func (s *Service) Disable(ctx context.Context, id string) error     // every Keycloak member enabled=false; status disabled
func (s *Service) Enable(ctx context.Context, id string) error      // members enabled=true; status active
func (s *Service) CreateUser(ctx context.Context, id, email, firstName, lastName string) (temporaryPassword string, err error)
// 20 characters from [A-Za-z0-9] with at least one of each class, crypto/rand; ErrState unless active

type Visibility struct{ OK bool; Findings []string }
func (s *Service) Visibility(ctx context.Context, id string) (*Visibility, error)
```

Provisioning steps, in order, each find-then-create and each recorded
with `AddStep` (name, ok, detail = extId or error):
`keycloak-group`, `project`, `resource-group` (name `<project>-rg`),
`saml-group`, `idp-share`, `role-membership`, `visibility`. The IdP is
`ListSAMLIdPs` filtered by `cfg.SAMLIdPName` (none or several → error
before any write). A found project with `IsDefault` aborts. Success sets
`StatusActive`; any error sets `StatusFailed` and `LastError`. Deletion in
reverse: `role-membership` (every membership of the group), `idp-unshare`,
`saml-group`, then if `deleteProject`: `resource-group` (all of the
project) and `project` (only when its name is `<prefix>-<slug>`), then
`keycloak-group`, then the row; a 404 from Prism Central or Keycloak on a
deletion step is recorded ok with detail `already absent`. Background runs
use `context.WithTimeout(context.Background(), cfg.OperationTimeout)` and
the per-tenant lock (`ErrBusy` while running).

Visibility (static, spec §4.4): project exists by extId; exactly one
role membership whose identity is the SAML group and its `ProjectExtID`
is the tenant's project and `ScopeTemplateName == "ProjectsScopeTemplate"`;
no membership of that group in another project; `PoliciesMentioning(group
extId)` is empty. Each violation is one finding; `OK` is `len(Findings)==0`.

- [ ] **Step 1: Write the fakes and the failing tests**

```go
func TestSlugValidation(t *testing.T)   // table: "acme" ok; "Acme", "a b", "-a", "a-", "", 33 chars → ErrInvalidSlug; fakes record zero calls
func TestProvisionHappyPath(t *testing.T)   // Create then Provision synchronously: fakes hold 1 group, 1 project named "portico-acme" id "portico-acme", 1 rg, 1 saml group "tenant-acme", idp shared with project, 1 membership; tenant active; 7 steps all ok
func TestProvisionIsIdempotentAfterFailure(t *testing.T)   // fake PC fails CreateRoleMembership once → status failed, LastError set; Retry/Provision again → active; create counters: project 1, rg 1, saml group 1, kc group 1
func TestProvisionRefusesDefaultProject(t *testing.T)   // fake FindProject returns IsDefault → failed, no resource group created
func TestProvisionRequiresExactlyOneIdP(t *testing.T)    // 0 and 2 IdPs named cfg.SAMLIdPName → error, no writes
func TestCreateDuplicateSlug(t *testing.T)               // second Create → store.ErrExists
func TestBusyWhileRunning(t *testing.T)                  // fake PC blocks on CreateProject; Create then Delete → ErrBusy
func TestDeleteReverseOrderAndTolerates404(t *testing.T) // fakes return not-found for membership and saml group; Delete(deleteProject=true) removes rg and project; row gone; steps include "already absent"
func TestDeleteKeepsProjectByDefault(t *testing.T)       // deleteProject=false → project still in fake PC, row gone
func TestDeleteRefusesForeignProjectName(t *testing.T)   // tenant row ProjectID "other-name" → project not deleted, step detail says why
func TestDisableEnable(t *testing.T)                     // two members → both enabled=false, status disabled; Enable → true, active
func TestCreateUserTemporaryPasswordShape(t *testing.T)  // 20 chars, has upper, lower, digit; fake user has UPDATE_PASSWORD; user in group; ErrState when tenant failed
func TestVisibilityFindings(t *testing.T)                // membership in another project + policy mentioning group → 2 findings, OK false
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/tenant/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement the service**

- [ ] **Step 4: Run the tests with the race detector**

Run: `go test -race ./internal/tenant/`
Expected: PASS (13 tests), no race.

- [ ] **Step 5: Commit**

```bash
git add internal/tenant
git commit -m "feat: tenant service with idempotent provisioning and static visibility check"
```

---

### Task 5: Admin REST API with break-glass Basic authentication

**Files:**
- Create: `internal/api/api.go` (routes, JSON helpers), `internal/api/auth.go`
  (Basic middleware), `internal/api/api_test.go`
- Modify: `internal/proxy/internal.go` (`NewAdmin(log, api http.Handler)`
  mounts `api` under `/api/v1/`; `/healthz` stays open), `internal/proxy/internal_test.go`

**Interfaces:**
- Consumes: `tenant.Service`, `store.Store` (interface) for `AdminPasswordHash`.
- Produces:

```go
package api

func New(svc *tenant.Service, st store.Store, log *slog.Logger) http.Handler
// routes (all JSON, Cache-Control: no-store):
//   GET    /api/v1/tenants                      200 [tenantJSON]
//   POST   /api/v1/tenants                      {slug, display_name, contacts} → 202 tenantJSON (status creating); 400 invalid slug/json; 409 exists
//   GET    /api/v1/tenants/{id}                 200 tenantJSON; 404
//   GET    /api/v1/tenants/{id}/steps           200 [{name, ok, detail, at}]
//   POST   /api/v1/tenants/{id}/provision       202 (retry); 409 busy or wrong status
//   DELETE /api/v1/tenants/{id}?delete_project=true|false   202; 409 busy
//   POST   /api/v1/tenants/{id}/disable | /enable           200 tenantJSON; 409
//   POST   /api/v1/tenants/{id}/users           {email, first_name, last_name} → 201 {id, email, temporary_password}; 409 ErrState
//   GET    /api/v1/tenants/{id}/visibility      200 {ok, findings}
// errors: {"error": "<message>"}; unknown path 404 {"error":"not found"}
func BasicAuth(st store.Store, realm string, log *slog.Logger) func(http.Handler) http.Handler
// user "admin"; bcrypt.CompareHashAndPassword; missing/invalid → 401 with WWW-Authenticate: Basic realm="<realm>", body {"error":"unauthorized"}; no password or hash in any log line
```

`tenantJSON`: `{id, display_name, status, contacts, project_id,
project_ext_id, resource_group_ext_id, saml_group_ext_id, keycloak_group_id,
role_membership_ext_id, idp_shared, last_error, created_at, updated_at}`.

- [ ] **Step 1: Write the failing tests (real `store` on a temp file, `tenant.Service` with the fakes of Task 4 moved to an internal test helper package `internal/tenant/tenanttest`)**

```go
func TestUnauthenticatedIs401WithChallenge(t *testing.T)   // no header → 401, WWW-Authenticate present; wrong password → 401; healthz → 200 without credentials
func TestCreateTenantReturns202AndRow(t *testing.T)        // POST → 202, status "creating"; GET → row; GET /steps eventually has 7 ok (poll ≤ 5 s)
func TestInvalidSlugIs400WithNoBackendCalls(t *testing.T)  // Review Focus 1
func TestDuplicateIs409(t *testing.T)                      // Review Focus 3
func TestBusyIs409(t *testing.T)                           // blocking fake; DELETE while creating → 409
func TestCreateUserReturnsPasswordOnce(t *testing.T)       // 201 body has temporary_password; GET tenant and steps never contain it; log buffer never contains it
func TestPasswordNeverInLog(t *testing.T)                  // wrong Basic password "hunter2" → log buffer lacks "hunter2" (Review Focus 5)
func TestUnknownPathIs404JSON(t *testing.T)
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/api/ ./internal/proxy/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement the handler and the middleware; change `NewAdmin`**

`NewAdmin(log, api)`: mux with `GET /healthz` open and `/api/v1/` handled
by `api` (the caller wraps `api` with `BasicAuth`). `AccessLog` as before.

- [ ] **Step 4: Run the suite**

Run: `go test ./... && go vet ./... && gofmt -l cmd internal`
Expected: PASS, clean.

- [ ] **Step 5: Commit**

```bash
git add internal/api internal/proxy internal/tenant
git commit -m "feat: tenant REST API on the admin listener behind the break-glass account"
```

---

### Task 6: Settings, `portico serve` wiring, deployment

**Files:**
- Modify: `internal/config/config.go` (+ `config_test.go`, `testdata/minimal.yaml`)
- Modify: `cmd/portico/serve.go`, `cmd/portico/serve_test.go`
- Modify: `deploy/portico.yaml.example`, `lab/portico/portico.yaml`,
  `lab/portico/deploy.sh`, `lab/test_scripts.sh` (checks for the new
  scripts if it enumerates them)

**Interfaces:**
- Produces, in `config.Config`:

```go
type Store struct{ Path string }                                   // default /var/lib/portico/portico.db
// PrismCentral gains: APIKeyFile string `yaml:"api_key_file"` (required), SAMLIdPName string `yaml:"saml_idp_name"` (required)
// Keycloak gains: Realm string `yaml:"realm"` (required), AdminClientID string `yaml:"admin_client_id"` (default "portico-admin"), AdminClientSecretFile string `yaml:"admin_client_secret_file"` (required)
type Tenants struct {
    ProjectPrefix         string `yaml:"project_prefix"`           // default "portico"
    GroupPrefix           string `yaml:"group_prefix"`             // default "tenant-"
    Role                  string `yaml:"role"`                     // default "Project Admin"
    ClusterExtID          string `yaml:"cluster_ext_id"`           // required
    StorageContainerExtID string `yaml:"storage_container_ext_id"` // required
    OperationTimeout      time.Duration `yaml:"operation_timeout"` // default 10m
}
func (c *Config) PrismAPIKey() (string, error)          // readSecretFile
func (c *Config) KeycloakAdminSecret() (string, error)  // readSecretFile
```

- [ ] **Step 1: Write the failing config tests**

```go
func TestValidateRequiresMilestone2Fields(t *testing.T)   // minimal.yaml without them → error lists prism_central.api_key_file, prism_central.saml_idp_name, keycloak.realm, keycloak.admin_client_secret_file, tenants.cluster_ext_id, tenants.storage_container_ext_id
func TestDefaultsForTenants(t *testing.T)                 // prefix "portico", group prefix "tenant-", role "Project Admin", timeout 10m, store path
func TestPrefixMustBeSlugSafe(t *testing.T)               // project_prefix "Bad Prefix" → error
```

Update `testdata/minimal.yaml` so the existing tests keep passing with the
new required keys.

- [ ] **Step 2: Run them**

Run: `go test ./internal/config/`
Expected: FAIL.

- [ ] **Step 3: Implement the fields, defaults and validation**

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/config/`
Expected: PASS.

- [ ] **Step 5: Write the failing serve test**

```go
func TestServeOpensStoreAndMountsAPI(t *testing.T)   // runServe with loopback listeners, temp store, fake PC/KC URLs; GET /api/v1/tenants without credentials → 401; GET /healthz → 200; store file exists; cancel ctx → returns nil
```

- [ ] **Step 6: Run it**

Run: `go test ./cmd/portico/ -run TestServeOpensStore`
Expected: FAIL (401 not returned: no API mounted).

- [ ] **Step 7: Wire `runServe`**

Order: `store.OpenSQLite(cfg.Store.Path)` (error aborts start; the variable is typed `store.Store`), read the API key and the
Keycloak admin secret, `nutanix.New(cfg.PrismCentral.URL, key, pcTLS,
log)`, `keycloak.New(cfg.Keycloak.URL, cfg.Keycloak.Realm, ...)`,
`tenant.NewService(...)` with `tenant.Config` from `cfg.Tenants` and
`cfg.PrismCentral.SAMLIdPName`, `api.New(...)` wrapped in
`api.BasicAuth(store, "portico", log)`, passed to `proxy.NewAdmin`. Close
the store on shutdown.

- [ ] **Step 8: Run the suite**

Run: `go test ./... && go vet ./... && gofmt -l cmd internal`
Expected: PASS, clean.

- [ ] **Step 9: Deployment files**

- `deploy/portico.yaml.example`: the new keys with comments (where the
  cluster and container ext ids come from: `GET /api/clustermgmt/v4.0/config/clusters`
  and `.../storage-containers`; the Keycloak client needs service accounts
  with the five `realm-management` roles of Task 3).
- `lab/portico/portico.yaml`: `store.path: /var/lib/portico/portico.db`,
  `prism_central.api_key_file: /etc/portico/prism-api-key`,
  `saml_idp_name: portico-lab-keycloak`, `keycloak.realm: portico-lab`,
  `admin_client_secret_file: /etc/portico/keycloak-admin-secret`,
  `tenants.project_prefix: portico-lab`, cluster
  `00065dbb-5b39-35c7-78ee-5254000e0b96`, container
  `206ecd5c-0ae4-44e7-bf94-dc7b0870f9e2` (from
  `docs/lab/payloads/resource-group-create.json`).
- `lab/portico/deploy.sh`: stage the key only (`cut -d= -f2-
  ~/portico-secrets/portico-sa-key`) and `portico-kc-admin-secret`, install
  both as `portico:portico` 0600; generate `~/portico-secrets/portico-admin-pw`
  (`openssl rand -base64 18 | tr -d '/+='`, 0600) when missing, stage it
  and run `sudo -u portico /usr/local/bin/portico admin set-password
  -config /etc/portico/portico.yaml < "$st/portico-admin-pw"` after the
  binary is installed and before the restart. The staging directory is
  removed as today.

- [ ] **Step 10: Deploy and check**

Run: `source lab/env.sh && lab/portico/deploy.sh && lab/auth/probe-local-login.sh`
Expected: `portico ready`; probe exit 0 (including any line added by Task
0); `curl --cacert ~/portico-secrets/lab-ca.crt --resolve prism.lab.portico.test:8443:10.23.210.190 https://prism.lab.portico.test:8443/api/v1/tenants`
→ 401 with `WWW-Authenticate`; with `-u admin:<pw from the file>` → `[]`.
`sudo journalctl -u portico` has no key, secret or password.

- [ ] **Step 11: Commit**

```bash
git add internal/config cmd/portico deploy lab/portico lab/test_scripts.sh
git commit -m "feat: Milestone 2 settings, serve wiring and lab deployment"
```

---

### Task 7: Lab acceptance, write-up 11, documentation

**Files:**
- Create: `lab/tenants/api.sh`, `lab/tenants/acceptance.sh`
- Create: `docs/lab/2026-10-18-11-tenant-arrival-acceptance.md`
- Modify: `docs/lab/README.md`, `docs/lab/lab-inventory.md`, `lab/README.md`,
  `README.md` (Status), `docs/superpowers/specs/2026-10-17-portico-design.md`
  (§9 row 2: "2a done <date>, 2b pending"), `deploy/portico.yaml.example`
  if anything changed

**Interfaces:**
- Consumes: the API of Task 5 on `https://prism.lab.portico.test:8443`
  (lab certificate, `--resolve` to the VM).

- [ ] **Step 1: Write `lab/tenants/api.sh METHOD PATH [JSON_FILE]`**

Like `lab/pc.sh`: curl config file (mode 0600) with
`user = "admin:<~/portico-secrets/portico-admin-pw>"`, `cacert`,
`resolve`, JSON headers, body to stdout and `HTTP <code>` to stderr. Never
prints the password.

- [ ] **Step 2: Write `lab/tenants/acceptance.sh`**

Steps, each printed as `got/expected`, exit 1 on mismatch:
1. `POST /api/v1/tenants {slug:"tenant-b", display_name:"Tenant B (lab)"}`
   → 202 (the lab prefix gives project `portico-lab-tenant-b`, Keycloak
   group `tenant-tenant-b`: accepted, the group name is derived, not
   pretty; note it in the write-up).
2. Poll `GET /api/v1/tenants/tenant-b` every 10 s up to 15 min until
   status is `active` or `failed` → expected `active`; print the steps.
3. `GET /visibility` → `ok: true`.
4. Prism Central read-back with `lab/pc.sh` as the service account:
   project `portico-lab-tenant-b` exists, one role membership for group
   `tenant-tenant-b` in it, IdP shared with it.
5. `POST /users {email:"carol@lab.portico.test", first_name:"Carol",
   last_name:"Lab"}` → 201; save `temporary_password` to
   `~/portico-secrets/carol-tmp-pw` (0600), never print it.
6. `POST /disable` → `disabled`; Keycloak member `carol` has
   `enabled=false` (via `lab/saml/kc.sh`); `POST /enable` → `active`.
7. Print `browser check pending` and stop here unless `--delete` is given;
   with `--delete`: `DELETE ...?delete_project=true` → 202, poll until 404,
   then Prism Central has no `portico-lab-tenant-b`, no `tenant-tenant-b`
   group, and `_internal` is unchanged (snapshot diff as in write-up 05).

- [ ] **Step 3: Run the acceptance without `--delete`**

Run: `source lab/env.sh && lab/tenants/acceptance.sh`
Expected: every line matches; `browser check pending`.

- [ ] **Step 4: Browser check (Chrome, manual by the executor)**

Open `https://prism.lab.portico.test/` in a fresh window: Keycloak login
as `carol@lab.portico.test` with the temporary password → Keycloak forces
a new password → Prism Central provider page → SAML button → console.
Expected: Projects shows only `portico-lab-tenant-b`; VMs is empty;
Clusters is 403/hidden. Record what was seen in the write-up.

- [ ] **Step 5: Run the acceptance with `--delete`**

Run: `lab/tenants/acceptance.sh --delete`
Expected: every line matches, including `_internal` unchanged.

- [ ] **Step 6: Write-up 11 and documentation**

`docs/lab/2026-10-18-11-tenant-arrival-acceptance.md` (template): what
ran, timings per step on the nested lab, the browser check, the re-measure
command. `docs/lab/README.md` row 11. `docs/lab/lab-inventory.md`: rows
for the Keycloak client `portico-admin` and its secret file, the
break-glass password file, the store on the VM, `carol` (created and
deleted by the acceptance), and the note that `tenant-b` objects exist
only while the acceptance runs. `lab/README.md`: the `lab/tenants/`
scripts. `README.md` Status: "Milestone 2a: tenants are created through
the API in the lab". Design §9 row 2 split into 2a (done) and 2b
(pending).

- [ ] **Step 7: Full verification**

Run: `go test ./... && go vet ./... && gofmt -l cmd internal && lab/test_scripts.sh`
Expected: all green.

- [ ] **Step 8: Commit**

```bash
git add lab/tenants docs/lab lab/README.md README.md docs/superpowers/specs deploy
git commit -m "lab: tenant arrival acceptance on NTNX-01 (write-up 11)"
```

---

## Self-review notes

- Spec coverage: §4.11 store behind an interface → Task 1 (`store.Store`,
  `SQLite` implementation; consumers depend on the interface only).
  §4.4 tenant row fields → Task 1 schema; idempotent steps,
  recording, deletion order, disable, first user with temporary password,
  static visibility → Task 4; project naming prefix → Task 4/6; live
  visibility check → not in this plan (spec allows "a button in the UI",
  Milestone 2b); SAML IdP creation by Portico → setup wizard, Milestone
  2b (this plan requires an existing IdP by name). §4.5 API key only →
  Task 2/6; bootstrap path → 2b. §4.6 SQLite WAL → Task 1; encryption at
  rest → 2b (no secret enters the store here). §4.7 API boundary → Task 5.
  §5 no global role → Task 4 constants and visibility findings.
- Interfaces: `store.Tenant` fields are the ones `tenant` writes and `api`
  serialises; `nutanix` method names match the `tenant.PrismCentral`
  interface verbatim; `keycloak` likewise.
- Review Focus 1–5 each name their task's test.
- Proportion: signatures and test names, no bodies.
