# 31 — Setup in the web UI: Keycloak's temporary administrator, the tunnel check, the exposure request

## Question

ADR 0007 moves the certificate, the tunnel and the realm out of the
console. Three things it relies on had to be measured on a real
appliance:

1. Once Portico deletes the first boot's temporary Keycloak
   administrator, does Keycloak recreate it at the next restart, and how
   does an operator get a master administrator back for maintenance?
2. How does `cloudflared` behave with a token Cloudflare does not
   accept, and what tells `portico exposure apply` that a token works?
3. Does the exposure request (admin UI → `portico-exposure.path` → root
   `apply`) install a pair, switch modes and report back as designed?

## Measured on

- Appliance: `10.23.210.191`, from `dev-03cd3de` to `dev-5df267e`,
  setup mode (no sites yet)
- Keycloak 26.3 (quadlet image of `deploy/`), cloudflared 2026.10.4
- Date: 2026-10-26 and 2026-10-27
- By: Claude

## Method

1. Saved the public names on the setup page and waited for the realm
   job; listed the master realm's users with the temporary
   administrator's credentials before and after, restarted Keycloak
   (`systemctl restart keycloak`) and tried them again. Then, on the
   appliance:
   `podman exec -e PW keycloak /opt/keycloak/bin/kc.sh bootstrap-admin user --username <name> --password:env PW --no-prompt --http-management-port=9001`
   (once with, once without the last flag), and a password grant on the
   master realm with the new user; the test users were deleted after.
2. Sent a tunnel request with a well formed token (base64 JSON with
   `a`, `t`, `s`) that belongs to no tunnel; read
   `journalctl -u cloudflared`, `systemctl show -p ActiveState -p
   NRestarts cloudflared` and `curl http://127.0.0.1:20241/ready`.
3. Through the admin UI with `curl` (multipart, CSRF token of the
   session, `HX-Request: true` as HTMX sends it): a key that does not
   match, a CA certificate first, a token with spaces, a leaf with its
   chain, the self-signed pair, the unknown tunnel token, and a switch
   from tunnel back to direct; after each, `result.json`, the request
   directory and the installed files on the appliance.

## Result

- MEASURED: the realm job creates the realm and both clients, stores
  them, then deletes the temporary administrator and its copy
  `/var/lib/portico/keycloak-bootstrap.env`. A Keycloak restart does not
  recreate it: its credentials stay refused.
- MEASURED: `kc.sh bootstrap-admin user … --http-management-port=9001`
  creates a master administrator (exit 0, token 200). Without the flag
  it fails because port 9000 is in use by the running server, but it
  still creates the user.
- MEASURED: while Keycloak restarts with its public name (right after
  the names are saved), the job's admin calls get a dropped connection
  (`EOF`, `connection reset by peer`). Classified as "did not answer",
  the job waits and the next attempt succeeds.
- MEASURED: with a token Cloudflare does not know, `cloudflared` does not
  exit: it logs `Register tunnel error from server side error="Failed to
  get tunnel"` and retries, `ActiveState=active`, `NRestarts=0`. A check
  on the service's state passes it. Its readiness endpoint answers `503`
  with `"readyConnections":0`. The first registration attempt comes about
  eight seconds after the start.
- MEASURED: with the check on `/ready` (up to 30 s), the unknown token is
  refused 31 s after the request, the old token comes back, `cloudflared`
  is disabled again, Portico keeps direct exposure and the session.
- MEASURED: the refusals reach the page with their messages. The leaf
  chain is installed as `/etc/portico/tls/public.crt` (root:portico 0644,
  two certificates) and `public.key` (root:portico 0640). The request
  files are deleted and `result.json` (root, 0644) carries the request's
  id.
- MEASURED: tunnel → direct restarts Portico; `/exposure/ready` answers
  without a session during and after the restart, and the result says
  "direct exposure on port 443 … Portico restarted"; `cloudflared` is
  disabled.
- MEASURED: in setup mode port 443 is closed (the public listener starts
  with the first Apply), so the served certificate is read from the file,
  not from the listener.

- MEASURED (dev-5df267e, systemd 252-67.el9_8.6): a path unit with
  `PathChanged=` on a file fires when a file is renamed onto that name,
  absent or present, as well as when it is removed. The page now writes
  each request file aside and renames it; a direct renewal sent that way
  ran `portico exposure apply` and left only `result.json` in the
  request directory, with no temporary file.
- MEASURED (dev-5df267e): the setup page refuses, with 200 and the
  message in the card, an RSA 1024 key, a P-521 key, a SHA-1 signed
  certificate and an Ed25519 certificate, each made with `openssl req
  -x509` and `basicConstraints=CA:FALSE`. This found that a self-signed
  server certificate's SHA-1 signature was let through (only a root
  above the server's certificate is exempt); fixed before the release.
- NOT MEASURED live: the needs-admin phase of an appliance without a
  temporary administrator (this appliance has its realm) and the job
  racing "Use an existing realm" (no reliable way to time it by hand);
  both are covered by tests.

## Implications for the design

- The tunnel token check reads `cloudflared`'s `/ready` on a pinned
  metrics address (`deploy/cloudflared.service`), not the service state
  (ADR 0007).
- `docs/operating.md` gives the recovery command with
  `--http-management-port=9001`.

## How to re-measure after an upgrade

- Keycloak: on a fresh appliance, save the names, wait for the realm
  card to say ready, then `systemctl restart keycloak` and try the
  temporary administrator from `keycloak.env`; run the recovery command
  above.
- cloudflared: send a tunnel request with a made-up token from the setup
  page and read `curl http://127.0.0.1:20241/ready` and
  `journalctl -u cloudflared` on the appliance while it waits; the page
  must answer within about 30 s that the connection did not register.
- The path unit: run a throwaway `systemd-run --path-property=PathChanged=<dir>/request.json`
  unit on a scratch directory, rename a file onto that name and count
  its runs.
