# Lab write-ups (Milestones 0 to 2 and the layout pass)

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
| 09 | [The Go proxy in the lab (Milestone 1 acceptance)](2026-10-17-09-go-proxy-acceptance.md) | probe passes on every line; alice logs in once and sees her VM; VM console through the Go proxy; fail closed with Keycloak down; 8 MiB |
| 10 | [Bearer replay and v3 user enumeration](2026-10-18-10-bearer-and-v3-users.md) | the local-login session is two cookies, nothing replayable as Bearer (500); v3 users/list returns only the caller, leaks the count; no new deny rule |
| 11 | [Tenant arrival through the API (Milestone 2a acceptance)](2026-10-18-11-tenant-arrival-acceptance.md) | tenant created in 77 s with seven recorded steps; carol logs in once, forced password update, sees only her project; disable locks her out; deletion clean in 28 s, `_internal` unchanged |
| 12 | [Console headers, provider selection, Sign Out path; admin UI acceptance](2026-10-18-12-console-headers-and-provider-selection.md) | 644 console requests, none with `Authorization`: every scheme now refused; the SAML button's PUT body captured for the landing page; `/redirect` (Prism Central Sign Out) added to the allow-list; part two: tenant created, equipped, disabled, enabled and deleted from the admin UI in Chrome |
| 13 | [Keycloak login theme, login request, `/redirect` target; landing page acceptance](2026-10-18-13-keycloak-theme-and-landing.md) | theme from a mounted directory applies without restart (file set and selectors recorded); the provider-selection PUT and `initiate-login` (200 with a self-submitting SAML form); `/redirect` ignores its parameter; part two: alice reaches the console with no click (1.2 s landing to auth_code), branded page with Keycloak down, one fallback with a wrong IdP name |
| 14 | [Settings in the store, master key, apply as restart](2026-10-18-14-settings-in-the-store.md) | one `settings import` moves the YAML and its secret files into the store (no secret file left on the VM, master key 0600 created by the binary); setup page with checks that fill the cluster and container pickers; Apply = exit 0 + `Restart=always`, back in ~10 s; a wrong key stops the service with the field name, and systemd's start limit then needs `reset-failed` |
| 15 | [Fresh-appliance bootstrap and setup mode](2026-10-18-15-bootstrap-and-setup-mode.md) | a fresh appliance starts in setup mode (admin listener only, missing list in the journal); the three bootstraps took 2 s (Keycloak), 3 s (Prism Central) and 17 s (SAML) on a quiet IAM, 41 s for the whole setup through the API; `Project Manager_acp` cannot create the SAML IdP (403), so that step takes an administrator's credentials once; deleting an IdP needs its SAML users deleted first and IAM answers slowly for minutes afterwards; a key creation that times out is revoked by name |
| 16 | [Minor sweep after 2d and 2e](2026-10-18-16-minor-sweep.md) | password policy on the realm Portico creates (`length(12) and notUsername and notEmail`), login restarted once when its state cookie is gone, logo drawn as a block above the realm name, URL with credentials refused, store failures on save as 500, no start limit on `portico.service`; the older deferred findings moved to `docs/backlog.md` |
| 17 | [Keycloak login page layout and Portico's mark](2026-10-19-17-login-page-layout.md) | keycloak.v2 on 26.3 ships `css/styles.css` (`login.css` is 404) and declares Red Hat Display; the page is PatternFly 5 markup laid out as a grid; Portico's theme turns it into one centred column on the accent colour with the lockup, the claim and a powered-by line; static URLs versioned on the admin UI |
| 18 | [Admin UI skin and the setup page state](2026-10-19-18-admin-ui-skin.md) | Red Hat variable fonts (78 KB, OFL) ship in the binary since the CSP allows no external origin, woff2 needs its media type registered; the setup cards now state what is configured and fold the bootstrap forms away; the multi Prism Central setup page goes to the backlog as its own milestone |

`lab-inventory.md` lists everything created in the lab and how to remove it.
`TEMPLATE.md` is the write-up skeleton. `payloads/` holds sanitised request
bodies. `raw/` (gitignored) holds API dumps.
