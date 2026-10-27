# 33 — v0.1.0 from the release image: a scripted rehearsal and the demo

## Question

Does the published image of v0.1.0 (tag on `96f592b`) set up from zero
on NTNX-01 and NYC1, serve two tenants on their sites with VMs in their
projects, and come apart again cleanly, before the release is published
and before the setup is recorded by hand along `docs/install.md`?

## Measured on

- Prism Central: pc.7.6.0.6 on NTNX-01 (`10.23.210.175`) and NYC1
  (`10.23.210.185`)
- Portico: `portico 0.1.0`, image of run 40481607487 (draft release
  v0.1.0, 8 assets)
- Date: 2026-10-27
- By: Claude, with Marco away; the hand setup is his, after this

## Method

1. Downloaded `portico-0.1.0.qcow2` and its `.sha256` from the draft
   with `gh release download` and checked them (`shasum -a 256 -c`).
2. Removed the previous appliance and its image (`lab/appliance/destroy.sh`).
3. Served the image from the build host (`LAB_IMAGE_HOST=10.23.220.90
   LAB_IMAGE_USER=build lab/appliance/deploy.sh --image … --version 0.1.0`).
4. When that failed (below), uploaded the qcow2 from the Mac: `POST
   /api/nutanix/v3/images` (name `portico-0.1.0`, no source) then `PUT
   /api/nutanix/v3/images/{uuid}/file` with `curl -T` (streamed;
   `--data-binary` runs out of memory on 1.35 GB). Imported it into the
   OpenTofu state (`tofu import nutanix_images_v2.portico <extId>`), set
   `portico_image_url = "file://<extId>"` in `lab/appliance/terraform.tfvars`
   (the URL Prism Central records for an upload, so the plan has no
   change on the image) and applied: the VM only.
5. Set the break-glass password over SSH (`portico admin set-password`
   from the file), then the setup through the admin API and, for the
   exposure card, the setup page's form with `curl` (session, CSRF,
   multipart with `lab-leaf-chain.crt` and `lab-leaf.key`): public
   names, certificate, the realm job, `AZ-IT-MI1` and `AZ-US-NYC1` in
   one step each with the administrators' passwords from their files,
   the tenants card, Apply.
6. `lab/portico/demo-tenants.sh` (new): tenants acme (both sites) and
   globex (first site), users `carol@acme.example` and
   `dave@globex.example` with permanent passwords in 0600 files, VMs
   `acme-web-01`, `acme-db-01` (MI1), `acme-web-02` (NYC1),
   `globex-app-01`, `globex-app-02` (MI1), Rocky 9 GenericCloud, 1 vCPU,
   1 GiB, powered on. The image `rocky-9-genericcloud` was created on
   NYC1 from its public URL first.
7. Through the public path with `curl`
   (`LAB_LOGIN_EMAIL=… LAB_LOGIN_PWFILE=… lab/sites/tenant-login.sh`):
   each user's VM list read with the session cookie; Basic auth;
   `/.portico/logout` with `Sec-Fetch-Site: cross-site` and then
   `same-origin`.
8. Teardown through Portico: the VMs, each tenant's Disconnect, the
   tenants, each site's Remove with the administrator's credentials;
   then the VM only (`tofu destroy -target=nutanix_virtual_machine_v2.portico`)
   and a new one from the same image, with no password set.

## Result

- MEASURED: the draft has the 8 assets (qcow2 1 354 760 192 bytes, the
  binary tarball, the install tarball, the OpenSCAP report, each with
  its `.sha256`); the qcow2's checksum matches.
- MEASURED: Prism Central (VLAN 210) cannot download from the build host
  (`10.23.220.90`, VLAN 220 behind Proxmox): "Failed to create the image
  as the provided URL timed out after streaming 0 bytes", while the Mac
  downloads the same URL. The build host is no image host for NTNX-01.
- MEASURED: the cause is a policy between the two VLANs, not routing.
  Both gateways (`10.23.210.254`, `10.23.220.254`) answer with the same
  MAC (`00:09:0f:09:00:04`, a Fortinet prefix), so one firewall routes
  both; each side reaches its gateway and the Internet (first hop the
  gateway, second `93.43.51.2`), but from `10.23.210.191` the build host
  answers neither ping nor TCP 22/8000, and from the build host
  `10.23.210.254`, `.175` and `.191` answer no ping. The Mac reaches
  both over the VPN.
- MEASURED: the upload from the Mac over the VPN took 1 267 s for
  1.35 GB; the image became `COMPLETE` with 42 949 672 960 bytes (the
  disk's virtual size). After the import, `tofu plan` wanted to change
  `url_source.url` from `file://<extId>` to the variable's URL, in place;
  with the variable set to `file://<extId>` it plans the VM only.
- MEASURED: `tofu apply` of the VM took 161 s and 149 s; `/healthz`
  answered 200 when the apply returned. Keycloak was `activating` at
  first (the first boot pulls its image) and `active` within the next
  minutes; no AVC denial in the boot's kernel log;
  `NetworkManager-wait-online` failed after its 60 s at both boots
  (known, `docs/backlog.md`).
- MEASURED: setup timings from the first call: public names saved at
  1 s; the certificate request answered "certificate installed" at 5 s
  (the public listener serves it once the appliance runs); the realm
  job done before 112 s (the realm and both clients were no longer
  missing, the tenants card was); `AZ-IT-MI1` in one step at 122 s and
  `AZ-US-NYC1` at 131 s, every step ok (certificate pinned, version
  7.6.0.6, rights, identity provider created, service account `portico`
  with a new key, SAML trust, defaults, live); Apply and running at
  137 s. After Apply port 443 presents `CN=msp.portico.test` issued by
  the lab CA, the portal answers a browser navigation with 302 to
  `/.portico/login`, and `id.portico.test` serves the realm's OIDC
  discovery (200).
- MEASURED: `demo-tenants.sh` took 358 s the first time and stopped on
  globex's VMs: Prism Central answered the projects list with 504
  "upstream request timeout" (as on 2026-10-27 for the vmm lists). The
  second run took 175 s and passed, keeping what existed; the script now
  tries project and image lookups six times.
- MEASURED: through the public path, carol on `AZ-IT-MI1` lists
  `acme-db-01`, `acme-web-01`; carol on `AZ-US-NYC1` lists `acme-web-02`;
  dave (one site, no site page) lists `globex-app-01`, `globex-app-02`.
  Each login ends on 200 at `/console/`. Basic auth on the public path:
  403.
- MEASURED: `/.portico/logout` with `Sec-Fetch-Site: cross-site` leaves
  the session working (VM list 200 after it); with `same-origin` it
  redirects to Keycloak's logout and the next VM list is 401.
- MEASURED: a VM deleted a minute before (`acme-web-02`, in the
  Recycle Bin) did not stop the Disconnect of acme from `AZ-US-NYC1`: the
  resource group and the project were deleted. So the Recycle Bin is not
  why the leftover project `portico-lab-tenant-a` refuses its resource
  group's deletion ("not empty"); that cause is still unknown.
- MEASURED: each site's Remove with the administrator's credentials
  deleted the identity provider, its SAML users (1 on NYC1, 2 on MI1),
  the service account's API key and the Keycloak client, and left the
  service account. Afterwards neither Prism Central has an identity
  provider or a `portico-*` tenant project. The first teardown pass
  stopped on 504s from NTNX-01 (a VM delete, then the VM list); the
  second finished it.
- MEASURED: the new VM from the same image starts in setup mode, the
  first-boot Keycloak administrator present, no admin password set.
- NOT MEASURED: a VM console through the public path (WebSocket), and
  the internal pass-through's cookie stripping; both are left to the
  recorded run in a browser.
- OBSERVED: the lab CA's name, `portico-lab`, shows in the certificate
  details of the browser.

## Implications for the design

None. For the lab: an image host for NTNX-01 must be on VLAN 210, or
the firewall must allow Prism Central (`10.23.210.175`) to the build host
on TCP 8000, or the image is uploaded from the Mac as above; the release URL needs the
release published. `lab/appliance/deploy.sh` reads
`LAB_IMAGE_USER`, `lab/tenants/api.sh` `LAB_ADMIN_CA`,
`lab/sites/tenant-login.sh` `LAB_LOGIN_EMAIL` and `LAB_LOGIN_PWFILE`.

## How to re-measure after an upgrade

From a fresh appliance: the setup as in step 5 (or by hand along
`docs/install.md`), then `source lab/env.sh &&
PORTICO_SECRETS=~/portico-secrets-appliance lab/portico/demo-tenants.sh`
(with `LAB_PRISM_HOST=portico LAB_ADMIN_CA=<the admin certificate>`
while 8443 presents its own self-signed certificate), then the logins
of step 7 and the teardown of step 8.
