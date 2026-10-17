# Milestone 0: Lab Measurements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Answer the eight open questions in section 6 of the design on the
real lab (NTNX-01, pc.7.6), with reproducible scripts and one write-up per
question, so Milestone 1 code is built on measured facts, not guesses.

**Architecture:** A throwaway lab VM (`portico-lab`, Rocky 9) on NTNX-01 runs
Keycloak + PostgreSQL in podman and nginx as a stand-in reverse proxy with
two host names (`prism.lab.portico.test`, `id.lab.portico.test`). Small shell
scripts under `lab/` call the Prism Central API from the Mac; findings go in
`docs/lab/` as short MEASURED write-ups. Nothing from this milestone is
product code; the lab VM and nginx are discarded when the Go proxy exists.

**Tech Stack:** bash + curl + jq (Mac side), Rocky Linux 9 GenericCloud,
podman quadlets, Keycloak 26 container, PostgreSQL 16 container, nginx,
Prism Central v4 and v3 APIs, Chrome devtools or the Chrome automation tools
for request capture.

**Spec:** `docs/superpowers/specs/2026-10-17-portico-design.md`, section 6
(items 6.1–6.8) and section 3 (lab constraints).

## Global Constraints

- All text on disk in English (CLAUDE.md).
- Prism password read only from `~/portico-secrets/prism-pw` (mode 0600),
  user `admin`. Never echoed, logged, or written under the repo.
- Nutanix documentation read from `~/portico-kb/`; never committed.
- Lab: Prism Central `https://10.23.210.175:9440`, Prism Element
  `10.23.210.170`, VLAN 210 `10.23.210.0/24`, gateway `.254`, DNS
  `1.1.1.1`, free IPs from `.190`, verified with `ping`/`arping` before use.
- Lab VM budget: 2 vCPU, 6 GiB, 40 GiB (design §3 appliance target).
- Write-ups must state the measured Prism Central version and how to
  re-measure after an upgrade.
- Captured HTTP traffic (HAR files) is never committed: it contains cookies
  and tokens. Only endpoint lists and sanitised payloads are stored.
- Nothing created in the lab Prism Central is left behind without being
  listed in `docs/lab/lab-inventory.md` (projects, users, roles, IdP, VMs).

## Review Focus

Failure modes the spec implies but no task's verification exercises:

1. A script run without the secrets file prints a shell error that includes
   the path and continues with an empty password; expected: stop with a
   clear message, exit 1 (Task 1 test).
2. The lab VM IP is already in use by a device that does not answer ping;
   expected: `arping` check in addition to ping before creating the VM
   (Task 3 step).
3. The pc.7.6 "Default Project" gets modified by a test project operation;
   expected: all project tests target only `portico-lab-*` names and the
   write-up proves the Default Project is unchanged (Task 6 verification).
4. Prism Central's SAML configuration is overwritten while one already
   exists; expected: Task 5 reads the current IdP first and refuses to
   proceed if one exists that was not created by this lab.
5. The lab leaves `portico` service account with a global admin role;
   expected: Task 7 ends with the role reduced to the measured minimum and
   lists it in the inventory.

---

### Task 1: Lab tooling (`lab/` scripts and write-up template)

**Files:**
- Create: `lab/env.sh`
- Create: `lab/pc.sh`
- Create: `lab/README.md`
- Create: `docs/lab/TEMPLATE.md`
- Create: `docs/lab/lab-inventory.md`
- Test: `lab/test_env.sh`

**Interfaces:**
- Produces: `lab/env.sh` (sourced) exports `PC_URL=https://10.23.210.175:9440`,
  `PE_URL=https://10.23.210.170:9440`, `PC_USER=admin`, `PC_PASS` (from
  `~/portico-secrets/prism-pw`, trailing newline stripped),
  `LAB_VM_IP=10.23.210.190`, `LAB_PRISM_HOST=prism.lab.portico.test`,
  `LAB_ID_HOST=id.lab.portico.test`. Exits 1 with
  `lab/env.sh: secrets file not found or not readable` if the file is
  missing.
- Produces: `lab/pc.sh METHOD PATH [JSON_FILE]` → prints the response body
  to stdout and `HTTP <code>` to stderr; `-k` TLS; Basic auth with
  `$PC_USER:$PC_PASS`; `Content-Type: application/json`; for `POST`/`PUT`/
  `PATCH` with a v4 path it sends `NTNX-Request-Id: $(uuidgen)` and, if the
  env var `IF_MATCH` is set, `If-Match: $IF_MATCH`. The password is passed
  with `curl --config` from a temporary file (mode 0600, deleted on exit),
  never on the command line.

- [ ] **Step 1: Write the failing test `lab/test_env.sh`**

```bash
#!/usr/bin/env bash
# Runs env.sh with a fake HOME to check the failure path, then the real one.
set -u
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
out=$(HOME="$tmp" bash -c 'source lab/env.sh' 2>&1); rc=$?
[ "$rc" -eq 1 ] || { echo "FAIL: expected exit 1, got $rc"; exit 1; }
grep -q 'secrets file not found' <<<"$out" || { echo "FAIL: missing message"; exit 1; }
source lab/env.sh
[ -n "$PC_PASS" ] && [ "$PC_URL" = "https://10.23.210.175:9440" ] || { echo "FAIL: env"; exit 1; }
[[ "$PC_PASS" != *$'\n'* ]] || { echo "FAIL: newline in password"; exit 1; }
echo PASS
```

- [ ] **Step 2: Run it to verify it fails**

Run: `bash lab/test_env.sh`
Expected: `lab/env.sh: No such file or directory` and non-zero exit.

- [ ] **Step 3: Implement `lab/env.sh` and `lab/pc.sh` per the Interfaces block**

`env.sh` must be safe to `source` with `set -u` active and must not print
the password under `set -x` (wrap the read in `{ set +x; } 2>/dev/null`).

- [ ] **Step 4: Run the test and a live call**

Run: `bash lab/test_env.sh && source lab/env.sh && lab/pc.sh GET /api/prism/v4.0/config/pc 2>/dev/null | jq -r '.data.config.buildInfo.version'` (or the v3 fallback `lab/pc.sh GET /PrismGateway/services/rest/v1/cluster | jq -r .version` if the v4 path is not found)
Expected: `PASS` then a version string starting with `pc.7.6`.

- [ ] **Step 5: Write `docs/lab/TEMPLATE.md` and `docs/lab/lab-inventory.md`**

Template sections, in this order: `Question` (copy of the design item),
`Measured on` (PC version, date, who), `Method` (commands or clicks),
`Result` (facts, each prefixed `MEASURED:`), `Implications for the design`,
`How to re-measure after an upgrade`. Inventory: a table with columns
`object | name | created by task | cleanup`.

- [ ] **Step 6: `lab/README.md` and shellcheck**

README: what the scripts are, that they are lab-only, and the secrets rule.
Run: `shellcheck lab/*.sh`
Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add lab docs/lab
git commit -m "lab: add Prism Central API helpers and write-up template"
```

---

### Task 2: Prism Central inventory and API discovery (design 6.3 part 1, 6.8 part 1)

**Files:**
- Create: `lab/inventory.sh`
- Create: `docs/lab/2026-10-NN-01-pc-inventory.md` (NN = day executed)
- Modify: `docs/lab/lab-inventory.md`

**Interfaces:**
- Consumes: `lab/env.sh`, `lab/pc.sh`.
- Produces: `lab/inventory.sh` → writes `docs/lab/raw/inventory-<date>.json`
  (gitignored path `docs/lab/raw/`) and prints a summary; a write-up with
  the list of API families available on this PC.

- [ ] **Step 1: Download the public v4 OpenAPI specs into `~/portico-kb/openapi/`**

From `https://developers.nutanix.com/` (v4 API reference, "Download
OpenAPI"): `iam`, `prism`, `vmm`, `clustermgmt`, `networking`. Record in
the write-up which exact versions (e.g. `iam 4.0`, `vmm 4.0`) were found.
If downloads need a login, ask Marco to add them to `~/portico-kb`.

- [ ] **Step 2: Write `lab/inventory.sh`**

Calls, with `lab/pc.sh GET`, and stores each response under its own key:
`/api/prism/v4.0/config/pc` (version), `/api/clustermgmt/v4.0/config/clusters`,
`/api/iam/v4.0/authn/users`, `/api/iam/v4.0/authz/roles`,
`/api/iam/v4.0/authz/authorization-policies`,
`/api/iam/v4.0/authn/saml-identity-providers`,
`/api/iam/v4.0/authn/directory-services`, and the projects endpoint:
try `/api/prism/v4.0/config/projects` (Projects 2.0, check the kb for the
exact path), fall back to `POST /api/nutanix/v3/projects/list` with
`{"kind":"project"}`. Each call records the HTTP code; a 404 is a finding,
not an error.

- [ ] **Step 3: Run it**

Run: `source lab/env.sh && lab/inventory.sh`
Expected: a summary line per endpoint with its HTTP code; `docs/lab/raw/`
exists and is ignored by git (`git status --short` shows nothing under it).

- [ ] **Step 4: Write the inventory write-up**

Must state as `MEASURED:` the PC version, the number of clusters, which
endpoints answered 200 vs 404, the list of existing projects including the
"Default Project" and its UUID, whether a SAML IdP already exists, the
existing roles that look relevant (`Project Admin`, `Developer`,
`Consumer`, `Operator` or their 7.6 names), and whether `/api/iam/v4.0`
exposes a projects or "project membership" resource. Add every pre-existing
object to `docs/lab/lab-inventory.md` with `created by: pre-existing`.

- [ ] **Step 5: Commit**

```bash
echo 'docs/lab/raw/' >> .gitignore
git add .gitignore lab/inventory.sh docs/lab
git commit -m "lab: inventory of the lab Prism Central and v4 API discovery"
```

---

### Task 3: Lab VM `portico-lab` on NTNX-01

**Files:**
- Create: `lab/vm/cloud-init.yaml`
- Create: `lab/vm/create-vm.sh`
- Create: `lab/vm/delete-vm.sh`
- Create: `docs/lab/2026-10-NN-02-lab-vm.md`

**Interfaces:**
- Consumes: `lab/env.sh`, `lab/pc.sh`, `~/.ssh/id_rsa.pub`.
- Produces: a running VM reachable as `ssh lab@10.23.210.190`; nginx not yet
  configured; podman installed. `create-vm.sh` is idempotent (exits 0 with
  `already exists` if a VM named `portico-lab` exists).

- [ ] **Step 1: Check the IP is free**

Run: `ping -c 2 -W 1 10.23.210.190; sudo arping -c 2 -I <mac-interface> 10.23.210.190` (if `arping` is not on the Mac, run it from the Prism Element CVM via SSH or pick the check Marco prefers).
Expected: no reply from either. If a reply arrives, choose the next free IP, update `LAB_VM_IP` in `lab/env.sh`, and note it in the write-up.

- [ ] **Step 2: Write `lab/vm/cloud-init.yaml`**

User `lab` with the public key from `~/.ssh/id_rsa.pub` injected by
`create-vm.sh` (the YAML holds a `__SSH_KEY__` placeholder), passwordless
sudo, static network for `eth0` (`LAB_VM_IP/24`, gateway `.254`, DNS
`1.1.1.1 1.0.0.1`), hostname `portico-lab`, packages `podman nginx
policycoreutils-python-utils`, `timezone: Europe/Rome`.

- [ ] **Step 3: Write `lab/vm/create-vm.sh`**

Sequence, each step via `lab/pc.sh` with v4 endpoints (exact paths from the
kb/OpenAPI; v3 fallback noted in the write-up if a v4 call is missing):

1. Image: `POST /api/vmm/v4.0/content/images` with source
   `https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud-Base.latest.x86_64.qcow2`,
   name `rocky-9-genericcloud`; skip if an image with that name exists.
   Poll the returned task (`/api/prism/v4.0/config/tasks/{extId}`) until
   `SUCCEEDED`.
2. Subnet: find the VLAN 210 subnet extId via
   `/api/networking/v4.0/config/subnets`.
3. VM: `POST /api/vmm/v4.0/ahv/config/vms` with name `portico-lab`, 2 vCPU,
   6 GiB, one disk cloned from the image, resized to 40 GiB, one NIC on the
   subnet, `guestCustomization` cloud-init with the rendered YAML
   (base64). Poll the task.
4. Power on: `POST /api/vmm/v4.0/ahv/config/vms/{extId}/$actions/power-on`
   with the `If-Match` ETag from a fresh `GET`.

`delete-vm.sh`: power off, delete VM, keep the image.

- [ ] **Step 4: Run it and wait for SSH**

Run: `source lab/env.sh && lab/vm/create-vm.sh && until ssh -o ConnectTimeout=3 lab@$LAB_VM_IP 'cat /etc/rocky-release'; do sleep 10; done`
Expected: `Rocky Linux release 9.x (...)`. Then `ssh lab@$LAB_VM_IP 'podman --version && nginx -v'` prints both versions.

- [ ] **Step 5: Write-up and inventory**

Write-up records as `MEASURED:` which v4 calls worked, which needed a v3
fallback, image upload time, boot-to-SSH time. Add `VM portico-lab` and
`image rocky-9-genericcloud` to `docs/lab/lab-inventory.md` with
`cleanup: lab/vm/delete-vm.sh`.

- [ ] **Step 6: Commit**

```bash
git add lab/vm docs/lab
git commit -m "lab: create the portico-lab VM on NTNX-01 via v4 API"
```

---

### Task 4: Keycloak and nginx stand-in on the lab VM

**Files:**
- Create: `lab/vm/keycloak.container`, `lab/vm/postgres.container`
  (podman quadlets), `lab/vm/keycloak.env.example`
- Create: `lab/vm/nginx.conf`
- Create: `lab/vm/provision.sh` (copies files to the VM over SSH, generates
  the self-signed cert, enables services)
- Create: `docs/lab/2026-10-NN-03-keycloak-nginx.md`

**Interfaces:**
- Produces: `https://id.lab.portico.test` → nginx → Keycloak on the VM
  (`127.0.0.1:8080`); `https://prism.lab.portico.test` → nginx →
  `https://10.23.210.175:9440` with `proxy_set_header Host $host`, no auth
  filter yet. Keycloak realm `portico-lab`, admin user `kcadmin`, password
  generated by `provision.sh` into `~/portico-secrets/keycloak-admin-pw`
  (mode 0600). Self-signed cert with SANs for both host names, saved to
  `~/portico-secrets/lab-ca.crt` for importing into the Mac keychain.
- Mac side: `/etc/hosts` lines `10.23.210.190 prism.lab.portico.test
  id.lab.portico.test` (Marco adds them; the write-up says so).

- [ ] **Step 1: Write the quadlets and `nginx.conf`**

Keycloak image `quay.io/keycloak/keycloak:26.3` with `start --hostname
https://id.lab.portico.test --proxy-headers xforwarded --http-enabled true`
and env `KC_DB=postgres`, `KC_DB_URL=jdbc:postgresql://postgres:5432/keycloak`,
`KC_BOOTSTRAP_ADMIN_USERNAME=kcadmin`, `KC_BOOTSTRAP_ADMIN_PASSWORD` from the
env file. PostgreSQL image `docker.io/library/postgres:16` with a named
volume. Both in one podman network `portico-lab`. nginx: two `server`
blocks, TLS on 443 with the self-signed cert, HTTP/1.1 upgrade headers on
the Prism block for WebSockets (`proxy_http_version 1.1`,
`Upgrade`/`Connection` headers), `proxy_ssl_verify off` towards 9440.

- [ ] **Step 2: Write `provision.sh`**

Generates the password and the certificate locally, copies everything with
`scp`, runs `systemctl --user` or system quadlets (system, under
`/etc/containers/systemd/`), sets `setsebool -P httpd_can_network_connect
1`, `firewall-cmd --add-service=https --permanent`, and waits for
`https://id.lab.portico.test/realms/master` to return 200.

- [ ] **Step 3: Run it**

Run: `source lab/env.sh && lab/vm/provision.sh`
Expected: last line `keycloak ready`; from the Mac, after adding the hosts
lines and trusting `lab-ca.crt`, `curl -s -o /dev/null -w '%{http_code}'
https://id.lab.portico.test/realms/master` → `200` and
`https://prism.lab.portico.test/console/` → `200` with the Prism login page
HTML.

- [ ] **Step 4: Write-up (includes design item 6.6, resource cost, first sample)**

`MEASURED:` memory used on the VM at idle (`free -m`, `podman stats
--no-stream`) with Keycloak started and after a login to its admin console;
which SELinux booleans were needed; Keycloak and PostgreSQL versions.

- [ ] **Step 5: Commit**

```bash
git add lab/vm docs/lab
git commit -m "lab: Keycloak and nginx stand-in with two public host names"
```

---

### Task 5: SAML between Prism Central and Keycloak behind a different host name (design 6.1, 6.8)

**Files:**
- Create: `lab/saml/keycloak-realm.sh` (creates realm `portico-lab`, group
  `tenant-a`, user `alice@lab.portico.test`, SAML client for Prism Central
  from PC's metadata)
- Create: `lab/saml/pc-idp.sh` (reads/creates the SAML IdP in Prism Central
  through the API; refuses if an IdP not named `portico-lab-keycloak`
  exists)
- Create: `docs/lab/2026-10-NN-04-saml-hostname.md`
- Create: `docs/lab/payloads/saml-idp-create.json` (sanitised)

**Interfaces:**
- Consumes: Task 4 services, `lab/pc.sh`, Keycloak admin REST API
  (`/admin/realms/...`) with a token from `kcadmin`.
- Produces: the measured answer to "does SAML survive a proxy with a
  different host name", and the exact v4 endpoint and payload to create a
  SAML IdP in pc.7.6.

- [ ] **Step 1: Read PC's SAML metadata through both names**

Run: `curl -sk https://10.23.210.175:9440/api/iam/v4.0/authn/saml-identity-providers/$metadata` style endpoint (exact path from the kb; older PCs expose `/api/iam/aiops/.../metadata`), once directly and once via `https://prism.lab.portico.test/...`.
Expected: an XML `EntityDescriptor`. `MEASURED:` the `AssertionConsumerService` `Location` host in each case (is it the IP, or the `Host` header's name?).

- [ ] **Step 2: Create the Keycloak side with `keycloak-realm.sh`**

SAML client with `clientId` = PC's `entityID`, ACS URL = the one from the
metadata read **through the public name**, NameID format `email`, "sign
assertions" on, "force POST binding" off (PC wants redirect for the SSO
URL; the response is POSTed to the ACS). Group `tenant-a`; user `alice`
in it, password set non-temporary.

- [ ] **Step 3: Create the IdP in PC with `pc-idp.sh` (6.8)**

First `GET` the IdP list; if non-empty and the name is not
`portico-lab-keycloak`, print `refusing: an identity provider already
exists` and exit 2. Otherwise `POST` with Keycloak's realm SAML metadata
URL or XML (`https://id.lab.portico.test/realms/portico-lab/protocol/saml/descriptor`),
name `portico-lab-keycloak`, username attribute `email`. Save the sanitised
payload to `docs/lab/payloads/saml-idp-create.json`.

- [ ] **Step 4: Measure the login through the public name**

In Chrome (devtools, Network, "Preserve log"), open
`https://prism.lab.portico.test/console/`, choose the SAML login, log in as
`alice`. Repeat starting from `https://10.23.210.175:9440/console/`.
Expected to record, `MEASURED:` for each: did it reach the console; the
sequence of redirects (host names only); the ACS URL the browser POSTed to;
any error page text. If the public-name login fails because the ACS host is
the IP, try the PC setting for the external name (kb: "Prism Central
FQDN"/"virtual IP"/"external hostname") and re-measure.

- [ ] **Step 5: Map `tenant-a` to a project role and check visibility (first pass)**

Create project `portico-lab-tenant-a` from the PC UI (the API version is
Task 6), add the SAML group `tenant-a` with role `Project Admin` (or the
7.6 equivalent found in Task 2) scoped to that project. Log in as `alice`
through the public name. `MEASURED:` what the left menu shows, whether
clusters/hosts/other projects are visible, and the list of API paths the
console called during the first minute (copy from devtools, hosts and paths
only, into the write-up).

- [ ] **Step 6: Write-up, inventory, commit**

Add `realm portico-lab`, `IdP portico-lab-keycloak`, `project
portico-lab-tenant-a`, `user alice` to the inventory.

```bash
git add lab/saml docs/lab
git commit -m "lab: measure SAML login through a proxy with a different host name"
```

---

### Task 6: Projects 2.0 API surface (design 6.3)

**Files:**
- Create: `lab/projects/create.sh`, `lab/projects/bind-group.sh`,
  `lab/projects/delete.sh`
- Create: `docs/lab/payloads/project-create.json`,
  `docs/lab/payloads/authorization-policy-create.json`
- Create: `docs/lab/2026-10-NN-05-projects2-api.md`

**Interfaces:**
- Consumes: `lab/pc.sh`, endpoints discovered in Task 2 and the Projects
  Guide pc.7.6 from `~/portico-kb`.
- Produces: the exact calls Milestone 2 will wrap: create project with
  cluster allocation and a per-cluster quota (2 vCPU, 4 GiB, 50 GiB), bind
  a SAML group to a role scoped to the project, read back, delete.

- [ ] **Step 1: Snapshot the Default Project**

Run: `source lab/env.sh && lab/pc.sh GET <projects endpoint> | jq '.data[] | select(.name=="Default Project")' > docs/lab/raw/default-project-before.json`
Expected: a JSON object with the Default Project's extId.

- [ ] **Step 2: Write and run `create.sh` for `portico-lab-tenant-b`**

Expected: HTTP 202 with a task, then the project visible in the UI under
Projects with the quota shown. `MEASURED:` endpoint, required fields,
whether cluster allocation and quota are in the same call or separate
ones, ETag/`If-Match` behaviour on update.

- [ ] **Step 3: Write and run `bind-group.sh`**

Bind SAML group `tenant-b` (create it in Keycloak first with
`lab/saml/keycloak-realm.sh` extended by a `--group` flag) to the project
role. `MEASURED:` whether this is an `authorization-policies` call with an
entity filter on the project, or a project-membership call; the payload.

- [ ] **Step 4: Verify isolation from the API side**

Run: as `alice` (tenant-a, via a SAML session is not scriptable; use a
local user `portico-lab-bob` mapped the same way as tenant-b for this
check): list VMs and projects. `MEASURED:` what a project-scoped user gets
from `/api/vmm/v4.0/ahv/config/vms` and from the projects list.

- [ ] **Step 5: Delete and compare**

Run: `lab/projects/delete.sh portico-lab-tenant-b` then re-snapshot the
Default Project and `diff` with the "before" file.
Expected: project gone, `diff` empty. Record `MEASURED: Default Project
unchanged`.

- [ ] **Step 6: Write-up, inventory, commit**

```bash
git add lab/projects docs/lab
git commit -m "lab: measure Projects 2.0 create, bind, delete on pc.7.6"
```

---

### Task 7: Local login endpoints, local-auth fallback, and the `portico` service account (design 6.2, 6.5, 6.7)

**Files:**
- Create: `lab/auth/probe-local-login.sh`
- Create: `lab/auth/create-service-account.sh`
- Create: `docs/lab/2026-10-NN-06-local-login-and-fallback.md`
- Create: `docs/lab/2026-10-NN-07-service-account-role.md`

**Interfaces:**
- Produces: the deny-list for Milestone 1 (exact methods + paths that carry
  local credentials on pc.7.6, plus the `Basic` header rule), and the
  minimal role definition for the `portico` account.

- [ ] **Step 1: Capture a local login from the browser**

Devtools, Preserve log, open `https://prism.lab.portico.test/console/`, log
in as `admin` locally, log out. `MEASURED:` every request whose body or
headers carried the username or password (method, path, content-type),
and the session cookie names set in response.

- [ ] **Step 2: Write `probe-local-login.sh`**

For each path found, plus the historical
`POST /PrismGateway/j_spring_security_check` and `GET
/api/nutanix/v3/users/me` with Basic auth, send the request through the
public name and print `METHOD PATH -> HTTP code`. Expected now (no filter
yet): 200/302 for the real ones, 404 for retired ones. This script becomes
Milestone 1's acceptance test: after the Go proxy is in place every line
must be `403`.

- [ ] **Step 3: Fallback when the IdP is down (6.5)**

Run: `ssh lab@$LAB_VM_IP sudo systemctl stop keycloak` then open the PC
login page through the public name and try the SAML button, then the local
form. `MEASURED:` what the page shows, whether local login still works
(expected yes: this is exactly why the proxy must block it), and any
timeout. Restart Keycloak.

- [ ] **Step 4: Service account (6.7)**

`create-service-account.sh`: create local user `portico` via
`/api/iam/v4.0/authn/users` with a generated password saved to
`~/portico-secrets/portico-sa-pw` (0600); try a custom role via
`/api/iam/v4.0/authz/roles` with the operations needed (project create/
read/update/delete, authorization-policy create/read/delete, SAML IdP
read/create, user create/read for the probe account). Then, using
`PC_USER=portico`, re-run `lab/projects/create.sh` and `delete.sh` and
`lab/saml/pc-idp.sh` in read mode. `MEASURED:` which operations succeed
with the custom role, which require a built-in role, and the smallest
built-in role that works (e.g. `Prism Admin` vs `Super Admin`). End with
the account holding only that minimum.

- [ ] **Step 5: Write-ups, inventory, commit**

```bash
git add lab/auth docs/lab
git commit -m "lab: measure local login endpoints, IdP fallback, and the service account role"
```

---

### Task 8: Tenant UI behaviour and WebSockets through the proxy (design 6.4)

**Files:**
- Create: `docs/lab/2026-10-NN-08-tenant-ui-behaviour.md`
- Create: `docs/lab/payloads/tenant-console-paths.txt` (hosts and paths
  only, one per line, deduplicated)

- [ ] **Step 1: Capture a tenant session**

Log in as `alice` through the public name; open the VM list, create a VM
in project `portico-lab-tenant-a` from image `rocky-9-genericcloud` (1
vCPU, 1 GiB), open its VM console, type one command, close it, delete the
VM. Export the request list (not the HAR) to `tenant-console-paths.txt`.

- [ ] **Step 2: Classify**

`MEASURED:` how many distinct path prefixes the console used (`/console`,
`/api/nutanix/v3`, `/api/*/v4.0`, `/PrismGateway`, websockets under which
path), whether the VM console WebSocket went through nginx without extra
settings, and whether an allow-list of prefixes is realistic for Milestone
1 or only a deny-list is (design 4.3 step 3).

- [ ] **Step 3: Write-up, commit**

```bash
git add docs/lab
git commit -m "lab: measure tenant console behaviour and WebSockets through the proxy"
```

---

### Task 9: Fold the findings back into the design

**Files:**
- Modify: `docs/superpowers/specs/2026-10-17-portico-design.md` (sections
  3, 4.3, 4.4, 4.5, 6)
- Create: `docs/adr/0001-pc-7-6-projects-2-baseline.md`
- Create: `docs/adr/0002-proxy-in-go-binary.md`
- Create: `docs/lab/README.md` (index of write-ups with one-line results)

- [ ] **Step 1: Update the design**

For each item in section 6, replace the question with a one-line
`MEASURED:` summary and a link to the write-up; move any item that stays
open to a new "Still open" list. Update 4.3 step 5 (does Portico need to
auto-start SAML?), 4.4 (exact Projects 2.0 fields the tenant row needs),
4.5 (the minimal role).

- [ ] **Step 2: ADRs**

ADR 0001 records the pc.7.6 / Projects 2.0 baseline with the measured
facts; ADR 0002 records the proxy-in-Go decision and what the lab showed
about WebSockets and path families. Format: Context, Decision,
Consequences, Status.

- [ ] **Step 3: Verify nothing Italian or secret slipped in**

Run: `git grep -nE 'perch[eé]|quindi|anche' -- docs/lab docs/adr docs/superpowers lab; git grep -nE '[Pp]assword *[:=] *[^$ ]' -- lab docs`
Expected: no output from either.

- [ ] **Step 4: Commit and tag**

```bash
git add docs
git commit -m "docs: fold Milestone 0 lab findings into the design and ADRs"
git tag m0-lab-measurements
```

Milestone 1 planning starts from this tag.
