# Admin JSON API

The admin API lives on the internal listener (`internal.admin_listen`,
default `127.0.0.1:8443`) under `/api/v1/`. It is the same surface the
admin UI uses, meant for scripts: every call is HTTP Basic with the
break-glass account (`admin` and the password set with
`portico admin set-password`), over TLS when the listener is off the
loopback. Repeated authentication failures from one address are delayed.
Bodies are JSON; errors come back as `{"error": "<message>"}` with the
status codes below. In setup mode (settings incomplete) the tenant calls
and the live site calls (maintenance, sessions) answer 503 with the
setup-mode message; the settings and site configuration calls work.

Secrets are write-only: a `PUT` carries them, a `GET` answers
`*_set: true` instead of the value. Nothing in a response is a password,
a key or a token, except the one-time temporary password of a new user.

## Tenants

| Method and path | What it does | Answers |
|---|---|---|
| `GET /api/v1/tenants` | List tenants | 200, array of tenants |
| `POST /api/v1/tenants` | Create a tenant and start provisioning on its sites. Body: `{"slug": "acme", "display_name": "ACME Cloud", "contacts": "ops@acme.example", "sites": [{"site": "az-it-mi1"}, {"site": "az-us-nyc1", "cluster_ext_id": "…", "storage_container_ext_id": "…"}]}`; the slug is lower-case letters, digits and dashes, 1 to 32 characters; a placement without cluster or container takes the site's defaults; `sites` may be left out when exactly one site is configured | 202 with the tenant in status `creating`; 400 on a bad slug, an unknown site or no placement target; 409 when it exists |
| `GET /api/v1/tenants/{id}` | One tenant | 200; 404 |
| `GET /api/v1/tenants/{id}/steps` | The recorded provisioning steps (`site`, `name`, `ok`, `detail`, `at`; `site` is empty for the Keycloak group) | 200 |
| `GET /api/v1/tenants/{id}/visibility` | The static visibility check on every site, read back from each Prism Central: the group holds one project-scoped role in its project and nothing else | 200 `{"sites": {"<site>": {"ok": true, "findings": []}}}`; 404 |
| `POST /api/v1/tenants/{id}/provision` | Retry provisioning on every site that is not active (idempotent steps) | 202; 409 unless the tenant is `failed` or a site of an active tenant failed |
| `POST /api/v1/tenants/{id}/sites` | Place an active tenant on one more site: `{"site", "cluster_ext_id", "storage_container_ext_id"}` | 202; 400; 409 when already placed there |
| `DELETE /api/v1/tenants/{id}/sites/{site}?delete_project=true` | Take the tenant off one site (the project too only with `delete_project=true`) | 202; 404 when not placed there; 409 for the last site |
| `POST /api/v1/tenants/{id}/disable` | Disable the Keycloak group: its users cannot log in | 202 |
| `POST /api/v1/tenants/{id}/enable` | Enable it again | 202 |
| `DELETE /api/v1/tenants/{id}?delete_project=true` | Delete the tenant on every site: role membership, SAML group, then the Keycloak group; the projects too only with `delete_project=true`. Portico never deletes VMs | 202 with `{"id", "status": "deleting", "delete_project"}` |
| `POST /api/v1/tenants/{id}/users` | Create the tenant's user in Keycloak. Body: `{"email", "first_name", "last_name"}`; the email is the username and the SAML `NameID` | 201 `{"id", "email", "temporary_password"}`, shown once; the user must change it at the first login; 409 when the email exists |

A tenant:

```json
{"id": "acme", "display_name": "ACME Cloud", "status": "active", "contacts": "ops@acme.example",
 "keycloak_group_id": "…", "last_error": "", "created_at": "…", "updated_at": "…",
 "placements": [{"site": "az-it-mi1", "code": "AZ-IT-MI1", "status": "active",
   "cluster_ext_id": "…", "storage_container_ext_id": "…", "project_id": "portico-acme",
   "project_ext_id": "…", "resource_group_ext_id": "…", "saml_group_ext_id": "…",
   "role_membership_ext_id": "…", "idp_shared": true, "last_error": ""}]}
```

Tenant statuses: `creating`, `active`, `failed` (with `last_error`, naming
the sites that failed), `disabled`, `deleting`. Placement statuses:
`creating`, `active`, `failed`, `removing`.

## Settings

| Method and path | What it does | Answers |
|---|---|---|
| `GET /api/v1/settings` | The stored settings (or the file's while nothing is stored): `stored`, `source`, `mode`, `complete`, `missing`, the public names, `passthrough_site`, and the sections `keycloak`, `oidc`, `tenants` with `*_set` flags for the secrets. The sites are under `/api/v1/sites` | 200 |
| `PUT /api/v1/settings` | Replace the settings (the sites are kept). A secret left out keeps its stored value; `operation_timeout` is a duration such as `10m`. Refused with the field name when a value is invalid | 200; 400 |
| `POST /api/v1/settings/check` | Live check of Keycloak with the settings as stored plus the body's fields: `{"target": "keycloak" \| "oidc"}` | 200 `{"ok", "message"}` |
| `POST /api/v1/settings/bootstrap/keycloak` | Create the realm, the admin client and the OIDC client from the Keycloak administrator's credentials: `{"admin_username", "admin_password", "realm", "display_name"}`. The credentials are used for this call and not stored; the two client secrets are | 200 with the result; 400; 409 when the realm exists |
| `POST /api/v1/settings/apply` | Exit so that systemd restarts the service on the stored settings | 202; 409 when the settings are incomplete or not stored |

## Sites

A site is one Prism Central, shown to tenants by its code. Its id
(default: the code in lower case) never changes; the code can.
Configuration calls are stored at once and take effect at Apply; the
live calls (maintenance, sessions) take effect at once.

| Method and path | What it does | Answers |
|---|---|---|
| `GET /api/v1/sites` | `{"sites": [...]}`, the sites in order: `id`, `code`, `name`, `order`, `url`, `tls_verify`, `ca_pem`, `api_key_set`, `saml_idp_name`, `saml_entity_id`, `cluster_ext_id`, `storage_container_ext_id` (defaults for new tenants), `tenants`; in running mode also `status` (`available`, `maintenance`, `unreachable` after three failed probes in a row), `maintenance`, `active`, `idle` | 200 |
| `POST /api/v1/sites` | Add a site: `{"code", "name", "order", "url", "tls_verify", "ca_pem", "api_key", "saml_idp_name", "cluster_ext_id", "storage_container_ext_id"}` (all but the code and URL optional) | 201; 400; 409 on a duplicate id or code |
| `GET /api/v1/sites/{id}`, `PUT /api/v1/sites/{id}` | One site; replace its fields (not the id). An empty `api_key` or `saml_idp_name` keeps the stored one | 200; 404; 409 |
| `DELETE /api/v1/sites/{id}` | Remove a site no tenant is placed on | 204; 409 naming the tenants |
| `POST /api/v1/sites/{id}/forget` | For a Prism Central gone for good: `{"confirm": "<code>"}`. Portico deletes its records of the site and of the placements on it, makes no call to that Prism Central, and lists what stays there | 200 `{"forgotten", "left_in_prism_central": [...]}`; 400 when the confirmation is not the code; 409 while a tenant operation runs |
| `POST /api/v1/sites/{id}/check` | Live check of the site's Prism Central with the stored connection plus the body's `url`, `tls_verify`, `ca_pem`, `api_key`. The stored key goes only to the stored host. A Prism Central older than pc.7.6 is refused | 200 `{"ok", "message", "version", "clusters", "containers"}` |
| `POST /api/v1/sites/{id}/bootstrap/prism-central` | Create Portico's service account on the site's Prism Central and its API key from an account that can create users and edit authorization policies: `{"username", "password", "service_account"}` (and `url`, `tls_verify`, `ca_pem` to replace the connection). Only the new key is stored | 200; 404; 409 |
| `POST /api/v1/sites/{id}/bootstrap/saml` | Create the SAML trust both ways on the site: the identity provider in its Prism Central (`{"name", "username", "password"}`, the credentials only when the service account's role is refused, as on pc.7.6) and the SAML client in Keycloak. Refused when that Prism Central already has another identity provider | 200; 404; 409 |
| `PUT /api/v1/sites/{id}/maintenance` | `{"maintenance": true}`: no new tenant session enters the site; sessions already on it continue | 200 `{"site", "maintenance", "status"}` |
| `GET /api/v1/sites/{id}/sessions` | Tenant sessions on the site through Portico: `active` (a request in the last two minutes) and `idle` counts, and per session `tenant`, `email`, `since`, `last_activity`, `state`. Administrators working on Prism Central directly are not counted | 200 |
| `POST /api/v1/sites/{id}/sessions/disconnect` | End the routing of every session on the site: their next request goes to the site page or the maintenance page | 200 `{"disconnected": n}` |

## Branding

| Method and path | What it does | Answers |
|---|---|---|
| `GET /api/v1/settings/branding` | `{"msp_name", "tagline", "accent_color", "logo_type", "logo_data", "icon_data", "min_display_ms"}`, the logo and the tab icon base64-encoded | 200 |
| `PUT /api/v1/settings/branding` | Replace the branding; the logo is PNG, JPEG or SVG up to 256 KiB, the SVG is checked for scripts and external references; the tab icon is a square PNG of 180 to 1024 px, up to 256 KiB (empty: Portico's arch on the accent colour). Writes the Keycloak login theme | 200, with `warning` when the theme directory could not be written; 400 |
| `GET /api/v1/settings/branding/logo` | The logo as an image | 200; 404 |
| `GET /api/v1/settings/branding/icon` | The uploaded tab icon (PNG) | 200; 404 |

`lab/tenants/api.sh METHOD PATH [JSON]` calls this API on the lab VM with
the credentials in a curl config file.
