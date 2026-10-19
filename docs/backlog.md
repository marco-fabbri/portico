# Backlog: deferred findings

Findings the final reviews of Milestones 2a, 2b and 2c graded Minor and
that no milestone has closed yet, one line each with the code or script
they concern. They are grouped by area, not by milestone (several were
raised more than once). An item leaves this file when a milestone closes
it; the Milestone 2f sweep closed the 2d and 2e items listed at the end.

## Public path, sessions, proxy

- A second login does not retire the first session (`internal/auth`).
- Access-log field `session` means "cookie present", not "valid session";
  an upstream 401 is labelled no-session by inference from the status:
  set the rule explicitly in `Require` and before proxying
  (`internal/proxy/accesslog.go`).
- An empty `Authorization` value passes the public policy while the docs
  say "every header" (deny on presence, `internal/proxy`).
- Cookie stripping re-parses the `Cookie` header with `r.Cookies()`; split
  textually instead (`internal/proxy`).
- A POST after idle expiry carries `next=<POST path>` and lands on a 405
  after login: set `next` for GET/HEAD only (`internal/auth`).
- The retry link built from `RequestURI` can start with `//` (guard like
  `safeNext`, `internal/proxy`).
- `/.portico/logo` is `no-store` on an unauthenticated path (`private,
  max-age=60` would do).
- `MaxIdleConnsPerHost` left at the default 2: set it to avoid repeated
  TLS handshakes to Prism Central (`internal/proxy`).
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

- `CreateUser` takes no per-tenant lock and reads the status before taking
  the lock (`internal/tenant`).
- `FindGroup` matches top-level groups only: a same-named subgroup
  elsewhere answers 409 (`internal/idp/keycloak`).
- `GroupMembers` caps at 1000 members without paging
  (`internal/idp/keycloak`).
- A Keycloak token invalidated server-side is reused until local expiry:
  clear on 401 and retry once (`internal/idp/keycloak`).
- `delete_project` accepts only `true`: use `ParseBool`, 400 on garbage
  (`internal/api`).
- Deletion steps vanish with the row (tombstone or events table).
- `Disable` does not end an existing Prism Central session (document in
  the API doc).
- `UNIQUE` violation detected by error-text substring (`internal/store`).
- `PoliciesMentioning` substring-matches the group extId in the policy
  JSON (measured ok on pc.7.6, re-measure after an upgrade).

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
- CSP `default-src 'self'` blocks the four inline style attributes and
  HTMX's indicator style: move to classes, `htmx-config
  includeIndicatorStyles false` (`internal/web`).
- Template errors after headers are sent: render into a buffer, 500 on
  error (`internal/web`).
- Unused reflected `?notice=` banner (remove until a handler needs it).
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
- Config validation accepts `prism_host == id_host`; a TLS private key
  file is not mode-checked like the client secret (`internal/config`).
- `Apply(ctx, hook, b)` surface is clumsy (`Apply(ctx)` would do);
  `serve.go` variable `store` shadows the store package (rename to
  `sessions`).
- Store file created with the umask, not chmod 0600 (plus `-wal`/`-shm`).
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
- Minor findings of the Milestone 3 review, deferred: `api.BasicAuth`
  duplicates the factory-password check of `RequireAdmin` and only tests
  use it; `install.sh` copies `$0` (breaks under `curl | bash`, use
  `BASH_SOURCE`); a second `install.sh` run does not restart PostgreSQL
  and Keycloak when their quadlets changed; `portico-firstboot`'s
  once-only condition is `keycloak.env`, not a marker written last;
  `sysctl` sets `ip_forward = 0` while the tailoring documents that
  netavark needs it; the kickstart opens SSH on every interface;
  `harden.sh` never fails the build on an OpenSCAP regression; the
  console's "show once" wording and scrollback after the credentials;
  the console writes `portico.yaml` in place (no temp file and rename);
  the image workflow gives the `contents: write` token to Packer's plugin
  getter and pins actions by tag; Packer uploads the whole `deploy/` tree
  (a developer's gitignored tfvars would ride along until the cleanup);
  `ParseKeyfile` does not check the `[ipv4]` section; the password page
  accepts the current password as the new one and keeps the session;
  tunnel mode is not measured with a real Cloudflare tunnel yet.

- Prism Central pc.7.6 answers 500 "Failed to store new cookie in IDF"
  intermittently for minutes after identity deletions (write-up 19); a
  provisioning step that meets it fails the tenant and the operator
  retries by hand. Retry a 500 from Prism Central once or twice with a
  pause before failing the step (`internal/tenant`).
- `deploy/tofu/nutanix` is validated by `tofu validate` and the CI boot
  test renders its template; a `tofu test` with a mock provider would
  also cover the VM resource's arguments (write-up 19).
- The image carries `openscap-scanner` and `scap-security-guide`
  (160 MB) so an appliance can be re-scanned in place; drop them and
  document a scan from outside if the image must shrink further.

- `portico.service` could add `RestrictAddressFamilies`,
  `SystemCallFilter`, `RestrictNamespaces`, `ProtectHostname`,
  `ProtectClock`, `ProtectProc`, `UMask` (Milestone 3 image).
- Theme caches off in production are a Milestone 3 image requirement.
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
