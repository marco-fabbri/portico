# ADR 0002: The reverse proxy lives inside the Go binary

- Status: accepted (2026-10-17)
- Deciders: Marco, Claude

## Context

The first analysis (docs/history) used nginx with a handful of deny rules.
The lab showed that the per-request decision is the product: on pc.7.6 the
local login is `POST /api/iam/v4.1.b3/authn/login-requests/{id}/$actions/validate-credentials`
with the login request id in a cookie, Basic auth works on four API
generations, service-account API keys work through the proxy, local login
keeps working when the IdP is down, and a Project Admin can list all
accounts. These rules change with Prism Central versions and depend on
session state, host name and headers. Keeping them in nginx means
generating configuration from an application and reconciling two languages.

## Decision

Portico's proxy is implemented in the Go binary with the standard library's
reverse proxy: Host-based routing (Prism Central host, Keycloak host),
session check first (no Portico session, no proxying), then the deny list
of design §4.3, then forwarding with TLS SNI to Prism Central, WebSocket
upgrade pass-through and no buffering for long-lived connections. The
nginx configuration of the lab (`lab/vm/nginx.conf`) is the reference
behaviour the Go proxy must match, not a component of the product.

## Consequences

- Adding a service (NAI, NKP) is a Go module with its own policy, not an
  nginx block.
- `lab/auth/probe-local-login.sh` is the acceptance test: every line 403.
- Less battle-testing than nginx; acceptable at MSP scale. WebSocket
  behaviour through the Go proxy must be measured in Milestone 1 (the lab's
  VM-console test, write-up 08, measures it through nginx first).
