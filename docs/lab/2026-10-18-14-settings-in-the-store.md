# 14 — Settings in the store, master key, apply as restart (Milestone 2d)

## Question

Design §4.6 and §4.7: the runtime settings (public names, Prism Central
connection and service-account key, Keycloak realm and admin client, OIDC
client, tenant naming and placement) move from the YAML file and its
secret files into the store, sealed with a master key the service creates
at its first start. Does an installation move over without a flag day
(`portico settings import`), does the setup page edit and check them, does
"Apply and restart" bring the service back on the new document, and what
happens when the master key is wrong?

## Measured on

- Prism Central pc.7.6.0.6, Keycloak 26.3.5 on the lab VM
- Portico `dev-b4a7303` (branch `m2d-settings`), Rocky Linux 9.8,
  systemd 252
- Date: 2026-10-18
- By: Claude (Marco's session), `lab/portico/deploy.sh`, Chrome on the
  admin UI, `lab/tenants/api.sh`

## Method

1. `source lab/env.sh && lab/portico/deploy.sh`: the deploy now installs
   `lab/portico/portico.store.yaml` (process settings only) as
   `/etc/portico/portico.yaml`, and when `portico settings show` finds
   nothing stored it installs the full `lab/portico/portico.yaml` and the
   three secret files for one `portico settings import`, then removes them.
2. `lab/auth/probe-local-login.sh`; `lab/tenants/api.sh GET /api/v1/settings`.
3. Chrome, `https://prism.lab.portico.test:8443/setup` as `admin`: Check
   on each card, operation timeout 15m → 12m, Save, Apply and restart.
4. `lab/tenants/acceptance.sh && lab/tenants/acceptance.sh --delete`
   (Milestone 2a acceptance on the store-based service).
5. Wrong key: `systemctl stop portico`, move `/var/lib/portico/master.key`
   aside, `systemctl start portico`, read the journal; put the key back.
6. Branding logo: `PUT /api/v1/settings/branding` with
   `lab/portico/portico-lab-logo.svg` as `logo_data`.
7. After the review fixes (binary `dev-c04e264` plus the fix commit):
   `POST /api/v1/tenants` (tenant-b), then `POST /api/v1/settings/apply`
   at once; poll the tenant to `active`; apply again; poll `/healthz`;
   `DELETE /api/v1/tenants/tenant-b?delete_project=true`.

## Result

- MEASURED: the first deploy printed `settings imported into the store
  (sealed with the master key)` once; the second run of the same deploy
  prints `settings already in the store; import skipped`. After the
  deploy `/etc/portico/` holds `portico.yaml` only (no secret file), and
  `/var/lib/portico/` holds `master.key` (32 bytes, `portico:portico`
  0600, created by the binary), `portico.db` and its WAL files.
- MEASURED: the journal says `settings source=store` at start; the
  live YAML has no `api_key_file`, `admin_client_secret_file` or
  `client_secret_file`; `GET /api/v1/settings` answers `stored: true,
  source: store` with `api_key_set`, `admin_client_secret_set` and
  `client_secret_set` all true and no secret value or `v1:` blob in the
  body. `probe-local-login.sh` PASS: nothing changed on the public path.
- MEASURED: the setup page prefills every card from the store with the
  secret inputs empty and the placeholder "unchanged". "Check" on Prism
  Central answered in about a second: `1 cluster(s), 3 storage
  container(s)` (the two `Nutanix*` system containers are left out) and
  turned the cluster and container inputs of the Tenants card into
  pickers with the stored values selected. The Keycloak admin client and
  OIDC checks answered green. Saving the Tenants card with `12m` redirected
  to the page; the API showed `operation_timeout: 12m0s`.
- MEASURED: "Apply and restart" rendered the waiting page; the journal
  shows `settings apply: restart requested` → `restarting on request` →
  `exiting for the restart` → systemd `Deactivated successfully`,
  `Scheduled restart job, restart counter is at 1`, `Started` two seconds
  later, `settings source=store`, `listening admin`. The waiting page
  switched to "Portico is back. Log in again" at its next poll (about
  10 s after the click, including the 2 s `RestartSec`). After the
  restart the API still reports `12m0s`; the Milestone 2a acceptance
  passed on the restarted service, including the cleanup.
- MEASURED: with the master key moved aside each start fails with
  `settings: prism_central.api_key cannot be opened with this master key:
  secrets: cannot open the sealed value (wrong master key or damaged
  data)` and exit code 1; `Restart=always` retried every 2 s until
  systemd's default start limit (5 starts in 10 s) stopped it: `Start
  request repeated too quickly`, unit `failed`. Putting the key back and
  running `systemctl start portico` is refused until
  `systemctl reset-failed portico`; after that the service is `active`
  and `/healthz` answers 200. No plain-text fallback, no default values.
- MEASURED: the branding logo uploaded through the API is served on the
  public path as `image/svg+xml` with no warning (the Keycloak theme was
  regenerated on the save).
- MEASURED (defect found and fixed in the same task): a check request
  with the target only (`{"target":"keycloak"}`) used empty fields
  instead of the stored ones and answered `refused the admin client
  secret for "" in realm ""`. The candidate now falls back to the stored
  value for every empty field; test
  `TestSettingsCheckUsesStoredFieldsWhenOmitted`.
- MEASURED (after the review fixes): Apply while tenant-b was
  provisioning answered `409 {"error":"1 tenant operation(s) running:
  retry when they finish"}` and nothing restarted; once the tenant was
  `active`, Apply answered 202 and `/healthz` was back within 4 s. The
  earlier plan wording ("bounded by TimeoutStopSec") was wrong: a
  voluntary exit waits for the operation with no bound, so the apply is
  refused instead.
- Fixed in the review pass, with tests: the check sends the stored
  service-account key only to the stored Prism Central host (another URL
  must carry its own key: `TestSettingsCheckRefusesStoredKeyForAnotherHost`);
  `tls_verify` omitted from a check body keeps the stored value instead
  of becoming false (`TestSettingsCheckKeepsTLSVerifyWhenOmitted`); a
  `PUT` without secrets on a file-based installation adopts the file's
  secrets as the UI does (`TestSettingsPutAdoptsFileSecrets`); `settings
  show` never creates the master key and `settings import` refuses to
  run as root, so the key always belongs to the service user
  (`TestSettingsShowDoesNotCreateKey`, `TestSettingsImportRefusesRoot`).
- MEASURED: the lab storage container `206ecd5c-…` is named
  `objectsl889411dccd9a44125aa3e31c2596dcf9` in Prism Central (the YAML
  comment called it the default container; corrected).

## Implications for the design

- The move from file to store works without a flag day: the same binary
  serves either, the import is one command, the YAML afterwards holds
  process settings only. Design §4.7 stands.
- A wrong or missing master key stops the service for good, as §4.6
  wants, but the operator must know two things: the error names the
  field, and systemd's start limit leaves the unit `failed` after ten
  seconds of retries, so recovery is "put the key back, `systemctl
  reset-failed portico`, `systemctl start portico`". Recorded in the
  example YAML and in §4.6.
- The restart page cannot tell the new process from the old through
  `/healthz` (both answer 200) and the admin page's CSP blocks inline
  scripts, so it polls `/setup/ready`, an unauthenticated fragment that
  reports the restarting flag of the process that answers.

## How to re-measure after an upgrade

`source lab/env.sh && lab/portico/deploy.sh` (twice: import, then skip);
`lab/auth/probe-local-login.sh`; `lab/tenants/api.sh GET /api/v1/settings`;
the setup page in a browser as above; `lab/tenants/acceptance.sh &&
lab/tenants/acceptance.sh --delete`; the wrong-key sequence of the
method on the VM, ending with `systemctl reset-failed portico`.
