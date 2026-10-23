# ADR 0006: Connections deleted only when empty, suspended by Portico alone, sites changed at runtime

- Status: accepted (2026-10-22)
- Deciders: Marco, Claude

## Context

The sites design (`2026-10-20-sites-design.md`) placed a tenant on one or
more sites and let the MSP remove a site from a tenant, keeping the
project unless asked to delete it, and never removing a tenant's last
site. Running it on the lab showed gaps (write-up 23 and the sites
usability design, `2026-10-22-sites-usability-design.md`): a project left
behind after "remove" is an orphan nobody owns; deleting a project from
Portico while it still holds VMs would make Portico the tool that
destroys a customer's work; there was no way to stop the traffic of one
tenant to one site during a maintenance; every site change needed Apply
and a restart; and a site whose certificate cannot be verified had only
"verify off". Write-up 24 measured that the v4 lists can be filtered by
project for most kinds, and that Prism Central itself refuses to delete
a project that still holds a VM.

## Decision

1. **Disconnect only when empty.** Disconnecting a tenant from a site
   counts what the project holds with the service account (VMs, images,
   templates, OVAs, volume groups, subnets, VPCs, floating IPs, security
   policies, categories, recovery points) and refuses while anything is
   there, naming it. An empty project is deleted with its resource group,
   SAML group and role membership, in the reverse order of creation. A
   kind Prism Central will not list for the service account is reported
   as not counted, and Prism Central's own refusal to delete a project
   that still holds something is the safety net: the connection then
   ends `remove-failed` with Retry. The last site can be disconnected
   too.
   Disconnect deletes only what Portico created: provisioning adopts an
   existing project or SAML group of the right name, so a project whose
   description does not start with "Portico tenant " (made by hand) is
   kept with its resource groups, and a SAML group Prism Central reports
   bound to another identity provider is kept; the tenant's access is
   removed either way and the step says what was kept.
2. **No deletion of resources by Portico.** Portico never deletes a VM,
   an image or any other thing a tenant made; emptying a project is the
   MSP's work in Prism Central. A "danger zone" that would do it was
   kept out (backlog).
3. **A tenant is deleted only with no site left.** Deleting it removes
   its Keycloak users, its group and its row; projects are deleted only
   by Disconnect, so there is one path that touches Prism Central.
4. **Suspend closes Portico's route only.** A suspended connection gets
   no new request through Portico (an open VM console finishes); nothing
   changes in Prism Central and the VMs keep running. Resume opens the
   same project. A suspended connection still counts among the tenant's
   sites, as a grey tile, so a tenant with two sites does not suddenly
   land on the other one.
5. **Sites change at runtime, certificates can be pinned.** Decided in
   the same design (4.3 and 4.9): adding, editing or forgetting a site
   takes effect without a restart, and a site whose certificate cannot
   be verified is trusted by the SHA-256 fingerprint of its leaf
   certificate, confirmed by the MSP, instead of "verify off". They are
   built in Part C of that design.

## Consequences

- An MSP that wants a tenant off a site first empties the project in
  Prism Central; Portico shows what is left and links to that Prism
  Central. Nothing a customer made disappears from a click in Portico.
- What the service account cannot list (Flow Network Security and
  floating IPs on the lab, write-up 25) is not counted; the refusal by
  Prism Central covers it, at the cost of a connection that ends
  `remove-failed` instead of a refusal up front.
- The count is a set of list calls at the moment of the click; something
  created between the count and the deletion is caught by the same
  refusal.
- A tenant may exist with no site: it can sign in and has nowhere to go
  until it is connected, which the site page says.
- Suspend is Portico's own state: an administrator who reaches Prism
  Central directly is not affected, and Prism Central does not know.
- The old `delete_project` choice is gone from the API and the pages; the
  API has no external users before the first release (sites design 4.6).
