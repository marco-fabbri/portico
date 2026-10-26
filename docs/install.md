# Installing Portico

This guide installs Portico 0.1.0. What the release contains, what it
was measured on and its known limits are in the
[release notes](releases/v0.1.0.md): read the limits before you put
the appliance in front of customers.

## What you need

- **One or more Prism Centrals, pc.7.6 or later**, with Projects 2.0:
  each becomes a site, shown to tenants by a code such as `AZ-IT-MI1`.
  An AHV cluster for the appliance (any hypervisor or cloud for a Rocky
  Linux 9 host of your own).
- **Network**: one subnet for the appliance; port 9440 of every site's
  Prism Central reachable from the appliance's address and from your
  management network only, never from the Internet. DNS and NTP for
  the appliance.
- **An SSH key** for the appliance's `admin` user, given when the VM is
  created (cloud-init, or `ssh_public_key` of the OpenTofu module): it
  is how the certificate files reach the appliance.
- **Internet access at the first boot**: the appliance pulls PostgreSQL
  from `docker.io` and Keycloak from `quay.io` when it first starts.
  Without that egress it never gets its identity provider. On a host of
  your own the installer also needs the Rocky Linux repositories and,
  for tunnel mode, `pkg.cloudflare.com`.
- **Two public names** for the tenants, the portal and the identity
  provider (`portal.msp.example` and `id.msp.example` below), resolving
  to the appliance or to a Cloudflare tunnel. Every site shares them: a
  new site needs no DNS or certificate change. They cannot be changed
  after the setup.
- **A certificate** for both names (direct mode, see
  [Certificates](#certificates)), or a Cloudflare tunnel with its
  connector token (tunnel mode).
- **An account used once per site**: a Prism Central administrator
  that can create users and authorization policies. With it Portico
  creates its own `portico` service account and the SAML trust; Portico
  never stores a human administrator's credentials.

## Certificates

In direct mode the appliance serves one certificate for both public
names on port 443. It must:

- name both hosts in its Subject Alternative Name;
- carry `extendedKeyUsage = serverAuth`;
- be given with its chain: the certificate first, then each
  intermediate, in one PEM file.

A certificate from a public CA works in every browser. With a CA of
your own, every browser that opens the portal must trust that CA: on
macOS, open it in Keychain Access, System keychain, and set "Always
Trust". A bare self-signed certificate, without a CA above it, is
refused by Chrome and Safari even when it is installed (measured,
write-up 29). The console's self-signed pair is for a first try only.

A CA of your own and a certificate for the two names, with `openssl`:

```
# the CA, once (keep ca.key offline)
openssl req -x509 -newkey rsa:3072 -nodes -days 3650 \
  -keyout ca.key -out ca.crt -subj "/CN=MSP portal CA" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,keyCertSign,cRLSign"

# the certificate of the two public names (397 days at most)
openssl req -newkey rsa:3072 -nodes -keyout portal.key -out portal.csr \
  -subj "/CN=portal.msp.example"
openssl x509 -req -in portal.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -days 397 -out portal.crt -extfile <(printf '%s\n' \
  "subjectAltName=DNS:portal.msp.example,DNS:id.msp.example" \
  "extendedKeyUsage=serverAuth" "keyUsage=critical,digitalSignature,keyEncipherment")

# the file the console asks for: the certificate, then the CA
cat portal.crt ca.crt > portal-chain.crt
```

Copy the two files to the appliance (`scp portal-chain.crt portal.key
admin@<address>:`); the console asks for their paths (step 3 below).

## What listens where

| Port | Bound to | Who reaches it |
|---|---|---|
| 443 | every address (direct mode) or `127.0.0.1:8081` (tunnel mode) | tenants, through your public names or the Cloudflare tunnel |
| 8443 | every address with one interface; the internal interface's address once the console configures a second one without a gateway | MSP administrators: the admin UI and API, the factory password until the first login |
| 9440 | `127.0.0.1` | nobody, until you bind the pass-through to an internal address in `portico.yaml`; it serves one site (`passthrough_site` in the settings, the first by default) |
| 8080 | `127.0.0.1` | Portico only: Keycloak |
| 22 | every address with one interface; the internal interface's address once the console configures a second one without a gateway; keys only | whoever has a key in `admin`'s `authorized_keys` (cloud-init) |

**Never let 8443 be reachable from the Internet.** With two interfaces
the console binds it to the internal one. With a single interface (a
cloud instance, one public address) it answers on that address: forward
only 443 to the appliance (security group, NAT, upstream firewall), and
log in soon after the first boot to replace the factory password.

## The appliance image

1. **Download** `portico-0.1.0.qcow2` and `portico-0.1.0.qcow2.sha256`
   from the [release](https://github.com/marco-fabbri/portico/releases/tag/v0.1.0)
   into one directory and check them with
   `sha256sum -c portico-0.1.0.qcow2.sha256`. The OpenSCAP report of
   that build is next to them.
2. **Create the VM**: with OpenTofu, `deploy/tofu/nutanix` uploads the
   image from the release and creates the VM (2 vCPU, 6 GiB, 40 GiB,
   legacy BIOS), with an optional static network and SSH key through
   cloud-init (see its README). By hand: import the qcow2 into the image
   service, create a VM with those sizes and one NIC, power it on.
3. **The console** (Prism Central, Launch console): the menu is on tty1
   and shows the state of `portico`, `keycloak`, `postgres` and
   `cloudflared` on its second line.
   - *1) Network*: hostname, address, prefix, gateway, DNS for each
     interface; skip it when cloud-init already set them.
   - *4) Keycloak administrator credentials (show once)*: write them
     down for the Identity card of the setup page.
   - *3) Exposure (direct certificate or Cloudflare tunnel)*:
     *1) Direct: install a certificate and key for the public names*
     asks for the "Certificate file (PEM, full chain)" and the "Private
     key file (PEM)": give the paths of the files copied in
     [Certificates](#certificates), such as
     `/home/admin/portal-chain.crt`. Pressing Enter at a path pastes the
     PEM instead, which works over SSH (`sudo portico console`) but not
     in Prism Central's console window. *2)* makes a self-signed pair
     for a first try; *3)* takes the Cloudflare tunnel token.
   - *2) Admin password*: optional here; the web asks at the first
     login.

   The first boot pulls the container images: Keycloak is ready about
   a minute after the network is up on a slow link, a little after the
   status line shows `keycloak: active` (*5) Refresh*).
4. **The first login**, `https://<address>:8443/` (self-signed internal
   certificate): `admin` with the factory password `portico/4u`; the
   appliance asks for a new password (at least 12 characters) before
   showing anything else. Do it soon after the first boot: until then
   anyone reaching 8443 on your internal network could do it instead.
5. **The setup page**, card by card, in this order (a site needs the
   public names and the Keycloak realm first):
   - *Public names*: Portal public host and Identity public host, then
     Save.
   - *Identity*: open "Let Portico create the realm and clients" and
     give the Keycloak administrator from step 3. Portico creates the
     realm, its clients and their secrets and stores the secrets.
   - *Sites*: "Add site" for each Prism Central. *1. The address*:
     the Prism Central address, then "Read the certificate" and compare
     the SHA-256 fingerprint it shows with the one your browser shows
     for that Prism Central. Code
     and Name are what tenants see. *2. The administrator*: Username and
     Password of the one-time account. "Add the site" creates the
     service account, its API key and the SAML trust in one step, and
     lists each step with its result. Portico never stores a human
     administrator's credentials.
   - *Tenants*: the project and group prefixes, the role in the
     project, the operation timeout; the defaults fit a first install.
   - "Apply and restart": the appliance leaves setup mode and the
     public path opens.
6. **Branding** and the first **tenant** from the admin UI, placed on
   the sites where it gets resources. The tenant's user signs in at
   `https://portal.msp.example/` and, with several sites, chooses one.

The appliance has no root password and no SSH user unless cloud-init
added one: the console is the way in.

## An existing Rocky Linux 9 host

The same components on a Rocky Linux 9 host of yours (a VM on any
hypervisor, a cloud instance). As root:

```
curl -fsSLO https://github.com/marco-fabbri/portico/releases/download/v0.1.0/install-0.1.0.tar.gz
curl -fsSLO https://github.com/marco-fabbri/portico/releases/download/v0.1.0/install-0.1.0.tar.gz.sha256
sha256sum -c install-0.1.0.tar.gz.sha256
tar -xzf install-0.1.0.tar.gz
./deploy/install.sh --release 0.1.0
```

The installer ([deploy/README.md](../deploy/README.md)) installs the
packages, the service, the Keycloak and PostgreSQL containers, the
console on tty1, the firewalld service (443, 8443) and the SSH policy
(keys only, no root, members of `wheel` only). The SSH policy is
installed only when a `wheel` user already has a key, so the session
you install from is not locked out. The installer generates the
Keycloak secrets and the internal certificate once, downloads the
binary of the release and checks its checksum, and starts in setup
mode with the factory password. Then run the console (`portico
console` as root on tty1, or over SSH in a terminal) for the exposure,
and continue at step 4 above. The host's own hardening is yours: the
image's CIS baseline (`image/oscap`) is applied only by the image
build.

## Updating

The upgrade is in place; a new image does not take over an existing
appliance's state in 0.1.0.

1. **Take a snapshot of the VM** in Prism Central, or check that its
   protection policy has a recent one. The snapshot is the backup: it
   holds Portico's store and master key and Keycloak's database (the
   realm and every tenant user), which lives outside `/var/lib/portico`.
2. On the appliance, as root, download and check
   `install-<version>.tar.gz` as above, unpack it and run
   `./deploy/install.sh --release <version>`.
3. The installer stops Portico, copies the store and the master key to
   `/var/lib/portico/backup-<old version>-<time>/`, replaces the binary
   and its files, restarts PostgreSQL and Keycloak only when their units
   changed, and starts Portico again. Open sessions end; tenants sign in
   again. The installer also updates the packages it installs.

To go back, restore the snapshot. The backup directory undoes a store
migration alone ([operating guide](operating.md#backup)).
