# Lab write-ups (Milestone 0)

Measurements on cluster NTNX-01, Prism Central pc.7.6.0.6, October 2026.
Each write-up states what was measured, the implications for the design,
and how to re-measure after an upgrade. Scripts are under `lab/`.

| # | write-up | one-line result |
|---|---|---|
| 01 | [Prism Central inventory and v4 API discovery](2026-10-17-01-pc-inventory.md) | Projects 2.0 = `multidomain/v4.4.b1`; role memberships = `iam/v4.1.b3`; no SAML IdP; v3 projects empty; UI answers 200+HTML to unknown API paths |
| 02 | [Lab VM via v4 API](2026-10-17-02-lab-vm.md) | image from URL, VM from image, cloud-init network must be in `bootcmd`; v4 actions need `If-Match` |
| 03 | [Keycloak and nginx stand-in](2026-10-17-03-keycloak-nginx.md) | two public host names behind nginx; Keycloak + PostgreSQL idle ≈ 1.3 GiB of 6 GiB |
| 04 | [SAML behind a different host name](2026-10-17-04-saml-hostname.md) | IdP with `redirectURL` publishes per-IdP SP metadata on the public name; browser round trip works end to end |
| 05 | [Projects 2.0 API](2026-10-17-05-projects2-api.md) | six calls make a tenant; `id` slug required; `ProjectsScopeTemplate`; IdP must be unshared before delete; isolation verified |
| 06 | [Local login and IdP fallback](2026-10-17-06-local-login-and-fallback.md) | `validate-credentials` under `login-requests`; Basic and API key pass; local login works with IdP down |
| 07 | [Service account role](2026-10-17-07-service-account-role.md) | service account + API key (`X-ntnx-api-key`) in `Project Manager_acp` does the whole tenant cycle |
| 08 | [Tenant UI behaviour and WebSockets](2026-10-17-08-tenant-ui-behaviour.md) | four API generations on one page; short prefix allow-list is realistic; `/vnc/.../proxy` WebSocket 101 through the proxy |

`lab-inventory.md` lists everything created in the lab and how to remove it.
`TEMPLATE.md` is the write-up skeleton. `payloads/` holds sanitised request
bodies. `raw/` (gitignored) holds API dumps.
