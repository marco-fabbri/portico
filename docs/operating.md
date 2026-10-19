# Operating the binary

What runs on the appliance today, before the image and the installer of
Milestones 3 and 4 wrap it. One static binary, one systemd unit, one
configuration file, one directory of state.

## Commands

| Command | What it does |
|---|---|
| `portico serve -config /etc/portico/portico.yaml` | Runs the three listeners: public (`:443`, or a loopback port behind `cloudflared`), admin (`internal.admin_listen`: health at `/healthz`, the admin UI, the JSON API) and the pass-through to Prism Central (`internal.passthrough_listen`, local login allowed) |
| `portico settings import -config <file>` | Moves the runtime sections of the file (public names, Prism Central, Keycloak, OIDC, tenants) and the contents of the secret files into the store, sealed with the master key. Run as the service user (it refuses root) so the key belongs to the user that runs `serve` |
| `portico settings show -config <file>` | Prints the stored settings as YAML, each secret as `<set>` or `<unset>` |
| `portico admin set-password -config <file>` | Sets the break-glass password of the admin UI and API, read from standard input, stored as a bcrypt hash |
| `portico version` | The version the binary was built with |

Exit codes of `serve`: 0 after "Apply and restart" (the unit has
`Restart=always`, so systemd starts it again on the stored settings); 2
on a configuration error, with the field named; 1 otherwise.

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

## Logs

Structured JSON on standard error (`log.level` in the file), read with
`journalctl -u portico`. The access log of the public path records
method, path, status, duration and whether a session was present; never
a header value, a cookie, a token or a password.

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
(`:443` with the pair) and tunnel mode (`127.0.0.1:8080`, no TLS), and
shows the Keycloak administrator credentials from
`/etc/portico/keycloak.env` on request. It re-runs at any time with the
current values as defaults; a wrong entry is refused with the field
named. Nothing else is configurable from the console.

On Apply, Portico writes `KC_HOSTNAME` for the identity host into
`/var/lib/portico/keycloak.hostname.env`; the unit
`keycloak-hostname.path` restarts Keycloak with it.
