# 07 — The `portico` service account and its minimal role (design 6.7)

## Question

Which Prism Central identity and permissions let Portico manage projects,
resource groups, role memberships, SAML settings and probe users, without
storing a human administrator's credentials.

## Measured on

- Prism Central: pc.7.6.0.6
- Date: 2026-10-17
- By: Claude (Marco's session), `lab/auth/create-service-account.sh`

## Method

Created a **service account** (user type `SERVICE_ACCOUNT`, no password)
with an **API key**, added it to the predefined `Project Manager_acp`
authorization policy, then re-ran the full tenant cycle
(`lab/projects/create.sh`, `bind-group.sh`, `delete.sh`) and a local-user
creation with `PC_API_KEY` instead of Basic auth.

## Result

- MEASURED: pc.7.6 has first-class service accounts:
  `POST /api/iam/v4.1.b3/authn/users` with `userType:"SERVICE_ACCOUNT"`,
  then `POST /api/iam/v4.1.b3/authn/users/{extId}/keys`
  `{name, keyType:"API_KEY"}` → the key is returned **once**
  (`keyDetails.apiKey`), default expiry 100 years, status `VALID`, revocable
  (`$actions/revoke`) and deletable. Same mechanism SP Central's
  `sp_svc_acc_<uuid>` uses.
- MEASURED: the key authenticates with the header `X-ntnx-api-key: <key>`
  (name taken from the Go client's `apiKeyAuthScheme`). No Basic auth, no
  password to rotate.
- MEASURED: a fresh service account with no policy gets 403 on projects and
  clusters (and an empty user list).
- MEASURED: adding its UUID to the predefined **`Project Manager_acp`**
  policy (`PUT /api/iam/v4.1.b3/authz/authorization-policies/{extId}` with
  `identities[].identityFilter.user.uuid.anyof += [uuid]`, type
  `PREDEFINED_UPDATE_IDENTITY_ONLY`) is enough for the whole tenant cycle:
  project create/delete, resource group create/delete, SAML user-group
  create/delete, IdP share/unshare, role membership create/delete, and
  **local user create/delete** (for probe users). Role `Project Manager`
  has 767 operations vs 1397 for Super Admin; the Projects Guide describes
  it as exactly this job ("allocates infrastructure resources, manages user
  IdP onboarding, defines project-level quotas, oversees the full project
  lifecycle").
- NOT MEASURED: whether Project Manager can *create* the SAML IdP itself
  (it was created by `admin` before the service account existed) and share
  subnets/images with projects. Both are one-time or admin-side actions in
  step 1; to measure in Milestone 2.
- NOT MEASURED: a custom role smaller than Project Manager. Not needed for
  step 1; the predefined role is what Nutanix documents for the job and
  survives upgrades.

## Implications for the design

- Design §4.5 changes: the `portico` identity is a **service account with
  an API key**, not a local user with a password. The bootstrap path
  creates the account, the key, and the policy entry with the
  administrator's one-time credentials, stores only the key (encrypted),
  and discards the credentials. The pre-created path asks the MSP for the
  key.
- The required permission is "member of `Project Manager_acp`" (or a
  policy with the `Project Manager` role). Document it as such; no custom
  role in step 1.
- Key rotation: create a second key, switch, revoke the first. Portico
  should expose this in the UI (step 2) and log the key expiry.
- `lab/pc.sh` supports `PC_API_KEY`; the Prism Central client in Milestone
  1 authenticates only with the API key.

## How to re-measure after an upgrade

`source lab/env.sh && lab/auth/create-service-account.sh`, then
`export PC_API_KEY=$(cut -d= -f2- ~/portico-secrets/portico-sa-key)` and run
the tenant cycle of write-up 05.
