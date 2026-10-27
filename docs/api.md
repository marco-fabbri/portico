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
| `POST /api/v1/tenants` | Create a tenant and start provisioning on its sites. Body: `{"slug": "acme", "display_name": "ACME Cloud", "contacts": "ops@acme.example", "sites": [{"site": "az-it-mi1"}, {"site": "az-us-nyc1", "cluster_ext_id": "…", "storage_container_ext_id": "…"}]}`; the slug is lower-case letters, digits and dashes, 1 to 32 characters; a placement without cluster or container takes the site's defaults; without the `sites` key the tenant goes on the only site when exactly one is configured and on none otherwise; `"sites": []` creates a tenant connected to no site, to connect later | 202 with the tenant in status `creating`; 400 on a bad slug, an unknown site or no placement target; 409 when it exists |
| `GET /api/v1/tenants/{id}` | One tenant | 200; 404 |
| `GET /api/v1/tenants/{id}/steps` | The recorded provisioning steps (`site`, `name`, `ok`, `detail`, `at`; `site` is empty for the Keycloak group) | 200 |
| `GET /api/v1/tenants/{id}/visibility` | The static visibility check on every site, read back from each Prism Central: the group holds one project-scoped role in its project and nothing else | 200 `{"sites": {"<site>": {"ok": true, "findings": []}}}`; 404 |
| `POST /api/v1/tenants/{id}/provision` | Retry provisioning on every site that is not active (idempotent steps) | 202; 409 unless the tenant is `failed` or a site of an active tenant failed |
| `POST /api/v1/tenants/{id}/sites/{site}/retry` | Retry one connection only: a `failed` one is provisioned again, a `remove-failed` one is disconnected again (counting first) | 202; 404; 409 in any other status; as `DELETE` below for a `remove-failed` one |
| `POST /api/v1/tenants/{id}/sites` | Place an active tenant on one more site: `{"site", "cluster_ext_id", "storage_container_ext_id"}` | 202; 400; 409 when already placed there |
| `GET /api/v1/tenants/{id}/sites/{site}/contents` | What the tenant's project holds on that site, counted with the service account per kind (VMs, images, templates, OVAs, volume groups, subnets, VPCs, floating IPs, security policies, categories, recovery points; what is shared into the project from elsewhere is not counted). A kind Prism Central will not list for the service account (403 or 400) is in `uncounted` | 200 `{"counts": {"vms": 2, …}, "uncounted": ["security-policies"], "empty": false}`; 503 when a kind cannot be counted |
| `DELETE /api/v1/tenants/{id}/sites/{site}` | Disconnect the tenant from one site, the last one included: refused while the project holds anything; otherwise Portico removes the role memberships, the identity provider's sharing, the SAML group, the resource group and the project, then the connection. When Prism Central itself refuses to delete the project (it holds something Portico could not count), the connection ends `remove-failed` and `retry` disconnects again. Portico never deletes VMs | 202 `{"id", "site", "status": "removing"}`; 404 when not connected there; 409 `{"error", "counts"}` when the project is not empty; 503 when the count fails |
| `POST /api/v1/tenants/{id}/sites/{site}/suspend` | Suspend an active connection for maintenance: Portico stops routing the tenant to that site (a new request, a new VM console included, is refused; an open console finishes); nothing changes in Prism Central and the VMs keep running | 200 with the connection (`status: suspended`); 404 when not connected there; 409 unless active |
| `POST /api/v1/tenants/{id}/sites/{site}/resume` | Resume a suspended connection: the same project serves again, nothing is re-created | 200 with the connection (`status: active`); 409 unless suspended |
| `POST /api/v1/tenants/{id}/disable` | Disable the Keycloak group: its users cannot log in | 202 |
| `POST /api/v1/tenants/{id}/enable` | Enable it again | 202 |
| `DELETE /api/v1/tenants/{id}` | Delete a tenant connected to no site: its Keycloak users, its Keycloak group, then the tenant itself. Disconnect it from every site first (`DELETE …/sites/{site}`), which is where projects are deleted | 202 with `{"id", "status": "deleting"}`; 409 while it is still connected to a site |
| `POST /api/v1/tenants/{id}/users` | Create the tenant's user in Keycloak. Body: `{"email", "first_name", "last_name"}`; the email is the username and the SAML `NameID` | 201 `{"id", "email", "temporary_password"}`, shown once; the user must change it at the first login; 409 when the email exists |

A connection's `status` is `creating`, `active`, `suspended`, `failed`,
`removing` or `remove-failed`. A suspension or a resume takes effect at
once on the node that made it; the pair's other node sees it within its
connection cache, 5 s.

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
(default: the code in lower case) never changes; the code can. Every call
takes effect at once, without Apply: a site added, changed or removed is
served, changed or dropped on this node, and the other node of a pair
picks the change up within 30 seconds. Its certificate is trusted by the
system roots or its CA (`tls_verify`), or pinned (`tls_pin`, the SHA-256
of the leaf certificate as `AB:CD:…`, which wins); a new site with
neither is refused, and verification cannot be turned off any more.

| Method and path | What it does | Answers |
|---|---|---|
| `GET /api/v1/sites` | `{"sites": [...]}`, the sites in order: `id`, `code`, `name`, `order`, `url`, `tls_verify`, `tls_pin`, `ca_pem`, `api_key_set`, `saml_idp_name`, `saml_entity_id`, `cluster_ext_id`, `storage_container_ext_id` (defaults for new tenants), `tenants`; in running mode also `status` (`available`, `maintenance`, `unreachable` after three failed probes in a row, `incomplete` when the site is not set up to the end or Prism Central or Keycloak says it no longer is), `maintenance`, `active`, `idle` | 200 |
| `POST /api/v1/sites/inspect` | Read the certificate an address presents, before anything is stored or any credential travels: `{"url", "ca_pem"}`. One TLS handshake, no HTTP request | 200 `{"trusted", "verify_error", "issuer", "subject", "fingerprint", "names", "not_after"}`; 400 when the URL is not https; 502 when nothing answers |
| `POST /api/v1/sites` | Add a site: `{"code", "name", "order", "url", "tls_pin", "ca_pem", "api_key", "saml_idp_name", "cluster_ext_id", "storage_container_ext_id"}` (all but the code and URL optional). With `"admin": {"username", "password"}` (and `"service_account"`, default `portico`), the site is set up in the same call, as `…/setup` below; the credentials are used for that run and not stored | 201 with the site, or with admin `{"site", "steps", "complete", "idp_changed"}`; 400 when an https site has `tls_verify` false and no `tls_pin`, or on a bad field (a certificate that does not verify stops the setup at its first step, and the site stays to be set up); 409 on a duplicate id or code, or with admin before the public names and the Keycloak realm are set |
| `GET /api/v1/sites/{id}`, `PUT /api/v1/sites/{id}` | One site; replace its fields (not the id). An empty `api_key` or `saml_idp_name` keeps the stored one; `tls_pin` is ignored (`…/pin` below) | 200; 404; 400 when `tls_verify` would go from true to false; 409 |
| `POST /api/v1/sites/{id}/setup` | Set the site up in one run (Finish setup, Repair): `{"username", "password", "service_account", "pin"}`, the credentials of a Prism Central account allowed to create users, roles and identity providers, used for this run only. Steps, each idempotent: `certificate` (trusted, the stored pin, or `pin` when it is the presented fingerprint, which pins it; before any credential travels), `version` (pc.7.6 or later), `rights` (the operations the next steps use, for a local account), `identity-provider` (another provider in Prism Central stops the run), `service-account` (a stored key that still works is kept), `saml-trust`, `defaults` (does not stop the run), `live`. A failed step stops the run and keeps what was done. After a complete run the tenants connected to the site are bound again to its identity provider, one `tenant <slug>` line each | 200 `{"site", "steps": [{"name", "status", "message"}], "complete", "idp_changed"}` (`status` is `ok`, `kept`, `skipped` or `failed`); 404; 409 before the public names and the Keycloak realm are set or on missing credentials |
| `POST /api/v1/sites/{id}/pin` | Pin the certificate the site presents now: `{"fingerprint"}`, the one the MSP compared | 200 with the site; 409 `{"error", "fingerprint"}` when Prism Central presents another one |
| `POST /api/v1/sites/{id}/status` | Check the site now in Prism Central and Keycloak, never from Portico's own row: `connection` (`version`, `trust`: `verified`, `pinned`, `unverified` or `none`, `fingerprint`, `not_after`), `service_account` (the stored key authenticates), `saml` (Portico's provider in Prism Central with this site's issuer, and the Keycloak client), `defaults` (the stored cluster and container exist); each part has `ok`, `message`, `action` and `unknown` (not checked). In running mode also `status`, `reason`, `new_fingerprint`, after the monitor took the result: the call changes the site's state, so it is a POST (no body) | 200; 404 |
| `DELETE /api/v1/sites/{id}` | Remove a site no tenant is connected to (a failed connection included). Portico deletes the site's Keycloak SAML client, its maintenance flag and its row. With the optional body `{"username", "password"}` of a Prism Central administrator, checked after the certificate, it also deletes there its identity provider, after the SAML users Prism Central made at sign-in through it (counted, not named; a refusal gives Prism Central's status and code, not its text), and the service account's API key (the account stays); a deletion that fails is listed as left with its reason | 200 `{"removed", "deleted": [...], "left_in_prism_central": [...]}` (without credentials: the identity provider, the service account and its key); 500 `{"error", "deleted"}` when Keycloak, the maintenance flag or the store fails after something was deleted (removing again finishes); 404; 409 naming each connected tenant and its state, while a tenant operation runs, or when the certificate or the credentials are refused (nothing is removed) |
| `POST /api/v1/sites/{id}/forget` | For a Prism Central gone for good: `{"confirm": "<code>"}`. Portico deletes its records of the site and of the placements on it and the site's Keycloak client, makes no call to that Prism Central, and lists what stays there | 200 `{"forgotten", "left_in_prism_central": [...]}`; 400 when the confirmation is not the code; 409 while a tenant operation runs |
| `POST /api/v1/sites/{id}/check` | Live check of the site's Prism Central with the stored connection plus the body's `url`, `ca_pem`, `api_key`. The stored key goes only to the stored host. A Prism Central older than pc.7.6 is refused | 200 `{"ok", "message", "version", "clusters", "containers"}` |
| `POST /api/v1/sites/{id}/bootstrap/prism-central` | Advanced, the earlier two-step path (`…/setup` does both): create Portico's service account on the site's Prism Central and its API key from an account that can create users and edit authorization policies: `{"username", "password", "service_account"}` (and `url`, `tls_verify`, `ca_pem` to replace the connection). Only the new key is stored | 200; 404; 409 |
| `POST /api/v1/sites/{id}/bootstrap/saml` | Advanced, as above: create the SAML trust both ways on the site: the identity provider in its Prism Central (`{"name", "username", "password"}`, the credentials only when the service account's role is refused, as on pc.7.6) and the SAML client in Keycloak. Refused when that Prism Central already has another identity provider | 200; 404; 409 |
| `PUT /api/v1/sites/{id}/maintenance` | `{"maintenance": true}`: no new tenant session enters the site; sessions already on it continue | 200 `{"site", "maintenance", "status"}` |
| `GET /api/v1/sites/{id}/sessions` | Tenant sessions on the site through Portico: `active` (a request in the last two minutes) and `idle` counts, and per session `tenant`, `email`, `since`, `last_activity`, `state`. Administrators working on Prism Central directly are not counted | 200 |
| `POST /api/v1/sites/{id}/sessions/disconnect` | End the routing of every session on the site: their next request goes to the site page or the maintenance page | 200 `{"disconnected": n}` |

## Branding

| Method and path | What it does | Answers |
|---|---|---|
| `GET /api/v1/settings/branding` | `{"msp_name", "tagline", "accent_color", "logo_type", "logo_data", "icon_data", "min_display_ms"}`, the logo and the tab icon base64-encoded | 200 |
| `PUT /api/v1/settings/branding` | Replace the branding; the logo is PNG, JPEG or SVG up to 256 KiB, the SVG must be static drawing (no scripts, event handlers, animation, DTD, or links and styles that load anything outside the file); the tab icon is a PNG of 128 to 1024 px on its longer side, the shorter at least half of it, up to 256 KiB, centred on a square (empty: Portico's arch on the accent colour). Writes the Keycloak login theme | 200, with `warning` when the theme directory could not be written; 400 |
| `GET /api/v1/settings/branding/logo` | The logo as an image | 200; 404 |
| `GET /api/v1/settings/branding/icon` | The uploaded tab icon (PNG) | 200; 404 |

`lab/tenants/api.sh METHOD PATH [JSON]` calls this API on the lab VM with
the credentials in a curl config file.
