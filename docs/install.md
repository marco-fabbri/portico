# Installing Portico

Pre-release: the paths below are what the lab exercises today. Nothing
here is supported yet; the first release (Milestone 6, `v0.1.0`) will say
what is.

## What you need

- **One or more Prism Centrals, pc.7.6 or later**, with Projects 2.0:
  each becomes a site, shown to tenants by a code such as `AZ-IT-MI1`.
  An AHV cluster for the appliance (any hypervisor or cloud for the
  second form).
- **Network**: one subnet for the appliance; port 9440 of every site's
  Prism Central reachable from the appliance's address and from your
  management network only, never from the Internet. Two public names for
  the tenants (`prism.<msp>.example`, `id.<msp>.example`) resolving to
  the appliance, or to a Cloudflare tunnel: every site shares them, a
  new site needs no DNS or certificate change.
- **A certificate** for the two public names (direct mode), or a
  Cloudflare tunnel with its connector token (tunnel mode). A
  self-signed pair is available for a trial.
- **Accounts used once per site**: a Prism Central account that can
  create users and edit authorization policies, and administrator
  credentials for the SAML trust (the service account's role cannot
  create an identity provider on pc.7.6, write-up 15). Portico stores
  neither.

## What listens where

| Port | Bound to | Who reaches it |
|---|---|---|
| 443 | every address (direct mode) or `127.0.0.1:8081` (tunnel mode) | tenants, through your public names or the Cloudflare tunnel |
| 8443 | every address with one interface; the internal interface's address once the console configures a second one without a gateway | MSP administrators: the admin UI and API, the factory password until the first login |
| 9440 | `127.0.0.1` | nobody, until you bind the pass-through to an internal address in `portico.yaml`; it serves one site (`passthrough_site` in the settings, the first by default) |
| 8080 | `127.0.0.1` | Portico only: Keycloak |
| 22 | every address, keys only | whoever has a key in `admin`'s `authorized_keys` (cloud-init) |

**Never let 8443 be reachable from the Internet.** With two interfaces
the console binds it to the internal one. With a single interface (a
cloud instance, one public address) it answers on that address: forward
only 443 to the appliance (security group, NAT, upstream firewall), and
log in soon after the first boot to replace the factory password.

## The appliance image

1. Download `portico-<version>.qcow2` and its `.sha256` from the
   [release](https://github.com/marco-fabbri/portico/releases); check
   the checksum. The OpenSCAP report of that build is next to them.
2. Create the VM: with OpenTofu, `deploy/tofu/nutanix` uploads the image
   from the release URL and creates the VM (2 vCPU, 6 GiB, 40 GiB, legacy
   BIOS), with an optional static network and SSH key through cloud-init
   (see its README). By hand: import the qcow2 into the image service,
   create a VM with those sizes and one NIC, power it on.
3. **The console** (Prism Central, Launch console): the wizard is on
   tty1.
   - *Network*: hostname, address, prefix, gateway, DNS for each
     interface; skip it when cloud-init already set them.
   - *Keycloak administrator credentials*: shown once, for the Keycloak
     card of the setup page.
   - *Exposure*: a certificate and key for the public names, a
     self-signed pair, or the Cloudflare tunnel token.
   - *Admin password*: optional here; the web does it at the first login.
4. **The first login**, `https://<address>:8443/` (self-signed internal
   certificate): `admin` with the factory password `portico/4u`; the
   appliance asks for a new password (at least 12 characters) before
   showing anything else. Do it soon after the first boot: until then
   anyone reaching 8443 on your internal network could do it instead.
5. **The setup page**: public names; Identity (the Keycloak credentials
   from the console, "Let Portico create the realm and clients"); Sites,
   "Add site" for each Prism Central (code, name, URL), then on the
   site's page "Let Portico create the service account" with the
   one-time account, the default cluster and container for new tenants,
   and "Let Portico create the SAML trust"; Tenants (prefixes, role);
   then Apply and restart. The appliance leaves setup mode and the
   public path opens.
6. **Branding** and the first **tenant** from the admin UI, placed on
   the sites where it gets resources; the tenant's user signs in at
   `https://prism.<msp>.example/` and, with several sites, chooses one.

The appliance has no root password and no SSH user unless cloud-init
added one: the console is the way in. Everything Portico keeps lives in
`/var/lib/portico` (the store and the master key; back them up together,
docs/operating.md).

## An existing Rocky Linux 9 host

The same components on a Rocky Linux 9 host of yours (a VM on any
hypervisor, a cloud instance): as root,

```
curl -fsSLO https://github.com/marco-fabbri/portico/releases/download/v<version>/install-<version>.tar.gz
tar -xzf install-<version>.tar.gz
./deploy/install.sh --release <version>
```

The installer (deploy/README.md) installs the packages, the service, the
Keycloak and PostgreSQL containers, the console on tty1, the firewalld
service (443, 8443) and the SSH policy (keys only, no root, members of
`wheel` only; installed only when a `wheel` user already has a key, so
the session you install from is not locked out); it
generates the Keycloak secrets and the internal certificate once and
starts in setup mode with the factory password. Then run the console
(`portico console` as root on tty1, or over SSH in a terminal) for the
exposure, and continue at step 4 above. The host's own hardening is yours: the image's
CIS baseline (image/oscap) is applied only by the image build.

Re-running `install.sh --release <new version>` upgrades.

## Updating

A new image is the clean path: back up `/var/lib/portico`, create the
new VM, restore the directory, set the network on the console. On an
existing host, `install.sh --release` replaces the binary and the files
and keeps the store.
