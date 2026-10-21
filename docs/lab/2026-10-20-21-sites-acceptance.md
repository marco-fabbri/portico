# 21 — Sites accepted on the appliance with one site

## Question

Milestone 4, Task 10: does the sites release upgrade the appliance in
place, turn its single Prism Central into a site, keep the tenant on
it, and behave as the sites design says with one site (no site page,
maintenance stops new entries only, the MSP sees and can cut the
sessions left, the public path still refuses local logins, a tenant can
be created and deleted on the site)? And D4, left open by write-up 20:
how often an open console polls, which decides whether an open tab
counts as an "active" user.

## Measured on

- Prism Central: pc.7.6.0.6 (NTNX-PC-01 on NTNX-01, AOS 7.6), the same
  day it came back from the power loss of write-up 20
- Portico: the appliance `portico-appliance` (10.23.210.191), upgraded
  from `dev-290a6a1` (main) to `dev-451bb8e`, then `dev-af25247` (the
  probe fix below)
- Date: 2026-10-20
- By: Claude, with the scripts below; the console tab for D4 was
  Marco's browser (Chrome, the tab hidden behind another one), logged in
  as the probe user of `acme`

## Method

- `lab/portico/upgrade-appliance.sh` builds the checkout, stops
  Portico, copies the store and the master key to
  `/var/lib/portico/backup-<version>-<time>/` on the appliance itself,
  and re-runs `deploy/install.sh --binary` (design 4.9).
- `lab/sites/acceptance.sh [--with-tenant]` runs the checks below
  against the admin API and the public name, one line each, got
  (expected). With `--with-tenant` it ends with
  `lab/tenants/acceptance.sh` and `--delete` on the site, tenant `beta`.
- `lab/sites/probe-latency.sh` times the calls a site probe could use
  (clusters list, the same with `$limit=1`, domain managers, IAM users).
- `lab/sites/console-polling.sh MINUTES` reads Portico's access log on
  the appliance (paths, status and times only) for the requests with a
  session on the public name while one console tab stays untouched;
  the same journal, from the login to Prism Central's sign-out, for the
  whole session.
- Prism Central's task list (`/api/prism/v4.0/config/tasks`) for the
  failed resource-group deletions.

## Result

- MEASURED: the upgrade. The installer kept every secret and the store;
  Portico logged "the single Prism Central of the stored document is now
  site site-1" and served at once. `GET /api/v1/sites` shows `site-1`,
  code `SITE-1`, name `10.23.210.175`, the key set, the IdP
  `portico-keycloak`, the old tenant defaults as the site's cluster and
  container, and tenant `acme`. `acme` has one active placement on
  `site-1` with the project, resource group, SAML group and role
  membership it had before (project extId `40211c14-…`, found by name in
  Prism Central). `saml_entity_id` is empty: the legacy site keeps the
  identity provider created before variant A. A clean stop folds the WAL
  into the store, so the backup is two files. Side effect: the
  installer's `dnf install` also upgraded cloudflared to 2026.10.2.
- MEASURED: the probe, as first shipped (one failed probe makes a site
  unreachable, 5 s timeout), flapped for three and a half minutes after
  the restart: five of eight probes timed out, the site was
  `unreachable` twice. In the same minutes the site check
  (clusters and containers) took 30.9 s (timeout), 9.3 s, 1.2 s; ten
  minutes later every candidate call answered in 0.5 to 0.8 s, the
  clusters list included. With one site, an unreachable site means the
  tenant gets the "site unreachable" page from a Prism Central that is
  only slow (whether the console itself was slow in those minutes was
  not measured). Fixed in `af25247`: three failed probes in a row
  (about 90 s) make a site unreachable, one answer makes it available,
  the timeout is 10 s.
- MEASURED: `lab/sites/acceptance.sh` on `dev-af25247`, every line ok:
  the code renamed to `AZ-IT-MI1` and applied (restart, `available`);
  the probe user's login reaches `/console/` with no site page; with
  maintenance on, the sessions list shows the probe session `active`, a
  new login gets the "Site in maintenance" page (503), the session
  already on the site keeps getting the console (200); "disconnect"
  answers `{"disconnected": n}` for the n sessions listed and the cut
  session's next request gets the maintenance page; maintenance off, a
  login reaches the console again.
- MEASURED: `lab/auth/probe-local-login.sh`, 17 requests, every one as
  expected once the script takes the appliance's realm (`portico`) and
  checks that the pass-through, on the loopback by default, does not
  answer on the appliance's address (connection refused).
- MEASURED: the tenant cycle on `site-1`. Tenant `beta` was provisioned
  in 11 s (seven steps), visibility ok on the site, project, role
  membership and IdP sharing read back from Prism Central, its user
  created, disabled and enabled. The deletion failed twice at the
  resource group: Prism Central's gateway answered
  "504 upstream request timeout" after 57 and 63 s, and its task said
  why, `failed to delete Nutanix Resource Group as it is not empty.:
  10018`, although the project had never held anything. The placement
  went `remove-failed`, the tenant `failed` with the reason on the page.
  Each retry skipped what was already gone ("already absent") and
  resumed at the resource group; the third, ten minutes after the role
  membership was deleted, removed everything (tenant row, project, SAML
  group, Keycloak group; `_internal` unchanged). On 2026-10-18 the same
  deletion took 28 s. The deletion code is unchanged since main.
- D4: see "Console polling" below.

## Console polling (D4)

- MEASURED: one console tab, hidden, no input, from the login (04:44:16
  UTC) to Prism Central's idle sign-out (05:00:52): 340 requests with a
  session on the public name, 205 of them in the first minute (the
  console loading), then 2 to 16 a minute and never a minute without
  one. The longest gap between two requests was 61.0 s over the whole
  session; 53.1 s in the five minutes read by `console-polling.sh 5`
  (04:45:22 to 04:50:22, 40 requests).
- What polls: tasks (`/api/prism/v4.1` and `v4.3/config/tasks`,
  `/PrismGateway/services/rest/v1/progress_monitors`) and alerts
  (`/api/monitoring/v4.3/serviceability/alerts`) every 30 s for the first
  three minutes, then every 60 to 70 s; the entity lists
  (`/api/nutanix/v3/groups`, `/api/nutanix/v3/search`) every 90 s. The
  slowdown may be Chrome's throttling of hidden tabs (at most one timer
  wake-up a minute after five minutes hidden) or the console's own
  back-off; one run does not tell them apart.
- MEASURED: Prism Central counts user input, not the console's polling.
  It showed its "Session timeout warning" and, with nothing clicked,
  signed the session out at 05:00:52, 16.5 minutes after the console
  loaded (04:44:21). The console then called `/api/nutanix/v3/logout`
  and Prism Central's `/redirect`, which Portico sends to
  `/.portico/logout`: Portico deleted its session, expired Prism
  Central's cookies and ended the Keycloak session (Keycloak's logout
  answered in 5.4 s); the browser was on Portico's login page at
  05:00:58. At 05:01:05 the site's sessions list answered
  `{"active": 0, "idle": 0}`. One request (`/dm/v3/groups`) logged with
  a session after the logout had started 0.3 s before it and lasted
  0.5 s.
- Answer: an open console tab counts as **active** (a request at least
  every 61 s against a two-minute window, with the tab hidden) until
  Prism Central signs it out for inactivity, about 15 minutes without
  input; that sign-out goes through Portico's logout and takes the
  session off the list. A forgotten tab is never shown as idle; "idle"
  is a session whose tab was closed without logging out. The two-minute
  window stays (the plan's rule: twice the polling interval only when
  it is above a minute; it is a minute).
- NOT MEASURED: a tab in the foreground, Safari, Firefox.

## Implications for the design

- The sites release upgrades a single-Prism Central appliance in place
  with no action from the MSP: §4.3 of the sites design (adoption of the
  legacy document) stands.
- The site status needs hysteresis: "unreachable" now means the last
  three probes failed (sites design §4.1 amended, `docs/operating.md`).
  A Prism Central truly down is shown unreachable about a minute later
  than before; until then its tenants get Prism Central's own error.
- Maintenance and disconnect behave as designed with one site.
- The "active" count of a site in maintenance drops by itself: a console
  left open counts as active until Prism Central's idle sign-out (about
  15 minutes), which ends the Portico session too. "Disconnect" shortens
  that wait. The two-minute window of sites design §4.1 is now measured.
- Tenant deletion depends on how fast Prism Central lets go of a
  resource group after its role membership. Two backlog items: retry the
  "not empty" refusal within `operation_timeout`, and report a failed
  task's reason instead of the gateway's 504.
- The two-site live test is not run: it waits for the second pc.7.6 lab
  cluster (sites design §6 items 4 to 6, and the audience check of
  variant A that write-up 20 could not make with one Prism Central). It
  stays in docs/backlog.md.

## How to re-measure after an upgrade

```
export PORTICO_SECRETS=~/portico-secrets-appliance LAB_VM_IP=10.23.210.191 \
  LAB_SSH_USER=admin LAB_REALM=portico LAB_SAML_IDP=portico-keycloak \
  LAB_PASSTHROUGH=loopback \
  LAB_SSH_OPTS="-o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no"
source lab/env.sh
lab/portico/upgrade-appliance.sh        # restarts Portico: tenant sessions end
lab/sites/acceptance.sh --with-tenant   # Apply and disconnect cut every session on the site
lab/sites/probe-latency.sh 10
lab/sites/console-polling.sh 5          # with one console tab open and untouched
lab/sites/console-polling.sh 20         # left 17 min or more: ends with Prism Central's sign-out
```
