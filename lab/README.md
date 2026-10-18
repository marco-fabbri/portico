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
  `undeploy.sh` restores nginx; `portico-lab-logo.svg` is the lab MSP's
  branding logo.
- `saml/keycloak-realm.sh oidc-client` registers Portico's OIDC client.
- `saml/theme-probe.sh [reset]` writes a probe login theme and sets it on the realm (write-up 13); `auth/probe-login-request.sh` prints the login-request shape, the provider-selection PUT and `initiate-login`.
- `tenants/api.sh METHOD PATH [JSON]` calls Portico's admin API on the lab VM with the break-glass password from `~/portico-secrets/portico-admin-pw`; `tenants/acceptance.sh [--delete]` is the Milestone 2a acceptance (write-up 11).
- `saml/keycloak-realm.sh admin-client` registers the confidential client `portico-admin` (service account with realm-management roles) that the tenant service uses; secret in `~/portico-secrets/portico-kc-admin-secret`.

Secrets rule: credentials live in `~/portico-secrets/` (mode 0600). Scripts
read them from there and never print, log, or copy them under the
repository.
