# 12 — Console headers, provider selection and the Sign Out path (Milestone 2b Task 0)

## Question

Three facts the public policy and the 2c landing page need:

1. Does the Prism Central console ever send an `Authorization` header on
   its own calls? If never, the public path can refuse every scheme, not
   only Basic (write-up 10 left Bearer allowed for lack of this fact).
2. What exactly does the "Log in with <IdP>" button send to select the
   SAML provider, so Portico's landing page (design §4.3 step 5, §4.10)
   can do it on the user's behalf?
3. Found on the way: does Prism Central's own Sign Out work through
   Portico?

## Measured on

- Prism Central: pc.7.6.0.6, cluster NTNX-01 (nested lab)
- Portico: branch `m2b-admin-ui`, access log with the new `auth_scheme`
  field (scheme name only, never the value), deployed on the lab VM
- Chrome on the Mac as `alice@lab.portico.test`
- Date: 2026-10-18
- By: Claude (Marco's session)

## Method

1. Full console session as alice through the public name: Keycloak login,
   `/console/`, VM list, VM selected, Launch Console (noVNC popup),
   Projects, Portico logout. Then on the VM:
   `journalctl -u portico -o cat | grep '^{' | jq -r 'select(.msg=="request") | "\(.host) \(.auth_scheme)"' | sort | uniq -c`.
2. On the provider page (`/ui/iam/providers`, Portico session alive,
   Prism Central logged out) a `fetch`/`XMLHttpRequest` wrapper injected
   with the browser tooling logged method, URL and body of every call to
   `login-requests`; then the SAML button was clicked.
3. Prism Central user menu → Sign Out, through Portico.

## Result

- MEASURED: **644 requests on `prism.lab.portico.test` in the session,
  every one with `auth_scheme: none`** (plus 5 on the identity host and 2
  on the admin listener, all `none`). The console, the noVNC popup
  (`/console/lib/noVNC-app/...`, `generate-console-token` 202) and the
  Admin Center pages authenticate with cookies only.
- MEASURED: the SAML button sends one
  `PUT /api/iam/v4.1.b3/authn/login-requests/{iam-sessionid}` with body
  `{"clientId": "<the console's own OIDC client id>", "responseType":
  "CODE", "scope": ["OFFLINE_ACCESS","OPENID","PROFILE","EMAIL","GROUPS"],
  "redirectUri": "https://<public host>/auth_code?original_request=<url>",
  "loginProviderId": "<SAML IdP extId>"}` and then navigates to
  `/api/iam/authn/initiate-login` (write-up 06). The `clientId` is a
  constant of the Prism Central installation (`7e3a…` on the lab) and the
  console reads it from `GET .../login-requests/{id}` before the PUT; the
  landing page must do the same GET and copy `clientId`, `scope` and
  `redirectUri` from it, setting only `loginProviderId`.
- MEASURED: Sign Out in the console goes `#logout` →
  `GET /api/nutanix/v3/logout` → `GET /api/iam/authn/v1/oidc/logout?post_logout_redirect_uri=…`
  → `GET /redirect?logout=https://<public host>/`. `/redirect` was not in
  the allow-list: Portico answered 403 "path not allowed on the public
  listener" and the tenant could not sign out of Prism Central. Added
  `/redirect` (exact) to the default allow-list; after the redeploy the
  chain ends on the provider page as intended.
- MEASURED, incidental: a browser holding a Prism Central session of one
  user (carol) and a Portico session of another (alice) is served as the
  Prism Central user (carol) until that session ends. The two sessions
  are independent by design (two protocols, two cookies); Portico's
  logout already expires every Prism Central cookie it sees, which
  covers the shared-workstation case measured in write-up 09.

## Implications for the design

- Public policy: every `Authorization` header is refused on the Prism
  Central host (`deny-basic-auth` for Basic, `deny-authorization` for
  any other scheme). Write-up 10's "Bearer stays allowed" is superseded.
  `lab/auth/probe-local-login.sh` has the line.
- `/redirect` joins the default allow-list (design §4.3 step 3 list).
- Landing page (Milestone 2c): GET the login request, PUT it back with
  `loginProviderId` set to the IdP extId, then redirect to
  `/api/iam/authn/initiate-login`; on any failure, forward the original
  provider page.

## How to re-measure after an upgrade

Repeat the session as alice and run the `jq` count above; the expected
answer is a single `none` line per host. Re-capture the PUT with the
wrapper (the snippet is in the Milestone 2b plan, Task 0 Step 5).
`lab/auth/probe-local-login.sh` covers the Bearer refusal and the console
Sign Out stays a browser check.

## Part two: the admin UI in the browser (Milestone 2b acceptance)

Measured on 2026-10-18 with Chrome on the Mac against
`https://prism.lab.portico.test:8443/` (build of branch `m2b-admin-ui`
deployed by `lab/portico/deploy.sh`, NTNX-01 pc.7.6.0.6).

- MEASURED: `/` without a session redirects to `/login?next=%2F`. A wrong
  password answers the form again with the banner "Wrong user or
  password"; the right one lands on the empty tenant list ("No tenants
  yet."), with the administrator name and a Log out button in the header
  and the build version in the footer.
- MEASURED: "New tenant" → slug `tenant-b`, display name "Tenant B (lab)",
  contacts → the detail page opens at once with the badge `creating`, an
  empty steps table and the line "Running… this page refreshes by
  itself". The steps table filled in by itself every 5 s
  (`keycloak-group` first); when the run ended the whole page reloaded
  once: badge `active`, seven `ok` steps with their extIds, visibility
  "OK: the group holds one project-scoped role in this project and
  nothing else". Provisioning took **67 s** (05:31:38 → 05:32:45 on the
  VM's clock).
- MEASURED: "Add a user" with `carol@lab.portico.test` → the "User
  created" page shows the 20-character temporary password once in a
  selectable box; "Copied, continue" returns to the detail page, where it
  appears nowhere.
- MEASURED: Disable → badge `disabled` (amber) and the Enable button
  replaces Disable; Enable → `active` again.
- MEASURED: "delete the project too" ticked + Delete tenant → the list
  shows `tenant-b` as `deleting` (grey) and polls every 10 s; after the
  run (journal: `tenant operation done delete`, 106 s on the nested lab)
  the row is gone and the list says "No tenants yet." The API confirms
  `[]`; `lab/tenants/acceptance.sh --delete` then found the project and
  the groups absent and removed carol from Keycloak.
- MEASURED: Log out → `/login`, and `/` redirects to the login page again.
- MEASURED on the way: a GET of a deep link while logged out lands on
  `/login?next=<path>` and returns there after the login.

Re-measure: `source lab/env.sh && lab/portico/deploy.sh`, open the URL
it prints, repeat the sequence above; `lab/tenants/acceptance.sh --delete`
cleans up.
