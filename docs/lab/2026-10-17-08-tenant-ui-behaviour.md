# 08 — Tenant console behaviour and WebSockets through the proxy (design 6.4)

## Question

Which calls the console makes for a tenant user, whether an allow-list of
path prefixes is realistic for Milestone 1, and whether the VM console
WebSocket passes through the proxy.

## Measured on

- Prism Central: pc.7.6.0.6 through the nginx stand-in; Chrome with the
  Claude in Chrome extension, user alice (Project Admin of one project)
- Date: 2026-10-17

## Method

Network capture of the login and of the first minute on the VM list page
(153 requests), then "Launch Console" on the project's VM; nginx access log
on the lab VM; WebSocket handshakes replayed with curl.

## Result

- MEASURED: path families used by the console for a tenant (full list in
  `payloads/tenant-console-paths.txt`): static `/console/...`,
  `/static/v3/swagger.json`; v4 `/api/{iam,prism,multidomain,networking,
  vmm,monitoring,licensing,cfs,ntnxsaas}/...` including `OPTIONS
  /api/*/unversioned/info`; v3 `/api/nutanix/v3/{batch,groups,search,
  clusters/list,user_preference,services/...}` and `/dm/v3/groups`; v1
  `/PrismGateway/services/rest/v1/{users/session_info,license,
  progress_monitors,application/user_data}`. Four generations on one page.
- MEASURED: Prism Central's own RBAC answers 403 to the tenant on
  licensing, product details and one `groups` query; the UI tolerates it.
- MEASURED: the VM console is a popup `/console/lib/noVNC-app/index.html?view=console&uuid=...`
  that calls `POST /api/vmm/v4.3/ahv/config/vms/{uuid}/$actions/generate-console-token`
  (task returns `VmConsoleToken` and a **relative** `WsUri=/console/launch/{uuid}`)
  and opens `wss://<window.location.host>/vnc/vm/{uuid}/proxy?proxyClusterUuid=...`
  (noVNC bundle). The host is the page's host, so the WebSocket goes
  through the proxy, never to the IP.
- MEASURED: the WebSocket handshake `GET /vnc/vm/{uuid}/proxy?proxyClusterUuid=...`
  with `Upgrade: websocket` answers **101** both directly on 9440 and
  through nginx (HTTP/1.1, `proxy_http_version 1.1` + upgrade headers).
- PENDING: the popup itself in Chrome (the automated click did not produce
  a request in the nginx log; a popup blocker is suspected). To confirm
  by hand.

## Implications for the design

- An allow-list by prefix is realistic for step 1 and short:
  `/console/`, `/static/`, `/ui/iam/`, `/api/`, `/dm/v3/`,
  `/PrismGateway/services/rest/v1/`, `/vnc/`, `/auth_code`, plus the
  WebSocket paths. The deny list of write-up 06 sits inside it. A finer
  allow-list per API family is step 2.
- The Go proxy must handle `Upgrade: websocket` on `/vnc/` and
  `/console/launch/` with HTTP/1.1 to Prism Central and no buffering.
- Public listener on HTTP/2 is fine: browsers open WebSockets on HTTP/1.1.

## How to re-measure after an upgrade

Log in as alice through the public name with the browser devtools open,
export the request list, and compare prefixes with
`payloads/tenant-console-paths.txt`; replay the `curl` WebSocket handshake
of this write-up.
