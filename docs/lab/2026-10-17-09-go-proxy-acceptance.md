# 09 — The Go proxy in the lab: Milestone 1 acceptance

## Question

Milestone 1 (design §9): a browser through Portico reaches Prism Central
only after a Keycloak login; every local-login path measured in write-up 06
is refused on the public name; the VM console works through the Go proxy
(ADR 0002 left it to be measured); Portico keeps serving with the identity
provider down.

## Measured on

- Prism Central: pc.7.6.0.6; Keycloak 26.3.5 with
  `KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true`; Portico built from branch
  `m1-core-binary` (first run `dev-175e532`, re-run after the review fixes),
  running as `portico.service` on the lab VM in place of nginx
- Date: 2026-10-17
- By: Claude (Marco's session): `lab/portico/deploy.sh`,
  `lab/auth/probe-local-login.sh`, Chrome via the Claude in Chrome extension

## Method

1. `lab/vm/provision.sh` (Keycloak restarted with the dynamic backchannel),
   `lab/saml/keycloak-realm.sh oidc-client https://prism.lab.portico.test/.portico/callback`,
   `lab/portico/deploy.sh`.
2. `lab/auth/probe-local-login.sh` (prints got/expected per line, exit 1 on
   any difference).
3. Chrome: `https://prism.lab.portico.test/console/` as alice, then the
   SAML button, then "Launch Console" on the tenant VM.
4. `sudo systemctl stop keycloak` on the VM, probe again, restart.
5. `systemctl status portico` for memory, `ss -tni` for the upstream
   connections, `ausearch -m avc` for SELinux.

## Result

- MEASURED: Keycloak's discovery document read on `http://127.0.0.1:8080`
  carries the public `issuer` and `authorization_endpoint`
  (`https://id.lab.portico.test/...`) and loopback `token_endpoint` and
  `jwks_uri` (`http://127.0.0.1:8080/...`). Portico needs no DNS for the
  identity host and never leaves the appliance to complete a login.
- MEASURED: the probe passes on every line:

  | request through the public name | result |
  |---|---|
  | `GET /console/` without a session | 302 to `/.portico/login?rd=%2Fconsole%2F` |
  | `POST .../login-requests/{id}/$actions/validate-credentials` with admin | 403 `deny-local-login` |
  | `POST /PrismGateway/j_spring_security_check` with admin | 403 `deny-legacy-login` |
  | `GET` on four API generations with `Authorization: Basic admin` | 403 `deny-basic-auth` |
  | `GET /api/multidomain/...` with `X-ntnx-api-key` | 403 `deny-api-key` |
  | `POST /api/nutanix/v3/groups` without session or credentials | 401 |
  | `GET https://id.lab.portico.test/admin/master/console/` | 403 `id-not-allowed` |
  | `GET https://id.lab.portico.test/realms/portico-lab/` | 200 |
  | internal pass-through `https://10.23.210.190:9440/...` with Basic admin | 200 |

- MEASURED, browser: `/console/` → Keycloak form on `id.lab.portico.test`
  (through Portico) → alice's credentials once → Prism Central login page
  → "Log in with portico-lab-keycloak" → console without a second password
  prompt (Keycloak SSO session) → VM list with only
  `portico-lab-tenant-a-vm1`. 191 requests in Portico's log for the
  session; the only refusal caused by Portico was `/favicon.ico`
  (`not-allowed`, now in the default allow-list); the other 403s are Prism
  Central's own RBAC on licensing and product details, as in write-up 08.
- MEASURED, VM console: "Launch Console" opened the noVNC popup through
  Portico (`/console/lib/noVNC-app/...` 200, `generate-console-token` 202,
  task polled to completion) and one of Portico's connections to Prism
  Central then carried 1.37 MB inbound while the others stayed at polling
  size: the VNC framebuffer passed through the Go proxy. The popup window
  itself is outside the automation's tab group, so the picture was not
  captured; it stayed open on Marco's screen for a visual check.
  CONFIRMED by Marco on 2026-10-18: the popup showed the VM's own
  `localhost login:` prompt, the Rocky console rendered through the proxy.
- MEASURED, IdP down: with Keycloak stopped the credential lines still get
  403, `/healthz` stays 200, the service stays `active`,
  `/.portico/login` answers 302 to the identity host (discovery already
  cached) and the identity host answers 502 `upstream unavailable`; with a
  cold cache the login answers 503 (unit test `TestRunServeWithIdPDown`).
  After `systemctl start keycloak` the probe passes again.
- MEASURED: `portico.service` memory 8.2 MiB after the browser session
  (peak 8.6 MiB); VM total used 1.1 GiB. SELinux enforcing, zero AVC
  denials; the unit runs as user `portico` with `ProtectSystem=strict` and
  `CAP_NET_BIND_SERVICE` only.
- MEASURED, logout (review finding): `/.portico/logout` expires the
  Portico cookie and every other cookie the browser holds for the Prism
  Central host (`iam-sessionid` is set with `Path=/`), then sends the
  browser to Keycloak's end-session endpoint. Keycloak answered 400
  "invalid redirect uri" until the OIDC client listed
  `https://prism.lab.portico.test/` among its post-logout redirect URIs;
  now it answers 302 back to the Prism Central host (no Keycloak session
  on the curl side; a browser gets a confirmation page). Two probe lines
  cover it.
- MEASURED: with nginx gone, the Keycloak admin API is reachable only on
  the VM loopback: Portico refuses `/realms/master` on the public name
  (`id-master-realm`), so `lab/saml/kc.sh` now runs curl on the VM over
  SSH. This is the design working as intended.
- MEASURED: the binary refuses a secret file readable by group or others
  (first deploy installed it 0640 root:portico and the service failed with
  a clear message; now 0600 portico:portico).

## Implications for the design

- Milestone 1 acceptance met. ADR 0002's open point (WebSocket through the
  Go proxy) is closed.
- Design §4.2 gains the same-site requirement for the two public names
  (cookie `SameSite=Lax`, SAML response is a cross-host POST); §4.3 gains
  the `/.portico/*` endpoints, the deny-before-session order, the cookie
  attributes and the loopback backchannel.
- The "IdP down" experience is fail closed but blunt (502 on the identity
  host); a friendlier page is a Milestone 2 polish item.

## How to re-measure after an upgrade

`source lab/env.sh && lab/portico/deploy.sh && lab/auth/probe-local-login.sh`,
then the browser round trip as alice and "Launch Console". Compare the
refused paths in `journalctl -u portico -o cat | jq 'select(.rule!="ok")'`
with this write-up.
