# 29 — The general test before v0.1.0: a fresh appliance from the image

## Question

Before the first release (Milestone 6) Marco asked for a general test:
not the appliance upgraded in place since write-up 21, but the release
path itself. An image built by the CI from main, a new appliance from
that image, the bootstrap through the admin API with the lab scripts,
two sites, tenants created, disabled, enabled and deleted, maintenance
and disconnection, the local-login paths, the hardening, an upgrade in
place. What breaks on a machine nobody has touched?

## Measured on

- Prism Central: pc.7.6.0.6 on both sites (`AZ-IT-MI1`, NTNX-01
  10.23.210.175; `AZ-US-NYC1`, 10.23.210.185)
- Image: `portico-0.1.0-rc.1.qcow2`, workflow run 40238336960 of
  `.github/workflows/image.yml` on main `3873d91` (after PRs #38 and
  #39), started by hand with `workflow_dispatch`; the qcow2 (1.3 GB),
  its OpenSCAP report and the binary tarball downloaded as one artifact
  (629 MB), checksum verified
- Portico: `0.1.0-rc.1` from the image, then `dev-12a1a1e` and
  `dev-f37aafa` by the upgrade in place (this branch)
- Date: 2026-10-25, 23:03–00:15 CET (the clocks went back that night)
- By: Claude, through the admin API, the lab scripts and SSH; the
  browser check is Marco's

## Method

`source lab/env.sh` with `PORTICO_SECRETS=~/portico-secrets-appliance`,
`LAB_SSH_OPTS` for the appliance's host key and `LAB_IMAGE_HOST` (the VM
of Milestones 0 to 2, 10.23.210.190, still up, serves the qcow2 to Prism
Central over HTTP). Passwords from the secrets files only.

1. Teardown through the product (as write-up 28): acme off both sites,
   deleted, both sites removed with their administrators' credentials.
   `lab/appliance/destroy.sh`: VM and image gone from NTNX-01.
2. `lab/appliance/deploy.sh --image … --version 0.1.0-rc.1`: image
   service, VM, cloud-init network, break-glass password from the file.
   The lab certificate pair for the portal names installed as the
   public and the internal pair (the image makes its own self-signed
   ones at first boot). The Keycloak admin password of this first boot
   copied from `/etc/portico/keycloak.env` into the secrets directory.
3. `lab/portico/bootstrap-acceptance.sh` (one site in one step), Marco's
   branding back, acme on the site, the probe user.
4. `lab/sites/acceptance.sh --with-tenant`: maintenance, sessions,
   disconnection, the local-login probe, then
   `lab/tenants/acceptance.sh` for the tenant `beta` (create, visibility,
   user, disable, enable, delete).
5. `AZ-US-NYC1` in one step, acme placed on it
   (`POST /api/v1/tenants/acme/sites`), visibility, the probe user's
   login on each site.
6. Hardening read on the appliance: `systemd-analyze security portico`,
   the listening sockets, firewalld, SELinux, the sshd drop-ins.
7. `lab/portico/upgrade-appliance.sh` with this checkout: the upgrade in
   place of design 4.9.
8. carol again; the fixes of this write-up verified on the same
   appliance by a second upgrade in place.

## Result

- MEASURED: the CI built the image in 13 minutes; destroy 45 s; deploy
  296 s to `apply done` (the 1.3 GB upload to the image service
  included), `healthz` 200 at once, the break-glass password set over
  SSH by the script. Fourteen minutes from `destroy` to a reachable
  admin listener.
- MEASURED, release blocker, fixed: **the appliance came up with
  Keycloak dead.** The image carries no container image: at first boot
  `postgres.service` pulled `docker.io/library/postgres:16` while
  NetworkManager-wait-online had failed and the network was not up
  (DNS error at 22:33:15), podman exited 125 four times in four
  seconds, `Restart=always` kept trying and PostgreSQL started at
  22:34:14. `keycloak.service` had `Requires=postgres.service`: its
  start job failed with result `dependency` at 22:33:15 and systemd
  never retries a job, so Keycloak stayed `inactive (dead)` until
  `systemctl start keycloak` by hand (it then pulled its image and was
  up in 40 s). Fix: `Wants=postgres.service` with the same `After=`;
  Keycloak's own `Restart=always` covers a database still coming up.
  Pinned by `lab/test_scripts.sh`.
- MEASURED, known limit: the first boot needs the Internet (docker.io
  and quay.io) and takes about a minute after the network is up on the
  lab's 470 KB/s link. An appliance in a network without egress never
  gets its identity provider. For v0.1.0 the install guide says so;
  shipping the two container images inside the qcow2 is the real fix
  (backlog).
- MEASURED: `bootstrap-acceptance.sh` passed in 89 s: the Keycloak
  bootstrap 68 s (the realm's first creation on a fresh Keycloak), the
  site `AZ-IT-MI1` in one step 8 s, eight steps `ok`, checks ok, Apply,
  service back `running`. acme active on the site in 12 s. The probe
  user created and added to its group.
- MEASURED, release blocker, fixed: **the login page answered 500.**
  `lab/sites/acceptance.sh` got 302 where it expected the console, and
  `tenant-login.sh` showed hop 3,
  `GET /realms/portico/protocol/openid-connect/auth`, answering 500;
  Keycloak's log: `TemplateNotFoundException: Template not found for
  name "error.ftl"`. The theme directory written by Portico had
  `login/` and every directory below it at mode 0700 (`portico:portico`)
  while the files were 0644: Milestone 5 gave `portico.service`
  `UMask=0077`, and `os.MkdirAll(…, 0o755)` is subject to the umask,
  while the files had an explicit `Chmod`. Keycloak's user (uid 1000)
  in the container could not read `theme.properties`, the theme lost
  its `parent=keycloak.v2` and no template was found. `chmod 0755` on
  the directories made the login reach `/console/` (200, 19 hops) at
  once. Fix: every theme directory is `Chmod 0755` after `MkdirAll`
  (`TestWriteThemeDirsReadableUnderUmask` runs the writer under umask
  077); verified by setting the directories back to 0700 and upgrading
  in place: Portico rewrote the theme at start with 0755 and both logins
  reached the console. The "check keycloak" and "check oidc" lines of
  the setup pass with a broken login page: they call the service
  endpoints, not the page (backlog: the OIDC check could fetch the
  realm's login page and expect 200).
- MEASURED: `lab/tenants/acceptance.sh` on the fresh appliance for
  `beta`: every line matched, create to delete, the `_internal` project
  unchanged. `lab/auth/probe-local-login.sh`: 17 requests, every
  local-login path refused on the public name.
- MEASURED: `AZ-US-NYC1` in one step 6 s; acme placed on it in 22 s;
  visibility ok on both sites.
- MEASURED: hardening on the image: `portico.service` exposure 1.7
  (`OK`), SELinux enforcing, firewalld services `portico` and `ssh`
  only, listening: sshd on 22, Portico on 443, 8443 and the loopback
  9440 pass-through, Keycloak on the loopback 8080 only, aardvark-dns
  on the container network; sshd drop-ins `00-complianceascode-hardening.conf`
  and `00-portico.conf`, no `10-portico-listen.conf` (one interface:
  the console had nothing to restrict).
- MEASURED: the upgrade in place from `0.1.0-rc.1` to the checkout's
  binary took 156 s, backup of the master key and the store first,
  "a quadlet changed: restarting PostgreSQL and Keycloak" (the
  `Wants=` fix), settings back `running`, both sites available. The
  second upgrade (the theme fix) 129 s. Each upgrade restarts Keycloak
  when its quadlet changed, so Portico's start logged "theme not
  applied at start" (the token request hit a Keycloak still booting):
  the theme is saved and applied at the next save; minor, backlog.
- MEASURED: `lab/sites/acceptance.sh` one-site part, run again after
  the theme fix with `AZ-US-NYC1` removed through the product for the
  twenty seconds it takes: every line matched — the probe's login 200,
  maintenance on, its session listed active, a new login during
  maintenance 503 with the maintenance page, the session already on
  the site still 200, `sessions/disconnect` 2, the disconnected
  session's next request 503, maintenance off, a login after it 200,
  the local-login probe pass. `AZ-US-NYC1` back in one step in 20 s,
  acme placed on it again in 12 s.
- MEASURED: NTNX-01 was intermittent all evening: two 504s on the count
  before a disconnect (the third try passed), the visibility read and
  a login timing out right after the re-add (`az-it-mi1` shown
  `unreachable` by the monitor for a while), as in write-ups 25 and
  28. Portico showed it as such and recovered by itself.
- MEASURED: carol created again (201); `carol@acme.example` and the
  probe user `portico-probe-acme@portico.test` are acme's users.

## Implications for the design

- A first boot without the Internet is a known limit of v0.1.0; the
  design 4.9 ("container image tags for Keycloak/PostgreSQL") should say
  the images are pulled at first boot until they ship in the qcow2.
- The service hardening of Milestone 5 (`UMask=0077`) is right for the
  store and the secrets; anything Portico writes for another process
  (the theme for Keycloak) needs explicit modes. The theme writer is
  the only such place today.
- `healthz` 200 means the Go binary answers, not that the appliance can
  log anyone in: the setup page's status card shows Keycloak's state,
  and `deploy.sh` could wait for it (lab script; minor).

## How to re-measure after an upgrade

1. `gh workflow run image.yml --ref main -f version=<v>`; download the
   artifact into `lab/appliance/artifact/`.
2. Teardown through the product, `lab/appliance/destroy.sh`,
   `lab/appliance/deploy.sh --image lab/appliance/artifact/portico-<v>.qcow2 --version <v>`
   (`LAB_IMAGE_HOST` set to a host Prism Central can download from).
3. On the appliance: `systemctl is-active postgres keycloak portico`
   within two minutes of the first boot — all three active.
4. The lab certificate pair as public and internal pair, the Keycloak
   admin password from `/etc/portico/keycloak.env` into
   `PORTICO_SECRETS/keycloak-admin-pw`, then
   `lab/portico/bootstrap-acceptance.sh`,
   `lab/sites/tenant-login.sh --ensure-probe acme`,
   `lab/sites/acceptance.sh --with-tenant`: every line `ok`, the last
   hop of a login `200 https://msp.portico.test/console/`.
