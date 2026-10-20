# ADR 0004: Sites share one public name; the session holds the site

- Status: accepted (2026-10-20)
- Deciders: Marco, Claude

## Context

Milestone 4 puts several Prism Centrals behind one Portico and shows
them to tenants as sites (`AZ-IT-MI1`, `AZ-US-NYC1`). Prism Central's
console uses absolute paths from `/`, so it cannot live under a path
prefix: Portico must know, for each request, which Prism Central it is
for. The main design (section 4.10) had planned one public host name
per Prism Central. Marco asked for one public name and no Prism Central
address ever shown, with a later Portico-owned interface "in the style
of a cloud provider", chosen per tenant, in mind.

## Decision

1. Every site shares the Prism Central public name. After the Keycloak
   login, a tenant with several sites chooses one on a branded site
   page; with one site it goes straight on.
2. The chosen site lives in the Portico session (server-side today; in
   the shared encrypted session cookie when the pair arrives, design
   4.11) and is checked against the tenant's active placements on every
   request. The routing sends the request to that site's Prism Central
   after the deny list and the session check, before the allow-list.
3. One site at a time per browser. Prism Central's cookies of the site
   the browser leaves go into per-site stash cookies
   (`__Host-pstash.<site>.<name>`) in the switch response, and the
   arriving site's come back; no upstream ever receives a stash cookie
   or a Portico cookie.
4. SAML: Prism Central derives its service-provider entity id from the
   IdP's redirect URL, the same for every site, unless the IdP carries an
   `entityIssuer`. Write-up 20 measured that pc.7.6.0.6 accepts one per
   site and logs in with a Keycloak client of that name: each site gets
   `https://<prism_host>/api/iam/authn/<site-id>` and its own client
   (variant A). An upgraded appliance's first site keeps the shared
   default name until its trust is recreated.
5. The MSP sees who is still on a site and can put it in maintenance
   (no new entries) and disconnect the remaining users, without a
   restart.

## Consequences

- One DNS name and one certificate entry for all sites; a site added
  later costs no DNS or certificate change.
- Two tabs on two sites in one browser do not work: the tab left on the
  previous site fails until reloaded. A usability limit, not an
  isolation one: each Prism Central authorises the same user for that
  user's project only.
- With one SAML client per site, an assertion names the site it was
  made for; whether Prism Central refuses one made for another site is
  measured in the two-site test. Isolation does not depend on it: the
  routing checks the placements on every request and each Prism Central
  authorises the user only inside the tenant's project.
- The site is a Portico concept, independent of Prism Central's console,
  which is what the per-tenant Portico interface of the future needs.
