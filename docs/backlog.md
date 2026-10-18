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
- Keycloak login page layout: the realm name under the logo is readable
  now but plain and redundant with a logo that already carries the name
  (Marco, 2026-10-18); a layout pass on the login page is planned after
  Milestone 2f (hide the name when a logo is set, or style it).
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
