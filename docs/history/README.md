# History

Provenance of the Portico design. These files are the original analysis,
written in Italian under the working name "Prism Gate" before the project was
named Portico. They are kept verbatim as historical record and are not
maintained. The current design is in `docs/superpowers/specs/`.

## 2026-09-07 — Prism Gate design (`2026-09-07-prism-gate-design.it.md`)

How to make a single Prism Central multi-tenant and expose it on the
Internet while keeping local accounts usable only from the internal network.
Key ideas that carried over into Portico:

- Prism Central projects as the tenant unit; identity-provider groups mapped
  onto projects and roles; no global roles for tenants.
- Two listeners on one reverse proxy: public for tenants (federated login
  only), internal for platform administration.
- Port 9440 reachable only from the proxy.
- Blocking `Authorization: Basic` and the local login endpoint on the public
  listener, with nginx examples.
- An honest list of limits (role matters more than port, fallback to local
  auth, logical rather than physical isolation) and of things to measure in
  the lab.

## 2026-10-15 — Lab note (`2026-10-15-lab-note.it.md`)

Written from a lab session as input for the design. Main points:

- Prism Central IAM accepts local accounts, Active Directory, OpenLDAP, SAML
  2.0 and CAC; Keycloak is on Nutanix's tested list. SAML constraints:
  SP-initiated only, redirect binding, `NameID` becomes the username.
- Proposal: Keycloak via SAML, bridging to each customer's directory.
- What changes from the first design: authentication at the proxy becomes the
  first line of defence; the product is the automated tenant arrival, not the
  proxy; two isolation levels (shared Prism Central with projects, or a
  cluster per customer) behind the same gate.
- First risk to measure: SAML behind a proxy with a different host name.
- Lab inventory: cluster NTNX-01, Prism Central pc.7.6, resources available
  for project VMs.
- Naming: "Prism Gate" rejected as public name (Nutanix trademark, clashes
  with the `PrismGateway` API prefix). The project was later named Portico.
