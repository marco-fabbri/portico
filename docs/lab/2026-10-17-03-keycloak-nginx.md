# 03 — Keycloak and nginx stand-in on the lab VM (design 6.6, first sample)

## Question

Infrastructure for items 6.1, 6.2, 6.4, 6.5, plus design item 6.6:
resource cost of Keycloak + PostgreSQL on a 6 GiB VM.

## Measured on

- Lab VM `portico-lab`: Rocky 9.8, podman 5.8.2, nginx 1.20.1,
  Keycloak 26.3.5 (quay.io/keycloak/keycloak:26.3), PostgreSQL 16.15
- Date: 2026-10-17
- By: Claude (Marco's session), `lab/vm/provision.sh`

## Method

`source lab/env.sh && lab/vm/provision.sh` (idempotent; second run ends
with `keycloak ready`). From the Mac, `curl --resolve <host>:443:10.23.210.190
--cacert ~/portico-secrets/lab-ca.crt` so no `/etc/hosts` change was needed
for the check. Memory with `free -m` and `podman stats` idle and after 20
password-grant token requests to Keycloak.

## Result

- MEASURED: `https://id.lab.portico.test/realms/master` → 200 and
  `https://prism.lab.portico.test/console/` → 302 (Prism login redirect)
  through nginx, with the self-signed certificate validating against the
  lab CA (`ssl_verify_result=0`). An unknown host name gets `return 444`
  (connection closed).
- MEASURED: the Keycloak admin console is reachable through the public
  name (`/admin/master/console/` → 200). Design §4.2 requires Portico to
  deny `/admin` and `/realms/master` on the public listener; nginx here
  does not, by design of the lab.
- MEASURED: memory on the 6 GiB VM, idle: 1226 MiB used in total,
  Keycloak container 490 MiB, PostgreSQL 44 MiB. After 20 token requests:
  Keycloak 508 MiB. Container images on disk: 1.8 GiB under
  `/var/lib/containers`; root filesystem 2.4 GiB used of 39 GiB.
- MEASURED: Keycloak starts in about 40 s on first boot (schema creation);
  the quadlet needs `TimeoutStartSec=300`. `KC_HOSTNAME=https://id.lab.portico.test`,
  `KC_HTTP_ENABLED=true`, `KC_PROXY_HEADERS=xforwarded` work behind nginx
  with `X-Forwarded-Proto/Host/Port` headers.
- MEASURED: SELinux stays enforcing with one boolean,
  `httpd_can_network_connect=on`; zero AVC denials after provisioning.
- MEASURED: Rocky GenericCloud ships without firewalld; the script installs
  and enables it. Quadlet-generated units cannot be `systemctl enable`d
  (`[Install]` in the quadlet does that); use `systemctl start`.
- MEASURED: nginx 1.20 (Rocky 9 AppStream) needs `listen 443 ssl http2;`,
  not the 1.25+ `http2 on;` directive.
- MEASURED: no SELinux or firewall issue for the WebSocket upgrade headers
  (not yet exercised; write-up 08 measures the VM console).

## Implications for the design

- Appliance sizing of 6 GiB (design §3) holds with wide margin: Keycloak
  plus PostgreSQL plus OS use about 1.3 GiB idle. 4 GiB would also fit,
  but 6 GiB leaves room for Portico itself and for Keycloak brokering
  many realms. Keep 6 GiB.
- The appliance image must include firewalld, podman, and
  `policycoreutils-python-utils`, pre-pull the Keycloak and PostgreSQL
  images (1.8 GiB) so first boot works without Internet, and set
  `TimeoutStartSec` on the Keycloak unit.
- `nginx.conf` here is the reference behaviour the Go proxy must match:
  Host-based routing, `X-Forwarded-*` for Keycloak, TLS SNI to Prism
  Central (`proxy_ssl_server_name on`), upgrade headers for WebSockets,
  no buffering for long-lived connections.

## How to re-measure after an upgrade

`lab/vm/provision.sh` then the `free -m` / `podman stats` commands above.
Bump the Keycloak image tag in `lab/vm/keycloak.container` first.
