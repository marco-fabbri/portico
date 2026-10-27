# ADR 0007: The exposure and the Keycloak realm are set up in the admin UI

- Status: accepted (2026-10-26)
- Deciders: Marco, Claude

## Context

Design 4.8 kept the console to the network and the status. Milestone 3
added two screens to it: the exposure (the certificate and key given as
paths on the appliance, a self-signed pair, the Cloudflare token) and the
Keycloak administrator's credentials, shown once to be typed into the
setup page's Identity card. On 2026-10-26 Marco installed a release
candidate from zero as an MSP would, following `docs/install.md` only,
and stopped at both: the certificate had to reach the appliance by `scp`
with an SSH key the guide had not asked for, its paths typed on the
console; the Keycloak password had to be copied by hand from one screen
to another. The console was also the only place that said which card
came next, under a name the page did not use.

## Decision

1. **The console is back to design 4.8**: network, break-glass password,
   status and the admin UI's address. Its exposure and Keycloak screens
   are removed.
2. **The exposure is on the setup page**, in the first card ("Public
   names and exposure"): upload a certificate and key, a self-signed pair
   for a first try, or the tunnel token. The page checks a pair before
   anything is written (names, chain order, key match, passphrase,
   validity, keys and signatures browsers accept, 64 KiB per file) and
   never echoes or logs a key or a token.
3. **Root acts through a path unit.** The service is unprivileged and
   cannot write `/etc/portico/tls`, the tunnel token or restart a unit.
   The page writes the files and then `request.json` into
   `/var/lib/portico/exposure/` (0700, `portico`), each aside and renamed
   into place (`PathChanged=` fires on the rename, measured on systemd
   252), and removes what it wrote when a write fails; `portico-exposure.path`
   runs `portico exposure apply` as root, which checks the pair again,
   installs it, switches the mode and deletes the request. It is the
   pattern of `keycloak-hostname.path`.
4. **A renewal keeps the sessions.** The public listener reloads its pair
   when the file changes; only a mode change restarts Portico.
5. **Portico creates the realm by itself.** The first boot writes a copy
   of Keycloak's temporary administrator readable by `portico` alone.
   Once the public names are saved, Portico creates the realm `portico`
   and its clients with it, stores their secrets, then deletes the
   temporary administrator from Keycloak and the copy. The Identity card
   shows the job; "Use an existing realm" keeps the typed path.

## Consequences

- The install is the console for the network, then the browser. The SSH
  key becomes optional.
- "Portico never stores a human administrator's credentials" holds: the
  temporary administrator is a machine account created by the first
  boot, its copy lives on the appliance only until the realm exists, and
  Keycloak no longer has it afterwards. The master realm then has no
  administrator; `docs/operating.md` gives the `kc.sh bootstrap-admin`
  command for a maintenance task (measured on Keycloak 26.3).
- One more root unit on the appliance, triggered by a file the service
  owns. `apply` trusts nothing in the request: it validates the pair
  and the token itself; it opens the request directory once, refusing it when it is
  a link, and works through that handle, so swapping the directory for
  a link while `apply` runs does not move it elsewhere; in it, it reads
  only regular files, never through a link and at most 64 KiB each, and
  replaces `result.json` by a rename, so a link planted there by the
  service user is not followed; outside it, it writes only the public
  pair, the tunnel token and the YAML's public block.
- A mode change restarts Portico and the admin sessions with it (they
  live in memory): the page waits on `/exposure/ready`, which needs no
  session and says only whether a change is still being applied. A
  request nobody applied within two minutes is reported and can be
  replaced.
- A tunnel token is judged by Cloudflare registering `cloudflared`'s
  connection within 30 seconds, read on its readiness endpoint, whose
  address the unit pins. Staying up is not enough: with a token
  Cloudflare does not know, `cloudflared` 2026.10.4 keeps retrying
  without exiting (measured).
- ACME (design 4.6) is still not built; it would plug into the same
  request.
