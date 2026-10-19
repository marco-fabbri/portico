# Admin JSON API

The admin API lives on the internal listener (`internal.admin_listen`,
default `127.0.0.1:8443`) under `/api/v1/`. It is the same surface the
admin UI uses, meant for scripts: every call is HTTP Basic with the
break-glass account (`admin` and the password set with
`portico admin set-password`), over TLS when the listener is off the
loopback. Repeated authentication failures from one address are delayed.
Bodies are JSON; errors come back as `{"error": "<message>"}` with the
status codes below. In setup mode (settings incomplete) every call
outside `/api/v1/settings*` answers 503 with the setup-mode message.

Secrets are write-only: a `PUT` carries them, a `GET` answers
`*_set: true` instead of the value. Nothing in a response is a password,
a key or a token, except the one-time temporary password of a new user.

## Tenants

| Method and path | What it does | Answers |
|---|---|---|
| `GET /api/v1/tenants` | List tenants | 200, array of tenants |
| `POST /api/v1/tenants` | Create a tenant and start provisioning. Body: `{"slug": "acme", "display_name": "ACME Cloud", "contacts": "ops@acme.example"}`; the slug is lower-case letters, digits and dashes, 1 to 32 characters | 202 with the tenant in status `creating`; 400 on a bad slug; 409 when it exists |
| `GET /api/v1/tenants/{id}` | One tenant | 200; 404 |
| `GET /api/v1/tenants/{id}/steps` | The recorded provisioning steps (`name`, `ok`, `detail`, `at`) | 200 |
| `GET /api/v1/tenants/{id}/visibility` | The static visibility check, read back from Prism Central: the group holds one project-scoped role in its project and nothing else | 200 `{"ok": true}` or `{"ok": false, "findings": [...]}`; 404 |
| `POST /api/v1/tenants/{id}/provision` | Retry provisioning after a failure (idempotent steps) | 202; 409 unless the tenant is `failed` |
| `POST /api/v1/tenants/{id}/disable` | Disable the Keycloak group: its users cannot log in | 202 |
| `POST /api/v1/tenants/{id}/enable` | Enable it again | 202 |
| `DELETE /api/v1/tenants/{id}?delete_project=true` | Delete the tenant: role membership, SAML group, Keycloak group; the project too only with `delete_project=true`. Portico never deletes VMs | 202 with `{"id", "status": "deleting", "delete_project"}` |
| `POST /api/v1/tenants/{id}/users` | Create the tenant's user in Keycloak. Body: `{"email", "first_name", "last_name"}`; the email is the username and the SAML `NameID` | 201 `{"id", "email", "temporary_password"}`, shown once; the user must change it at the first login; 409 when the email exists |

A tenant:

```json
{"id": "acme", "display_name": "ACME Cloud", "status": "active", "contacts": "ops@acme.example",
 "project_id": "portico-acme", "project_ext_id": "…", "resource_group_ext_id": "…",
 "saml_group_ext_id": "…", "keycloak_group_id": "…", "role_membership_ext_id": "…",
 "idp_shared": true, "last_error": "", "created_at": "…", "updated_at": "…"}
```

Statuses: `creating`, `active`, `failed` (with `last_error`), `disabled`,
`deleting`.

## Settings

| Method and path | What it does | Answers |
|---|---|---|
| `GET /api/v1/settings` | The stored settings (or the file's while nothing is stored): `stored`, `source`, `mode`, `complete`, `missing`, and the sections `prism_central`, `keycloak`, `oidc`, `tenants` with `*_set` flags for the secrets | 200 |
| `PUT /api/v1/settings` | Replace the settings. A secret left out keeps its stored value; `tls_verify` left out keeps its value; `operation_timeout` is a duration such as `10m`. Refused with the field name when a value is invalid (a Prism Central URL with credentials, for one) | 200; 400 |
| `POST /api/v1/settings/check` | Live check of one target with the settings as stored plus the body's fields: `{"target": "prism_central" \| "keycloak" \| "oidc"}`. Prism Central answers the clusters and storage containers it offers | 200 `{"ok", "message", "clusters", "containers"}` |
| `POST /api/v1/settings/bootstrap/keycloak` | Create the realm, the admin client and the OIDC client from the Keycloak administrator's credentials: `{"admin_username", "admin_password", "realm", "display_name"}`. The credentials are used for this call and not stored; the two client secrets are | 200 with the result; 400; 409 when the realm exists |
| `POST /api/v1/settings/bootstrap/prism-central` | Create Portico's service account and its API key from an account that can create users and edit authorization policies: `{"username", "password", "url", "tls_verify", "ca_pem", "service_account"}`. Only the new key is stored | 200; 400; 409 when the account exists |
| `POST /api/v1/settings/bootstrap/saml` | Create the SAML trust both ways: the identity provider in Prism Central (`{"name", "username", "password"}`, the credentials only when the service account's role is refused, as on pc.7.6) and the SAML client in Keycloak. Refused when Prism Central already has an identity provider | 200; 409 |
| `POST /api/v1/settings/apply` | Exit so that systemd restarts the service on the stored settings | 202; 409 when the settings are incomplete or not stored |

## Branding

| Method and path | What it does | Answers |
|---|---|---|
| `GET /api/v1/settings/branding` | `{"msp_name", "tagline", "accent_color", "logo_type", "logo_data", "min_display_ms"}`, the logo base64-encoded | 200 |
| `PUT /api/v1/settings/branding` | Replace the branding; the logo is PNG, JPEG or SVG up to 256 KiB, the SVG is checked for scripts and external references. Writes the Keycloak login theme | 200, with `warning` when the theme directory could not be written; 400 |
| `GET /api/v1/settings/branding/logo` | The logo as an image | 200; 404 |

`lab/tenants/api.sh METHOD PATH [JSON]` calls this API on the lab VM with
the credentials in a curl config file.
