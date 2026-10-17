# 06 — Local login endpoints and behaviour with the IdP down (design 6.2, 6.5)

## Question

Which exact requests carry local credentials on pc.7.6, so the proxy's
deny list is right for this version; and what Prism Central does when the
SAML identity provider is unreachable.

## Measured on

- Prism Central: pc.7.6.0.6, reached through the nginx stand-in on
  `https://prism.lab.portico.test` (no policy yet)
- Date: 2026-10-17
- By: Claude (Marco's session), `lab/auth/probe-local-login.sh` and curl;
  endpoints read from the IAM login page bundle
  (`/ui/iam/app.<hash>.js`), to be confirmed with a browser capture

## Method

Followed the console redirect chain with curl, extracted the API strings
from the IAM login bundle, replayed the local login, then stopped Keycloak
on the lab VM and repeated the calls.

## Result

The local login flow on pc.7.6 (all through the public name):

| # | request | result |
|---|---|---|
| 1 | `GET /console/` | 302 to `/api/iam/authn/v1/oidc/auth?client_id=...&redirect_uri=https://<public host>/auth_code?original_request=...` |
| 2 | `GET /api/iam/authn/v1/oidc/auth?...` | 302 to `/ui/iam/providers`, sets cookie **`iam-sessionid`** |
| 3 | `GET /ui/iam/providers` | the login page (SPA) |
| 4 | **`POST /api/iam/v4.1.b3/authn/login-requests/{iam-sessionid}/$actions/validate-credentials`** body `{"username","password"}` | **200** with `authCodeUri=https://<public host>/auth_code?code=...` on success; the browser then follows it and gets the session |
| 5 | `POST /PrismGateway/j_spring_security_check` (legacy form) | **302** to `/PrismGateway/nmb/loginfailure?error=6` with wrong credentials: the legacy endpoint still exists |

- MEASURED: the local-login request is `validate-credentials` under
  `login-requests`; the login request id is the `iam-sessionid` cookie
  value set by the OIDC auth step. Other strings in the bundle:
  `/api/iam/authn/initiate-login` (302 to providers with `ec=ERR-412` when
  called directly), `/PrismGateway/services/rest/v1/utils/pre_login_details`
  (public, version info), `/iam/authn/.well-known/openid-configuration`.
- MEASURED: `Authorization: Basic admin:...` works on all generations
  through the proxy: `/api/iam/v4.0/...`, `/api/nutanix/v3/...`,
  `/PrismGateway/services/rest/v1/...`, `/api/clustermgmt/v4.0/...` → 200.
- MEASURED: a service-account API key (`X-ntnx-api-key`) also works through
  the public name → 200. It must be denied there too.
- MEASURED: the `redirect_uri` and the `authCodeUri` are built from the
  request's Host header, so the OIDC round trip of the console itself
  survives a different public name without any setting.
- MEASURED with Keycloak stopped (6.5): the login page still renders
  (`/ui/iam/providers` → 200), and **local login still succeeds** (step 4 →
  200). Prism Central does not need the IdP to accept local credentials;
  there is nothing to "fall back" to, local auth is always on. The SAML
  path fails only when the browser is redirected to Keycloak (502 from
  nginx). The error text the console shows is left to the browser test.

## Implications for the design

Deny list for the public listener (Milestone 1), to be applied before any
proxying and regardless of session:

1. `POST /api/iam/*/authn/login-requests/*/$actions/validate-credentials`
2. `POST /PrismGateway/j_spring_security_check`
3. any request with `Authorization: Basic ...`
4. any request with `X-ntnx-api-key` (tenant API keys are step 2 and will
   be Portico's own, not Prism Central's)

Plus the session rule: no Portico session → no proxying at all (the deny
list is the second line). `lab/auth/probe-local-login.sh` is the acceptance
test: every line must become 403 from Portico.

Design 6.5 answered: an IdP outage does not open a local-login path that
was closed; it only breaks SAML. The proxy policy is sufficient.

## How to re-measure after an upgrade

`source lab/env.sh && lab/auth/probe-local-login.sh`, then re-extract the
strings from the IAM bundle:
`curl -s https://<pc>/ui/iam/providers | grep -oE 'src="[^"]+app[^"]+\.js"'`
and grep the file for `login-requests` and `authn/`.
