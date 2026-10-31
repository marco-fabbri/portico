# Portico

[![CI](https://github.com/marco-fabbri/portico/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/marco-fabbri/portico/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Prism Central pc.7.6](https://img.shields.io/badge/Prism%20Central-pc.7.6%2B-024DA1.svg)](#requirements)
[![Keycloak 26](https://img.shields.io/badge/Keycloak-26-4D4D4D.svg?logo=keycloak&logoColor=white)](#what-it-does)
[![Go](https://img.shields.io/badge/Go-1.27-00ADD8.svg?logo=go&logoColor=white)](go.mod)
[![Release v0.2.1](https://img.shields.io/badge/release-v0.2.1-green.svg)](https://github.com/marco-fabbri/portico/releases/tag/v0.2.1)

A virtual appliance that runs inside a Nutanix cluster and acts as the
**single, authenticated entry point** through which a managed service
provider's tenants reach **Nutanix Prism Central**. One Go binary fronts
Prism Central and Keycloak on two public names, lets nothing through
without a login, creates a tenant in one action and keeps platform
administration on an internal listener. Later services (Nutanix
Kubernetes Platform, Nutanix Enterprise AI) plug into the same identity
and the same tenant model.

> [!IMPORTANT]
> **Independent project.** Portico is not affiliated with, endorsed by or
> supported by Nutanix. Nutanix, Prism Central, AHV, NKP and related names
> are trademarks of Nutanix, Inc. Keycloak is a trademark of Red Hat, Inc.
>
> **No Nutanix software or documentation is distributed here.** Portico
> talks to a Prism Central you already run under your own licence, through
> its public v4 APIs; the Nutanix guides it was built against stay on the
> Nutanix portal.
>
> **Current release, 0.2.1.** It is for trying Portico with real tenants
> on your own Prism Central. Everything below was measured on two test
> clusters (see [Status and validation](#status-and-validation)), with
> no production load. Read the
> [known limits](docs/releases/v0.2.0.md#known-limits) before you put it
> in front of customers. It is provided "as is", without warranty (see
> [License](#license)).

## What it does

Portico does not replace SP Central, the service-provider layer Nutanix
ships inside Nutanix Central. It fills the gap for the small MSP that
wants to expose services to its own tenants on top of one or more Prism
Central instances, without a hyperscaler-sized control plane.
Tenants are created with the same primitive Nutanix's own SP Central uses,
a Projects 2.0 project per tenant with group-based role mapping and no
global roles: an MSP that later adopts SP Central finds tenants it
recognises. Portico is the path towards it, not a detour.

| Piece | What it does |
|---|---|
| **Public path** | `portal.msp.example` and `id.msp.example` behind a Cloudflare Tunnel or directly on port 443, same guarantees in both modes. No Portico session, no request to Prism Central: the public path fails closed, also with the identity provider down |
| **Identity** | Keycloak (OIDC for Portico, SAML for Prism Central): one login for the tenant user, MFA and brokering to the customer's directory available. Prism Central local accounts, `admin` included, can never log in from outside: every local-login path and every `Authorization` header is refused |
| **Sites** | Several Prism Centrals behind one Portico, shown to tenants as sites such as `AZ-IT-MI1` and `AZ-US-NYC1` on the one public name: the tenant signs in once and chooses a site; no Prism Central address is ever shown. The MSP puts a site in maintenance, sees who is still connected and disconnects them |
| **Tenant arrival** | One action places a tenant on the sites where it gets resources: on each, a Projects 2.0 project with its resource group, the SAML group and a project-scoped role membership; one group in Keycloak; then a visibility check per site. Sites can be added and removed later; tenants can be disabled, enabled and deleted; users get a one-time temporary password |
| **Branding** | The Keycloak login page, Portico's landing page and its error pages carry the MSP's name, claim, colour, logo and tab icon |
| **Admin path** | A web UI and a JSON API on an internal listener only, with a break-glass account; a raw pass-through to Prism Central for administrators |
| **Settings** | Stored in Portico's own database, secrets sealed with a master key the appliance creates at first start. A fresh appliance configures itself from the setup page: Portico creates its service account in Prism Central, the Keycloak realm and clients and the SAML trust from one-time credentials it never stores |

```mermaid
flowchart LR
    subgraph Internet
        T["Tenant user"]
    end
    subgraph Appliance ["Portico appliance (2 vCPU, 6 GiB)"]
        P["portico (Go)<br/>proxy, policy, sessions,<br/>tenant provisioning, admin UI"]
        K["Keycloak<br/>OIDC + SAML"]
        DB["SQLite store<br/>sealed secrets"]
    end
    subgraph Nutanix ["Sites: one Prism Central each"]
        PC["Prism Central pc.7.6<br/>Projects 2.0"]
    end
    A["MSP administrator<br/>(LAN / VPN)"]

    T -->|"443: portal and id hosts, Portico session required"| P
    P -->|"login pages, loopback"| K
    P -->|"9440, service account"| PC
    K -->|"SAML trust"| PC
    P --- DB
    A -->|"8443 admin UI and API"| P
```

## How a request flows

1. The browser opens `https://portal.msp.example`. Portico looks for
   its session cookie; without one it redirects to Keycloak (OIDC
   authorization code flow) and issues its own cookie after the login.
2. With a session, the public policy runs: every `Authorization` header
   is refused, the local-login endpoints of pc.7.6 are refused, and
   Portico's own cookies are stripped before forwarding.
3. The request is proxied on port 9440, under the public host name, to
   the Prism Central of the site the tenant chose (a tenant with several
   sites picks one on a branded page; with one it goes straight on); VM
   console WebSockets pass through.
4. Prism Central wants its own session: Portico's landing page starts the
   SAML login itself. Keycloak already holds a session, so the assertion
   comes back without a second login and Prism Central scopes the user
   by project membership.

Two protocols (OIDC for Portico, SAML for Prism Central), one login.

## The tenant model

A tenant is one row in Portico's store that ties together a group in
Keycloak (`tenant-<slug>`) and, on each site where it has resources, a
Projects 2.0 project in that site's Prism Central (with its resource
group and the cluster and container it may use), a SAML group and a role
membership that binds the group to a role inside the project, never a
global one. Creating a tenant runs those
steps in order, records each one, and ends with a visibility check that
reads back the project and asserts the group holds nothing outside it. A
tenant can be disabled (group disabled, login impossible) and deleted; the
project is left in place unless asked, and VMs are never deleted. Tenant
users are Keycloak users of the tenant group, created from the admin UI
or API with a one-time temporary password; the email is the username and
the SAML `NameID`.

Since 0.2.0 a tenant also gets a **ready network** on each site whose
MSP switched it on: a VPC with the tenant's `/24`, outbound access, and
floating IPs that publish its VMs, on a `/28` of the site's transit VPC
as in Nutanix's NVD-2219 routed shared transit. Portico stores what it
created, notices when a tenant deletes or moves it in Prism Central, and
removes it at Disconnect; the tenant's role in its project cannot create
floating IPs, so the MSP sets how many it has.

## Why "Portico"

A portico stands in front of private buildings and makes them reachable
from the public space. It is not the building, it does not replace it, and
it does not change who owns it; it has its own rules about who walks in
and what they see, and one covered walk can join several buildings. That
is Portico's relationship with Prism Central: Prism Central stays the
house, Portico is the way the tenants reach it.

The project is written in Bologna, whose porticoes are a UNESCO World
Heritage Site. The word is the same in Italian and in English, it is one
word with no vendor name in it, and `portico serve` reads well.

## What it is not

- Not a replacement for SP Central, nor for the Nutanix Central around
  it: an MSP that later adopts them finds tenants it can recognise.
- Not a second Prism Central: Portico orchestrates, it does not copy state
  it does not own.
- Not dependent on Cloudflare for security: direct exposure is as safe as
  tunnelled exposure.

## Requirements

- **Prism Central pc.7.6 or later** (Projects 2.0). Older releases use a
  different project model and are out of scope; a legacy adapter, if ever,
  would come with reduced guarantees.
- Port 9440 of Prism Central reachable only from Portico's internal
  address and the MSP management network: Portico checks what it can from
  its own address, the network enforces it.
- For tenant networks, Flow Virtual Networking (Integrated mode) and one
  transit VPC per site with a routed prefix (install guide).
- An account that can create users and edit authorization policies, used
  once by the setup page to create Portico's own service account; an
  administrator's credentials once more for the SAML trust, which the
  service account's role cannot create (measured, write-up 15).
- Appliance footprint target: 2 vCPU, 6 GiB RAM, 40 GiB disk for Portico,
  Keycloak, PostgreSQL and the optional `cloudflared`.

## Install

Download the appliance image of the
[latest release](https://github.com/marco-fabbri/portico/releases/latest),
check its checksum, import it into Prism Central and boot it; the
console on tty1 sets the network and the setup page on port 8443 does
the rest, the certificate included. The
[install guide](docs/install.md) has every step, the certificates and
the upgrade; the [release notes](docs/releases/v0.2.1.md) say what
the current release does not do yet.

## Status and validation

Everything was measured on Prism Central pc.7.6.0.6 (AOS 7.6, two
single-node AHV clusters) and Keycloak 26.3, first on a Rocky Linux 9 VM
and from Milestone 3 on the appliance, by the scripts in `lab/` and by
hand in a browser. Each row has a write-up in
[`docs/lab/`](docs/lab/) saying what was measured, on which version, and
how to measure again after an upgrade.

| Milestone | Result |
|---|---|
| 0 Lab measurements, 2026-10-17 | SAML works behind a public name (per-IdP SP metadata); the local-login endpoints of pc.7.6 recorded; six v4 calls make a tenant; the console's four API generations and the VNC WebSocket measured; a service account with an API key in `Project Manager_acp` does the whole tenant cycle (write-ups 01 to 08) |
| 1 Core binary, 2026-10-17 | A browser reaches the console only after the Keycloak login; `curl -u admin:…` and every other `Authorization` scheme get 403 from Portico; VM console WebSockets through the proxy; fail closed with Keycloak down; 8 MiB binary (write-ups 09, 10, 12) |
| 2 Tenant arrival, 2026-10-18 | Tenant created in 77 s with seven recorded steps, its user signs in once with a forced password change and sees only her project; disable locks her out; deletion leaves `_internal` unchanged. Admin UI and JSON API; MSP branding on the Keycloak login page; settings sealed in the store; a fresh appliance bootstrapped from the setup page in 41 s (write-ups 11 to 18) |
| 3 Appliance image, 2026-10-19 | Rocky Linux 9 qcow2 (1.36 GB) built by the CI in 20 to 25 min: CIS Level 1 Server with OpenSCAP (271 rules pass, three documented exceptions), console wizard on tty1, factory password changed at the first login, `cloudflared` option, secrets and TLS pairs made at the first boot; boot-tested on the runner with AHV's config drive and a NoCloud seed; imported into Prism Central and booted on the cluster with an OpenTofu module, set up through the API, a tenant active and its user signing in (write-up 19) |
| 4 Sites, 2026-10-20 | Several Prism Centrals behind one Portico on one public name: site page, routing checked against the tenant's placements on every request, per-site cookies, maintenance with the users still connected, tenants added to and removed from sites. Built and tested against two fake Prism Centrals and accepted on the appliance with one site (write-ups 20 and 21). Then with a second pc.7.6 cluster: a tenant on two sites opens both consoles from the site page with one login, and its connections are added, suspended and disconnected, Disconnect deleting only an empty project Portico created (write-ups 23 to 25) |
| 5 Backlog sweep, 2026-10-25 | The findings that must not ship closed with a test each: the public path, Prism Central retries, the appliance's SSH and service hardening, the admin UI, the portal's name (`portal_host`); the scripts moved onto the appliance (write-ups 27 and 28) |
| 6 First release `v0.1.0` | A fresh appliance from the CI image, both sites, tenants, maintenance, local-login probes and two upgrades in place: two release blockers found and fixed (write-up 29); the release rehearsed as a draft (write-up 30) and from its image (write-up 33), then published on 2026-10-27 |
| 7 Tenant ready network, 2026-10-30 | A VPC, a `/24` subnet, outbound access and floating IPs per tenant and site on a routed `/28` of the site's transit VPC (NVD-2219), built in seven recorded steps and removed at Disconnect; measured on two appliances, the second with a router VM in front of the transit VPC (write-ups 34 and 35); 0.2.1 moves the default pool clear of Prism Central's reserved subnets (ADR 0009) |

Not verified yet: tenant API keys, brokering to a customer's own
directory, brownfield projects, any production load. Deferred findings of the reviews are listed in
[`docs/backlog.md`](docs/backlog.md).

## Security considerations

The design turns on four rules, and the code is reviewed against them
(see [`SECURITY.md`](SECURITY.md) to report a finding):

- the public path fails closed: no Portico session, no request to Prism
  Central, whatever the identity provider is doing;
- Prism Central local accounts never authenticate from outside, in any
  exposure mode;
- Portico works through its own `portico` service account and never
  stores a human administrator's credentials; the one-time credentials of
  the setup page travel in that request only, and Keycloak's temporary
  administrator of the first boot is deleted once the realm exists;
- secrets at rest are sealed with a master key that never leaves the
  appliance (`/var/lib/portico/master.key`, mode 0600, created at first
  start).

Every appliance starts with the factory password `admin` / `portico/4u`
for the admin UI (Nutanix's own convention); the first login opens the
password page and nothing else until a new one is set, and the API
refuses the factory one. Between the first boot and that first login,
whoever reaches port 8443 on the internal network can claim the
appliance: never publish 8443, and log in soon after the first boot.
Before real use, replace the self-signed pairs with a certificate of
your own ([Certificates](docs/install.md#certificates)). Towards each
Prism Central, Portico verifies the certificate when the system trusts
it, and otherwise pins the SHA-256 fingerprint the administrator
confirmed when adding the site.

## Repository layout

```
cmd/portico/          main: serve | settings import|show | admin set-password | console | exposure apply | version
internal/
  proxy/              reverse proxy, public policy, landing and error pages
  auth/               OIDC login, sessions
  sites/              sites, their probes and certificate trust
  idp/keycloak/       Keycloak admin client
  nutanix/            Prism Central v4 client, service-account bootstrap
  tenant/             tenant model, provisioning steps, the ready network
  netready/ netplan/  a site's networking checks, the tenant address plan
  settings/           runtime settings, checks, bootstraps
  exposure/           the certificate and tunnel switch
  secrets/ store/     sealing, SQLite
  config/             the process configuration file
  branding/           MSP branding, generated Keycloak theme
  console/            the tty1 wizard
  web/ api/           admin UI (templates, HTMX) and JSON API
  textedit/           first-match edits of configuration text
deploy/               systemd units, quadlets, firewalld, the OpenTofu module
image/                the appliance image build (Packer, kickstart, OpenSCAP)
lab/                  scripts that drive the lab (Prism Central, VM, Keycloak)
docs/
  superpowers/specs/  the design documents
  superpowers/plans/  the plan each milestone was built from
  adr/                decisions
  history/            the original analysis, as provenance
  lab/                measurements, one write-up per question
  releases/           the notes of each release
  backlog.md          deferred findings
```

## Documentation

- [Install guide](docs/install.md) and the
  [release notes](docs/releases/), 0.2.1's with 0.2.0's known limits.
- [Design document](docs/superpowers/specs/2026-10-17-portico-design.md):
  intent, goals and anti-goals, architecture, security model, milestones.
- [Decisions](docs/adr/): nine, from the pc.7.6 baseline to the tenant
  address pool.
- [Admin JSON API](docs/api.md) and [operating the binary](docs/operating.md):
  commands, files, setup mode, logs, backup.
- [Lab write-ups](docs/lab/): one per question, with the version and how
  to measure again; [`lab-inventory.md`](docs/lab/lab-inventory.md) lists
  what the lab holds.
- [Plans](docs/superpowers/plans/) the milestones were built from, and
  the [backlog](docs/backlog.md) of deferred findings.
- [Example configuration](deploy/portico.yaml.example) and the
  [systemd unit](deploy/portico.service).

## Roadmap

Step 1 ended with 0.1.0: the appliance image (Rocky Linux 9, CIS
Level 1, console wizard, `cloudflared` option), sites and an install
guide. 0.2.0 added the tenant ready network, 0.2.1 its default pool.
Next, from the known limits: the container images inside the
qcow2, so a first boot needs no Internet, and a backup of Keycloak's
database with the store.

Step 2, in no fixed order: a setup page that lists connected services
(several Prism Central, later NKP and Nutanix Enterprise AI), tenant API
keys bound to federated identities, brokering each tenant to its own
directory (AD, Entra ID, LDAP), brownfield inventory with an exclusion
list, quotas and usage reporting, tenant self-service.

## Building and testing

Go 1.27 or later, nothing else: templates, HTMX and fonts are embedded
in the binary.

```
go build ./cmd/portico
go test ./...
```

The tests run against fakes of Prism Central and Keycloak. The lab
scripts drive the real ones and need a lab of your own;
[`CONTRIBUTING.md`](CONTRIBUTING.md) says how the project is built (plan,
test first, measure on the lab, English everywhere, no secrets).

## Supporting the project

Portico is built and measured on two single-node AHV clusters with
Prism Central pc.7.6. That is enough for the single appliance, and
not enough for what comes next. If you can help
with any of the following, it turns a design into a measurement:

- **A decommissioned Nutanix node** (NX, or a Dell XC, HPE DX, Lenovo HX
  equivalent), even an older one: it does not need to be in support.
- **A second cluster or Prism Central**, pc.7.6 or later, reachable from
  a lab network even for a few days at a time. The connected-services
  page (several Prism Central behind one Portico), the high-availability
  pair across two sites, and the firewall rules of a real multi-site
  setup all need one.
- **Access to a Nutanix lab** (a partner lab, a demo environment, a test
  tenant on a service provider's cluster): to try Portico against an
  installation that is not mine, with projects, categories and users I
  did not create.
- **A workstation that runs Nutanix Community Edition**: a single node
  with 256 GB of RAM and NVMe or SATA SSDs would let me run two Prism
  Central instances side by side, and would make the test environment
  reproducible by anyone with the same box.
- **Review and field experience** from MSPs: which tenant operations you
  do every week, which Prism Central versions you run, what your
  customers expect at the public URL.

Hardware is lent or donated, access is temporary and documented, and
every measurement that comes out of it lands in `docs/lab/` with
credit. Open an [issue](https://github.com/marco-fabbri/portico/issues)
or reach me on LinkedIn (linked on my GitHub profile).

## License

Copyright 2026 Marco Fabbri.

Licensed under the Apache License, Version 2.0; see [`LICENSE`](LICENSE).
This licence covers the content of this repository only. It grants no
rights to Nutanix software or trademarks, and Portico grants no product
entitlement: the Prism Central it fronts is licensed by Nutanix to you.
