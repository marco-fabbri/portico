# 32 — Pre-release review: the root boundary, the admin login, the polls

## Question

A review of the whole product code before v0.1.0 changed how root
works in the exposure request directory (opened once with `os.Root`,
system files replaced by a rename) and how the admin login is braked (a
lockout with 429 instead of a delay). Unit tests run on macOS; three
things needed a real appliance:

1. Does `portico exposure apply`, as root under SELinux, still install a
   pair through the opened directory, and do the files it renames keep
   their owner, mode and label?
2. Does the lockout answer 429 with `Retry-After` on the appliance's
   listener, and is a cross-site change to the API refused?
3. Do the polling banners carry the poll header once rendered by the
   appliance?

## Measured on

- Appliance: `10.23.210.191`, `dev-c29060b`, setup mode (no sites)
- Date: 2026-10-27
- By: Claude

## Method

1. `lab/portico/upgrade-appliance.sh`, then a login to the admin UI from
   the password file and, on the exposure card, a renewal with the pair
   already in service (`POST /exposure`, mode `certificate`, with
   `HX-Request: true`); `GET /exposure/status` until the result. On the
   appliance: `ls -la /var/lib/portico/exposure/`,
   `ls -lZ /etc/portico/tls/ /etc/portico/portico.yaml`,
   `journalctl -u portico-exposure`, `ausearch -m avc -ts recent`.
2. Four `curl -u admin:wrong https://<appliance>:8443/api/v1/tenants`,
   then a fifth with headers shown; a `POST` to
   `/api/v1/tenants/x/disable` with `Sec-Fetch-Site: cross-site`.
3. The answer to the renewal's `POST /exposure`, read for the banner.

## Result

- MEASURED: the renewal is applied ("certificate installed; the public
  listener serves it within a few seconds") and the request directory
  holds only `result.json` (root, 0644) afterwards.
- MEASURED: `public.crt` (0644) and `public.key` (0640) are
  `root:portico` with `system_u:object_r:etc_t:s0`, as before the
  change; no temporary file is left in `/etc/portico/tls/`; no AVC
  denial.
- MEASURED: the fifth failed Basic answers `429` with `Retry-After: 1`
  and `{"error":"too many failed sign-ins from this address: wait 1 s"}`;
  the cross-site `POST` answers `403`.
- MEASURED: the banner renders with
  `hx-headers='{"X-Portico-Poll": "1"}' hx-disinherit="hx-headers"`.
- NOT MEASURED live: the internal pass-through dropping `portico_admin`,
  and a tenant's sign-out with and without `Sec-Fetch-Site`, need a site
  and a tenant, which this appliance in setup mode did not have; the
  unit tests cover them. Port 443 was closed throughout, as setup mode
  keeps the public listener off.
- MEASURED on NTNX-01 (pc.7.6), 2026-10-27: in the morning the vmm VM
  and image lists answered `504 upstream request timeout` to a
  `$filter` on the name while the plain lists answered; a few hours
  later `$filter=projectExtId eq '<id>'`, the filter Disconnect counts
  a project's contents with, answered `200` within a second on the VMs,
  images and subnets lists. VMs carry `projectExtId` in the list. The
  timeouts were Prism Central being slow for a while, not the filter:
  Disconnect keeps failing its count on a 5xx (it stops with the
  reason, and a Retry counts again) rather than counting by paging the
  whole list.

## Implications for the design

None beyond the review's own changes (design §4.2 admin login, branding
logo, project naming).

## How to re-measure after an upgrade

Steps 1 to 3 above. With a site and a tenant in place, also: open the
pass-through on `:9440` under the same name as the admin UI and check
in Prism Central's access log, or with a capture on the appliance, that
no `portico_admin` cookie arrives; sign out from the console and check
that the chain still ends on Keycloak's logout. For the project
filter: `lab/pc.sh GET "/api/vmm/v4.0/ahv/config/vms?\$limit=1&\$filter=projectExtId%20eq%20'<id>'"`
with and without the filter, timed.
