# 10 — Bearer replay and v3 user enumeration (Milestone 1 review follow-up)

## Question

Two items the Milestone 1 review left open for the public-path policy:

1. Does Prism Central issue a token that a client can replay as
   `Authorization: Bearer` without cookies? If so, a stolen token of a
   local account could be used by anyone holding a Portico session, and
   Bearer would have to join Basic on the deny list.
2. Does v3 `POST /api/nutanix/v3/users/list` enumerate every account for a
   Project Admin, as `GET /api/iam/*/authn/users` does (write-up 05)? If
   so, it would need the same deny rule.

## Measured on

- Prism Central: pc.7.6.0.6, cluster NTNX-01
- Date: 2026-10-18
- By: Claude (Marco's session), `lab/auth/probe-bearer-and-v3-users.sh`
  on the internal pass-through (`https://10.23.210.190:9440`, no Portico
  session involved: the question is what Prism Central accepts)

## Method

Local login as `portico-lab-bob` (Project Admin of tenant A) with curl and
a cookie jar: `GET /console/` → `iam-sessionid` cookie →
`validate-credentials` → `authCodeUri` → session. Then every cookie value
and every token-like JSON field replayed as `Authorization: Bearer` with no
cookies on `v3 vms/list`; then `v3 users/list` and `v4.0 authn/users` with
bob's Basic credentials.

## Result

- MEASURED: the Prism Central session after a local login is two cookies,
  `NTNX_SESSION_ID` (36 characters, HttpOnly) and `iam-sessionid` (36
  characters). The `validate-credentials` response carries only
  `data.authCodeUri`; no field named token.
- MEASURED: replaying either cookie value as `Authorization: Bearer` with
  no cookies answers **500** on `v3 vms/list`; the same call with the
  cookies answers 200. Prism Central issues nothing a client can replay as
  a bearer token after a local login. The 500 (rather than 401) suggests
  the header is parsed as a JWT and fails; whether Prism Central accepts a
  JWT from its own IAM OIDC endpoint was not measured.
- MEASURED: `v3 users/list` as bob returns **1 entity (bob's own)** while
  `metadata.total_matches` says **9**: v3 does not enumerate accounts for
  a Project Admin, it only leaks their count.
- MEASURED: `GET /api/iam/v4.0/authn/users` as bob returns all 9 accounts
  (7 in write-up 05, plus alice's SAML user created at her first login
  and the recreated service account). The Milestone 1 rule
  `deny-user-listing` stays necessary.

## Implications for the design

- No new deny rule. `Authorization: Bearer` stays allowed on the public
  path with a session, and `v3 users/list` stays allowed.
- Open for Milestone 2b, measured in the browser: whether the console ever
  sends an `Authorization` header. If it never does, the simplest and
  safest rule is to deny every `Authorization` scheme on the public path,
  not only Basic, and the JWT question above becomes moot.
- The count leak in `v3 users/list` is noted, not acted on: a count is not
  an identity.

## How to re-measure after an upgrade

`source lab/env.sh && lab/auth/probe-bearer-and-v3-users.sh` (exit 0 when
every line matches this write-up; the user count is the lab's current
number of accounts, adjust the two `9`s if the lab changes).
