# Backlog: deferred findings

Findings the final reviews of Milestones 2a, 2b and 2c graded Minor and
that no milestone has closed yet, one line each with the code or script
they concern. They are grouped by area, not by milestone (several were
raised more than once). An item leaves this file when a milestone closes
it; the Milestone 2f sweep closed the 2d and 2e items listed at the end,
and the Milestone 5 sweep (plan of 2026-10-24) the release blockers.
The known limits of v0.1.0 (`docs/releases/v0.1.0.md`) are drawn from
this file.

## Public path, sessions, proxy

- Access-log field `session` means "cookie present", not "valid session";
  an upstream 401 is labelled no-session by inference from the status:
  set the rule explicitly in `Require` and before proxying
  (`internal/proxy/accesslog.go`).
- Cookie stripping re-parses the `Cookie` header with `r.Cookies()`; split
  textually instead (`internal/proxy`).
- `/.portico/logo` is `no-store` on an unauthenticated path (`private,
  max-age=60` would do).
- Keycloak asks "Do you want to log out?" when a tenant signs out after
  Portico lost the session: sessions live in memory, so any restart
  (Apply, an upgrade, a new TLS pair) or the 15-minute idle limit leaves
  a cookie without its ID token, and the logout reaches Keycloak with
  no `id_token_hint` (observed 2026-10-26: login 05:11 UTC, restart
  05:17, sign out 05:30). Keycloak's page is not branded. Brand the
  `logout-confirm` page in the generated theme, or keep the ID token
  past the session (`internal/auth`, `internal/branding`).
- The site's reverse proxy, the landing client and the Nutanix client
  build three transports to the same Prism Central; one would do, but
  it touches the swap and `CloseIdleConnections` of a site
  (`cmd/portico/sites_runtime.go`).
- Prism Central HTTP timeout 60 s hard-coded (`internal/nutanix`).
- Prism Central's own Sign Out spins forever when `/api/nutanix/v3/logout`
  answers 403 upstream (the user was deleted meanwhile, observed
  2026-10-18); Portico could end its own session and redirect to its
  logout on that answer (`internal/proxy`). The same stale session (a
  tenant deleted and recreated while the browser kept Prism Central's
  cookie) lands on Prism Central's 403 page after a fresh Portico login,
  because `/` is proxied when the cookie is present: on a console 403
  Portico could expire the Prism Central cookies and restart the SAML
  login (observed 2026-10-18, write-up 16).
- The limiter is a serial brake (bcrypt before the delay, N parallel
  connections); note it in design §4.2. No throttle on failed Basic
  attempts on the API (`internal/api`).
- `cachedIdPLookup` and the OIDC discovery hold their mutex across the
  remote call (`internal/tenant`, `internal/auth`).

## Tenants

- `FindGroup` matches top-level groups only: a same-named subgroup
  elsewhere answers 409 (`internal/idp/keycloak`).
- `GroupMembers` caps at 1000 members without paging
  (`internal/idp/keycloak`).
- Deletion steps vanish with the row (tombstone or events table).
- `Disable` does not end an existing Prism Central session (document in
  the API doc).
- `UNIQUE` violation detected by error-text substring (`internal/store`).
- `PoliciesMentioning` substring-matches the group extId in the policy
  JSON (measured ok on pc.7.6, re-measure after an upgrade).
- A v4 `DELETE` whose task fails late is answered by Prism Central's
  gateway with "504 upstream request timeout" after about 60 s: Portico
  then records the 504, not the task's reason. Look the entity's latest
  task up on a 504 and report its `legacyErrorMessage`
  (`internal/nutanix`).
- The tenant step retry of Milestone 5 covers 500, 502 and 503 only: a
  gateway `504 upstream request timeout` on a create step, and a `401
  AUTHENTICATION_REQUIRED` from a key that worked a second earlier (an
  IAM hiccup of NTNX-01 under load, write-up 28), fail the tenant and
  leave the Retry to the MSP, which then succeeds at once. Retry a 504
  on a create when the entity does not exist yet, and a 401 once after a
  short pause (`internal/tenant/provision.go`).
- The setup's "check keycloak" and "check oidc" pass with a broken login
  page (write-up 29: a theme without a readable `parent` answered 500):
  the OIDC check could fetch the realm's login page and expect 200
  (`internal/settings`).
- Portico's start applies the theme to Keycloak while an upgrade may
  still be restarting it ("theme not applied at start"); the theme is
  applied at the next save. Retry the application a few times at start
  (`internal/branding`).
- A "danger zone" that deletes a tenant's resources (VMs, images, volume
  groups) from Portico so a non-empty project can be disconnected: kept
  out with Marco on 2026-10-22 (sites usability design 4.6, ADR 0006);
  emptying the project stays the MSP's work in Prism Central. Reconsider
  only if MSPs ask for it.

## Admin UI, branding, settings

- Keycloak's SAML post page (the instant between the login and Prism
  Central, `saml-post-form`) shows the accent background with an empty
  white card and no lockup (Marco's recording, 2026-10-19): give that
  page the lockup and hide the empty card in the generated theme
  (`internal/branding/theme.go`).

- Setup as a list of connected services: today the store holds one Prism
  Central and the setup page is five cards; an MSP may run several (six
  is a real case) and later NKP. Needs a Prism Central table (name, URL,
  service account, SAML IdP, clusters), the Prism Central key on the
  tenant, one public host per Prism Central (Prism uses absolute paths),
  one SAML trust each, a "Connected services" page with add and edit, and
  the Prism Central picker on "New tenant". Design §2 step 2; its own
  milestone after the appliance image (Marco, 2026-10-19).
- The admin UI mockup (2026-10-19) shows data the pages do not have yet:
  counters above the tenant list (active, disabled, last visibility
  check), a users column, a Prism Central health dot in the header,
  "step N of M" while creating, last login per user; add them as the
  data appears (`internal/web`).
- `loginAccountTitle` may be double-escaped by Keycloak's FreeMarker
  (measure with `&` in the tagline, `internal/branding`).
- Only `messages_en.properties` is written (other default locales fall
  back to Keycloak's texts).
- Logo type taken from the browser's part `Content-Type`, no sniffing.
- Two concurrent branding saves can race cache, store and the theme `.tmp`
  files (mutex across `Set`, `internal/branding`).
- `min_display_ms` rounds down to whole seconds (label the field).
- The branding save logs the MSP name value (the plan said field names).
- `Validate` does not trim the MSP name on the API path.
- A TLS private key file is not mode-checked like the client secret
  (`internal/config`).
- `Apply(ctx, hook, b)` surface is clumsy (`Apply(ctx)` would do);
  `serve.go` variable `store` shadows the store package (rename to
  `sessions`).
- `go.mod` marks `modernc.org/sqlite` and `x/crypto` as indirect (`go mod
  tidy`).
- `TestPrismBootstrapPolicyMissingDeletesTheKey`-style gaps elsewhere:
  Review Focus "Basic header on /healthz → 200" not asserted explicitly.

## Deployment and lab scripts

- The lab's Internet link (about 470 KB/s) cannot feed a netinstall on the
  build host (`lab/build-host`): anaconda timed out on `install.img` after
  an hour. A local mirror of the BaseOS/AppStream packages the kickstart
  needs (reposync on the build host, `inst.repo` pointing at it) makes the
  build local; until then the image is built by the CI and the build host
  runs the boot test (write-up 19).
- Minor findings of the Milestone 3 review, deferred: `portico-firstboot`'s
  once-only condition is `keycloak.env`, not a marker written last;
  `sysctl` sets `ip_forward = 0` while the tailoring documents that
  netavark needs it;
  the console writes `portico.yaml` in place (no temp file and rename);
  Packer uploads the whole `deploy/` tree
  (a developer's gitignored tfvars would ride along until the cleanup);
  `ParseKeyfile` does not check the `[ipv4]` section;
  tunnel mode is not measured with a real Cloudflare tunnel yet.

- `deploy/tofu/nutanix` is validated by `tofu validate` and the CI boot
  test renders its template; a `tofu test` with a mock provider would
  also cover the VM resource's arguments (write-up 19).
- The image carries `openscap-scanner` and `scap-security-guide`
  (160 MB) so an appliance can be re-scanned in place; drop them and
  document a scan from outside if the image must shrink further.

- Theme caches off in production are a Milestone 3 image requirement.
- The qcow2 carries no container image: PostgreSQL and Keycloak are
  pulled from docker.io and quay.io at first boot (write-up 29), so a
  first boot without egress never gets an identity provider, and on a
  slow link it takes a minute after the network. Ship the two images in
  the qcow2 (`podman pull` at build, about 700 MB more) or document the
  mirror variables (`image/`, `deploy/quadlets`). Known limit of v0.1.0.
- No application-level backup: Keycloak's database (realm, clients,
  tenant users) lives in the podman volume `postgres-data`, outside
  `/var/lib/portico`, so copying that directory onto a new image loses
  them. v0.1.0 documents a snapshot of the VM as the backup (Milestone 6
  plan, decision 1). An export of Keycloak's database with the store,
  and its restore on a new image, would let a new image take over an
  appliance's state; the master key must still not leave the appliance
  in clear (`deploy/install.sh`).
- `lab/appliance/deploy.sh` reports "appliance ready" on `healthz` 200,
  which is the Go binary only: wait for Keycloak too (the setup status
  card) before saying so.
- `provision.sh` stages `keycloak.env` and `lab-ca.key` in `/tmp` on the
  VM; `create-vm.sh` exits silently if cluster or subnet are missing and
  its `sed` breaks on `|` or `&` in the SSH key comment; `env.sh` leaves
  xtrace off after sourcing; `wait_task` has no timeout and `etag_of` is
  duplicated in three files; fixed `/tmp/inv_code` and `/tmp/post_code`
  names; `create-service-account.sh` create path is only exercised by the
  rotation and `authorization-policy-create.json` is never produced.
- `lab/tenants/acceptance.sh --delete` checks that the `_internal`
  project is unchanged by diffing two snapshots it then discards: one
  run on 2026-10-18 failed that line with no way to see the field; print
  the diff on failure.

## Simplification after v0.1.0

From the code survey before the release (2026-10-26). The small,
behaviour-preserving items were done then; these change a documented
surface or the admin authentication path, so they wait for a decision.

- The file mode of the settings (Milestone 2, before the setup page):
  `config.ValidateRuntime`'s Prism Central and tenant fields,
  `settings.FromConfig`, `settings import` and the "pre-created path" of
  `deploy/portico.yaml.example`. Retiring it also shrinks
  `settings.Overlay`, which still writes fields nobody reads, and
  `Runtime` to the two Keycloak secrets (about 250 lines).
- The `prism_host` aliases (store document, YAML, the settings body of
  the API): drop them one release after 0.1.0 with a note.
- The adoption of the single-Prism-Central document of before Milestone
  4 (`adoptLegacy`, `LegacySiteCode`): no 0.1.0 store goes through it.
- `IdentityFromContext` is used by tests only;
  the admin login page repeats `checkPassword` and `sourceOf` of the
  API, without the constant-time dummy hash: share them, with a test on
  the limiter (`internal/api`, `internal/web/login.go`).
- Three hand-written cookie expiries (logout, leaving a site, switching
  site): one helper (`internal/auth`, `internal/proxy`).
- `nutanix.getRaw` repeats `call` for one request; `listAll` decodes
  each page twice (`internal/nutanix`).
- The retired VM of Milestones 0 to 2 still has its scripts and their
  checks: `lab/vm/`, `lab/portico/deploy.sh`, `undeploy.sh`, the lab
  `portico.yaml` files, and their lines in `lab/test_scripts.sh` and
  `lab/README.md`.

## Closed in Milestone 2f (write-up 16)

From Milestone 2d: realm typo reported as a refused secret; empty pickers
without `required`; store I/O errors answered as 400; secret inputs with
`autocomplete="off"`; `portico.service` start limit; test gaps (plaintext
secret in the store, refused import leaving the document unchanged).

From Milestone 2e: revoked key still listed in the failure result and
`key_expires` rendering the zero time; Prism Central URL with userinfo
stored and logged; Keycloak errors classified by substring; SAML log line
without the defaulted name; failed bootstrap fragment resetting typed
fields; no password policy on the realm the bootstrap creates; bare
"login state missing" page after the 10-minute login cookie; realm name
drawn across the logo on the Keycloak login page.

## Sites (Milestone 4)

- Disaster recovery between sites, managed by Portico (Marco,
  2026-10-29; until then the MSP configures it in Prism Central and
  Portico only places the tenant on the DR site, as kept open on
  2026-10-20): for a tenant placed on two sites, Portico creates and
  shows the protection policy and the recovery plan between them
  (replication schedule, retention, the network mapping of the tenant's
  subnet onto its twin on the other site), and offers test failover and
  failover from the tenant page. To be measured on two pc.7.6 sites:
  which v4 data-protection endpoints create them, whether a project
  scopes them, and which role the service account needs. The tenant
  networking design keeps the tenant's VPC and subnet identical in name
  and address on every site so the mapping is one to one.
  Optional per tenant (Marco, 2026-10-29): stretch the tenant's subnet
  between its two sites with Layer 2 subnet extension and move VMs with
  cross-cluster live migration (CCLM, or on-demand CCLM), keeping their
  addresses. The same tenant `/24` on both sites makes this possible
  without renumbering (measured on both sites on 2026-10-29).
  Prerequisites from the Flow Virtual Networking and DR guides 7.6,
  which Portico checks or asks for before offering it: the two Prism
  Centrals paired as availability zones, a Network Gateway appliance per
  extension and a `0.0.0.0/0` static route in the tenant's VPC, the same
  AHV version and compatible CPUs on both sites, the DR ports open, and
  the bandwidth between the sites: under 5 ms round trip and enough for
  peak writes when CCLM runs on synchronous replication. Portico cannot create the bandwidth; it
  measures or asks for it, and leaves the option off when it is missing.
- Site page variant C: sites grouped by region, for an MSP with many
  sites (mockup of 2026-10-20).
- API versions chosen per site, for sites on different Prism Central
  releases; today every site uses the pc.7.6 beta paths.
- The live test with two Prism Centrals (design sites §6 items 4–6) on a
  second pc.7.6 lab cluster: SAML on two IdPs with the same redirect URL,
  switching back without a password, a one-site tenant never reaching the
  other site, the tab left on the previous site.
- The connected-users list is per node; the pair (design 4.11) shares
  sessions and then the list.
- The new-tenant form takes cluster and container as text prefilled with
  the site's defaults; live pickers per site would save typing an extId.
- Minor findings of the Milestone 4 review, deferred: a placement left
  `creating` when the Keycloak group step fails during "add a site"
  (retry refused, the page polls); a legacy tenant without a project
  gets no placement and a retry marks it active with no site; the
  tenant is resolved only at login; an index read in flight during an invalidation can keep the
  old list up to 5 s; a settings save copies the sites read outside the
  lock and can revert a concurrent site change; removing a site checks
  its connections before the deletions in Prism Central and outside the
  patch, so a tenant connected to it during the removal (up to two
  minutes with credentials) is left on a removed site; "Disconnect"
  shows outside maintenance,
  where one-site sessions are re-attached at once; the sites design §5 still says the site is held in a cookie.
- The public names cannot change after setup: only the bootstraps write
  them into Keycloak (the OIDC client's redirect and post-logout URIs,
  each SAML client's ACS) and into each site's identity provider in
  Prism Central, so a new name and Apply break the login. Apply should
  carry a changed name there. (The rename to `portal_host` and the lab's
  new names were done in Milestone 5, write-up 27.)
- Minor findings of the appliance acceptance (write-up 21): the legacy
  adoption line ("the single Prism Central of the stored document is now
  site site-1") is written in plain text before the JSON logger exists;
  a maintenance change is logged twice (monitor and API); re-running
  `install.sh` on an appliance also upgrades the packages it installs
  (cloudflared went to 2026.10.2), which docs/install.md does not say.
- The service account's role (`Project Manager_acp`) cannot list
  floating IPs (403, write-up 25) nor Flow security policies, so
  Disconnect does not count them and relies on Prism Central refusing to
  delete a project that still holds one. A role or an authorization
  policy granting those reads is to be measured; the one-step setup of
  sites (Part C) creates no custom role.
- The other node of a pair reads the sites every 30 seconds but the
  maintenance flags only at its start: a site put in maintenance on one
  node still takes new entries on the other until it restarts.
- A site set up before Part C has no service account or key id
  recorded, so its first Finish setup or Repair makes a new key and
  leaves the old one valid in Prism Central, held by nobody, and its
  removal lists its key as left (write-up 26: both revoked by hand on
  the lab). The service-account step's line says so and asks the MSP to
  revoke it there. The run could revoke the old key when it can tell
  it is Portico's (by its `portico-` name on the account it found), or
  keep the stored key after checking it belongs to that account.

## Tenant ready network (0.2.0)

- The *Portico Networking* policy filters every entity type with `*`
  (`internal/settings/networking_rights.go`); a filter limited to the
  transit VPC, its external subnet and the tenants' projects is spec 6.1,
  to be measured.
- An update of a role's operations answered 400 in the lab before the
  code sent the full role; the product now replaces the networking role
  when its update is refused and keeps the tenant role. Measure the PUT
  on pc.7.6 and drop the replacement if it is never needed.
- The watch (`internal/tenant/netwatch.go`) does not re-run the site's
  readiness checks (spec 4.1 "in the site monitor at its own pace"):
  only the card does.
- A floating IP read with an empty `projectExtId` is not taken as moved
  by the watch: whether Prism Central always returns it is not measured.
