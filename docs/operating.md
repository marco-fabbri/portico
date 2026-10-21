# Operating the binary

What runs on the appliance today, before the image and the installer of
Milestones 3 and 4 wrap it. One static binary, one systemd unit, one
configuration file, one directory of state.

## Commands

| Command | What it does |
|---|---|
| `portico serve -config /etc/portico/portico.yaml` | Runs the three listeners: public (`:443`, or a loopback port behind `cloudflared`), admin (`internal.admin_listen`: health at `/healthz`, the admin UI, the JSON API) and the pass-through to Prism Central (`internal.passthrough_listen`, local login allowed) |
| `portico settings import -config <file>` | Moves the runtime sections of the file (public names, Prism Central, Keycloak, OIDC, tenants) and the contents of the secret files into the store, sealed with the master key; the file's Prism Central becomes site `site-1` (code `SITE-1`). Run as the service user (it refuses root) so the key belongs to the user that runs `serve` |
| `portico settings show -config <file>` | Prints the stored settings as YAML, each secret as `<set>` or `<unset>` |
| `portico admin set-password -config <file>` | Sets the break-glass password of the admin UI and API, read from standard input, stored as a bcrypt hash; a password set this way needs no change at login |
| `portico version` | The version the binary was built with |

Exit codes of `serve`: 0 after "Apply and restart" (the unit has
`Restart=always`, so systemd starts it again on the stored settings); 2
on a configuration error, with the field named; 1 otherwise.

## The factory password

A store without an admin password gets `admin` / `portico/4u` when
`serve` starts, flagged "to be changed": the admin UI lets that session
open only the password page until a new one (at least 12 characters, not
the factory one) is set, and the API answers 403 with the reason. The
flag is cleared by the password page and by `portico admin set-password`.

## Files

| Path | Owner, mode | What it is |
|---|---|---|
| `/etc/portico/portico.yaml` | root:portico 0640 | Process settings: listeners, TLS files, logging. See `deploy/portico.yaml.example`; the runtime sections are read from here only until they are stored |
| `/var/lib/portico/portico.db` | portico | SQLite store: settings, sealed secrets, tenants, provisioning steps, sessions, branding |
| `/var/lib/portico/master.key` | portico 0600 | Created by the binary at first start; seals the secrets in the store. It never leaves the appliance. Without it the store's secrets are unreadable: back it up with the store, separately from any copy you hand around |
| `/var/lib/portico/keycloak-theme/` | portico | The Keycloak login theme written from the branding; mounted into the Keycloak container as `/opt/keycloak/themes/portico` |
| `deploy/portico.service` | | The unit: user `portico`, `CAP_NET_BIND_SERVICE` for port 443, hardened, `Restart=always`, no start limit |

Backup: copy `/var/lib/portico` with the service stopped, or the `.db`
with its `-wal` file. Restore the key with the store.

## First start of a fresh appliance

With no runtime settings in the file and an empty store, `serve` starts
in **setup mode**: the public listener is off, the admin listener answers
the setup page, and the journal lists what is missing. The setup page (or
the JSON API) configures the appliance card by card and can create
Portico's service account in Prism Central, the Keycloak realm and
clients and the SAML trust from one-time credentials; "Apply and restart"
opens the public path. Measured on the lab: 41 s from empty store to
running (write-up 15). Filling the file by hand with pre-created accounts
remains possible (`deploy/portico.yaml.example`, then `settings import`).

## Sites

A site is one Prism Central behind Portico, shown to tenants by its code
(`AZ-IT-MI1`) on the one public name. The setup page lists the sites;
each has its own page.

- **Add**: code, name, Prism Central URL; then the site's service account
  and SAML trust from its page, and the default cluster and storage
  container for new tenants. Apply to start serving it. A site not yet
  complete (no key or no SAML trust) is not served and is listed as such
  on the setup page; it never stops the other sites, also across a
  restart.
- **Tenants on sites**: a tenant is placed on the sites where it gets
  resources, each with its project; a site can be added to or removed
  from a tenant later (never its last one). The tenant's user signs in
  once and, with several sites, chooses one on the site page.
- **Maintenance**: "Put in maintenance" on the site's page (or
  `PUT /api/v1/sites/{id}/maintenance`) takes effect at once: no new
  tenant session enters the site; those already on it keep working. The
  page shows how many users are still connected through Portico, active
  (a request in the last two minutes: an open console tab, even hidden,
  polls at least once a minute) or idle, and refreshes every 10 s. A
  console left open leaves the count when Prism Central signs it out
  after about 15 minutes without input. When the count does not drop,
  "Disconnect the remaining users" ends their routing to the site: their
  next request lands on the maintenance page. Administrators working on
  Prism Central directly are not counted.
- **Unreachable**: Portico probes every site every 30 s through its
  service account (10 s timeout). After three failed probes in a row,
  about a minute and a half, the site is unreachable: its tile is greyed
  out and a tenant whose only site it is gets the "site unreachable"
  page; sessions already on it keep being proxied. The first answer
  makes it available again. One slow answer does not count: on NTNX-01
  the call took up to 30 s for a few minutes after a restart, then
  0.6 s again.
- **Remove**: a site without tenants is removed from its page. A site
  whose Prism Central is gone for good is **forgotten**: type its code,
  Portico deletes its records of the site and of the tenants placed on
  it without contacting that Prism Central, and lists what stays there
  (projects, groups, role memberships, the identity provider, its
  service account) for a clean-up if it ever comes back.
- **Upgrade**: a store written by a single-Prism Central release becomes
  site `site-1`, code `SITE-1`, with every tenant placed on it; rename
  the code on the site's page.

## Logs

Structured JSON on standard error (`log.level` in the file), read with
`journalctl -u portico`. The access log of the public path records
method, path, status, duration and whether a session was present; never
a header value, a cookie, a token or a password. Site changes are logged
at warning level: maintenance on and off, disconnections (with the
count), a forgotten site with what it left behind.

## Upgrading

Replace the binary and restart the unit; the store format is versioned
and migrated at start. The lab does this with `lab/portico/deploy.sh`.

## The console wizard

`portico console` (the appliance's tty1, through a getty override; on
your own host run it as root in a terminal) is the only place that needs
root: it writes NetworkManager keyfiles and applies them with `nmcli`,
sets the break-glass password by running `portico admin set-password` as
the `portico` user, installs the public certificate and key in
`/etc/portico/tls/` or the tunnel token in `/etc/cloudflared/token` and
switches the `public` block of the process YAML between direct mode
(`:443` with the pair) and tunnel mode (`127.0.0.1:8081`, no TLS: 8080 is Keycloak's published port; point the Cloudflare tunnel's ingress at `http://127.0.0.1:8081`), and
shows the Keycloak administrator credentials from
`/etc/portico/keycloak.env` on request. It re-runs at any time with the
current values as defaults; a wrong entry is refused with the field
named. Nothing else is configurable from the console.

Whenever the settings are saved with an identity host (the public names
card, and Apply), Portico writes `KC_HOSTNAME` for it into
`/var/lib/portico/keycloak.hostname.env`; the unit
`keycloak-hostname.path` restarts Keycloak with it (about a minute). The
Keycloak bootstrap waits for Keycloak to answer again, up to two minutes,
and says so when it does not.
