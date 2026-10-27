# Lab scripts

Shell helpers used for the Milestone 0 measurements on the lab cluster
NTNX-01. They are **lab only**: nothing here is product code, and nothing
here runs on the appliance.

- `env.sh` — source it first. Exports `PC_URL`, `PE_URL`, `PC_USER`,
  `PC_PASS`, `LAB_VM_IP`, `LAB_PRISM_HOST`, `LAB_ID_HOST`. Reads the password
  from `~/portico-secrets/prism-pw` and stops if the file is missing.
- `pc.sh METHOD PATH [JSON_FILE]` — calls the Prism Central API with Basic
  auth. Body on stdout, `HTTP <code>` on stderr. Adds `NTNX-Request-Id` on
  v4 writes and `If-Match` when `IF_MATCH` is set.
- `lib.sh` — shared helpers sourced by the other scripts: `wait_task`
  (with a timeout), `etag_of`, `remote_stage_dir` (private directory on the
  VM for files in transit).
- `test_env.sh` — checks `env.sh`; `test_scripts.sh` — static checks and
  shellcheck on every lab script.

- `portico/` — `deploy.sh` builds the Go binary, installs it on the lab VM
  as `portico.service` in place of nginx, waits for the health endpoint and
  prints the admin UI URL. Since Milestone 2d the VM runs from the store:
  `portico.store.yaml` (process settings only) is the live
  `/etc/portico/portico.yaml`; `portico.yaml` (runtime sections and secret
  file paths) is used once by `portico settings import` on the first
  deploy, after which the secret files are removed (write-up 14).
  `deploy.sh --fresh` (Milestone 2e) installs the binary with no secret
  file and no import, so the service starts in setup mode and is
  bootstrapped from the setup page or by `bootstrap-acceptance.sh`
  (the whole setup through the API, with timings).
  `reset-for-bootstrap.sh --yes` takes the lab back to the fresh state
  (store moved aside, IdP, service account and Keycloak realm deleted,
  stale secret files removed; prints its plan and refuses without
  `--yes`), write-up 15. `undeploy.sh` restores nginx;
  `portico-msp-logo.svg` is the lab MSP's branding logo: Portico's own mark in white, shown on the accent colour `#a73422` (branding name `Portico MSP`).
- `saml/keycloak-realm.sh oidc-client` registers Portico's OIDC client.
- `saml/theme-probe.sh [reset]` writes a probe login theme and sets it on the realm (write-up 13); `auth/probe-login-request.sh` prints the login-request shape, the provider-selection PUT and `initiate-login`.
- `tenants/api.sh METHOD PATH [JSON]` calls Portico's admin API on the lab VM with the break-glass password from `~/portico-secrets/portico-admin-pw`; `tenants/acceptance.sh [--delete]` is the Milestone 2a acceptance (write-up 11).
- `saml/keycloak-realm.sh admin-client` registers the confidential client `portico-admin` (service account with realm-management roles) that the tenant service uses; secret in `~/portico-secrets/portico-kc-admin-secret`.

Secrets rule: credentials live in `~/portico-secrets/` (mode 0600). Scripts
read them from there and never print, log, or copy them under the
repository.
- `appliance/deploy.sh --release V` puts the published release on NTNX-01, Prism Central downloading the image from GitHub; `appliance/deploy.sh [--run ID | --image PATH]` puts the image built by `.github/workflows/image.yml` there: artifact download, HTTP server on `LAB_IMAGE_HOST` for Prism Central's image service (no default: the lab VM that did it, `portico-lab` on `10.23.210.190`, was deleted on 2026-10-27, and `vm/create-vm.sh` makes it again), `deploy/tofu/nutanix` with the lab values (VM `portico-appliance`, `10.23.210.191`, cloud-init network and SSH key), the break-glass password from `~/portico-secrets-appliance/portico-admin-pw` (write-up 19); `appliance/destroy.sh` removes VM and image. The acceptance scripts take the appliance's names from the environment: `PORTICO_SECRETS`, `LAB_VM_IP`, `LAB_SSH_USER=admin`, `LAB_REALM`, `LAB_SERVICE_ACCOUNT`, `LAB_SAML_IDP`, `LAB_PROJECT_PREFIX`, `LAB_TENANT_*`.
- `portico/demo-tenants.sh` sets the demo up on an appliance whose setup is done: tenants acme (both sites) and globex (first site), users `carol@acme.example` and `dave@globex.example` with permanent passwords in `PORTICO_SECRETS/<slug>-user-pw`, small powered-on VMs in their projects; idempotent (write-up 33). `LAB_ADMIN_CA` gives `tenants/api.sh` a fresh appliance's own admin certificate, `LAB_LOGIN_EMAIL` and `LAB_LOGIN_PWFILE` sign `sites/tenant-login.sh` in as any tenant user, `LAB_IMAGE_USER` is the image host's user (default `lab`).
- `build-host/deploy.sh` creates an x86_64 VM with KVM on the lab's Proxmox (`portico-build`, `10.23.210.192` on VLAN 210) and provisions it for `image/build.sh` and the boot test; `--destroy` removes it (write-up 19).
