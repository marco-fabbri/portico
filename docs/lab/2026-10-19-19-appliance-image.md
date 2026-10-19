# 19 — The appliance image on NTNX-01 (Milestone 3 acceptance)

## Question

Whether an appliance built from the image (Rocky Linux 9, CIS Level 1
Server, Portico, Keycloak, PostgreSQL, the console wizard) boots on
Prism Central from a download, configures itself from the console and
the setup page, and lets a tenant user in; what the build, the upload
and the first boot cost; what Prism Central and Keycloak demanded on the
way.

## Measured on

- Prism Central pc.7.6.0.6 (single-node AHV cluster NTNX-01), Keycloak
  26.3.5, Rocky Linux 9.8, Packer 1.11.2 with the QEMU plugin on
  GitHub-hosted `ubuntu-24.04` runners (KVM), OpenTofu 1.8/1.13 with the
  Nutanix provider 2.5
- Portico `0.0.0-dev` builds of branch `m3-appliance-image`, 28 workflow
  runs on 2026-10-19; the final one is 39308957377 (the acceptance VM ran
  the image of run 39253233122 plus the Keycloak hostname fix applied by
  hand and the final binary installed over SSH)
- Date: 2026-10-19
- By: Claude (Marco's session) for the builds, the scripts and the API
  runs; Marco in the Prism Central VM console and in Chrome

## Method

1. `.github/workflows/image.yml` on every push of the milestone branch
   that touches `image/` or `deploy/`: binary, two-stage Packer build
   (`image/build.sh`), the boot test (`image/scripts/boottest.sh`, twice:
   a config drive in AHV's format and a NoCloud seed), artifacts.
2. `lab/appliance/deploy.sh --run <id>`: artifact download, copy to the
   lab VM, HTTP server for Prism Central's image service, OpenTofu
   (`deploy/tofu/nutanix`) with the lab values, static network and SSH
   key through cloud-init, wait for the admin listener.
3. Marco on the VM console (the wizard); the setup through the admin API
   (`lab/portico/bootstrap-acceptance.sh` parametrised for the
   appliance, then the missing steps by hand after Prism Central's
   hiccups); `lab/tenants/acceptance.sh` for tenant `acme` and carol;
   Marco in Chrome with `/etc/hosts` on `.191`.

## Result

- MEASURED, build: anaconda from its kernel and initrd (netinstall from
  `dl.rockylinux.org`) 12 to 18 min, provisioning 3 min (`install.sh
  --no-start` 20 s, OpenSCAP remediation 45 s and evaluation 27 s),
  boot tests 1 min each; whole job 20 to 25 min. OpenSCAP: 271 rules
  pass, 0 fail with the three documented exceptions
  (`image/oscap/README.md`); the remaining findings were real and fixed
  (journald alone, journal-remote socket masked, `AllowGroups wheel`).
- MEASURED, size: 2.70 GB compressed before discard; 1.35 GB compressed
  and 2.05 GB uncompressed with `disk_discard = "unmap"` (the cleanup's
  `fstrim` finally reached the image); 1.36 GB uncompressed without
  `linux-firmware` (679 MB, nothing requires it on a VM) and the GeoIP
  packages (final build: 1,359,872,000 bytes). GitHub release assets are
  limited to 2 GiB.
- MEASURED, the CI boot test (`image/scripts/boottest.sh`, KVM on the
  runner): with a config drive in AHV's format and with a NoCloud seed,
  SSH up 15 s after power-on, cloud-init done, the first boot's secrets
  and TLS pairs made, every unit active, the firewall with the `portico`
  service, every LVM volume mounted, SELinux enforcing; one minute per
  run. The user data is rendered from the module's template through a
  provider-less harness (`image/scripts/render-user-data`): the
  `setup-opentofu` wrapper swallows the stdin of `tofu console`, and the
  module's own `tofu console` waited on the Nutanix provider.
- MEASURED, where to build: the Mac is arm64 and the lab VM has no
  nested virtualization (an emulated boot took 200 s to reach the LVM
  timeouts); a Rocky 9 VM on the lab's Proxmox with CPU type host
  (`lab/build-host/`, 4 vCPU, 8 GiB) has KVM and runs `image/build.sh`
  and the boot test like the runner. Twenty-eight runner builds were the
  price of not having it from the start.
- MEASURED: Prism Central's image service refuses a qcow2 with
  compressed clusters (`kImageCreate` fails after 5 min with "dependent
  backend service has encountered an error", twice); the same image
  converted to plain qcow2 imports in 4 to 7 min from a lab HTTP server;
  a 5 MB probe imports in seconds. `disk_compression = false`.
- MEASURED, the build on the runner: QEMU's default `qemu64` CPU lacks
  x86-64-v2 and Rocky 9's glibc refuses to start (kernel panic in the
  serial log): `-cpu host`. The boot command typed over VNC on the ISO
  menu never produced a usable install: anaconda booted from its kernel
  with the kickstart on a fixed HTTP port is deterministic, and the
  serial console written to a file outside Packer's output directory
  is the only sight of the guest. `/tmp` noexec breaks Packer's shell
  provisioner unless the scripts run through `bash`. Packer's plugin
  getter is rate-limited without `PACKER_GITHUB_API_TOKEN`.
- MEASURED, the image on AHV: the kickstart leaves predictable interface
  names (`ens3`) unless `net.ifnames=0` is on the kernel line; without it
  the `eth0` keyfile cloud-init writes matches nothing. A kickstart does
  not enable `cloud-init.target` (the GenericCloud image does by preset):
  cloud-init never ran. Anaconda's LVM devices file pins the physical
  volume to the build VM's disk: removed, `use_devicesfile = 0`.
- MEASURED, the installer: with `--no-start` the Keycloak secrets and the
  TLS pairs were generated at build time, the same in every appliance,
  and the firewall rule was never written: both moved to
  `portico-firstboot.service` (`install.sh --first-boot`, before any
  package step, no network needed); the image cleanup removes every
  secret. A quadlet's `EnvironmentFile=-path` is passed to podman with the
  dash: the Keycloak hostname file exists from the install, empty.
- MEASURED, OpenTofu: `indent()` leaves the first line unindented, so the
  keyfile in the cloud-init template made the whole user data invalid
  YAML; cloud-init dropped it silently (no user, no network) on two
  appliances. The plan now refuses invalid YAML (`cloud_init_valid`) and
  the CI boot test renders the module's template.
- MEASURED, Keycloak: `KC_HOSTNAME_STRICT=false` alone is not enough: the
  SAML descriptor read over the loopback names the loopback as entity ID
  and the SAML bootstrap refuses it. With `KC_HOSTNAME=https://<id_host>`
  and the dynamic backchannel (written by Portico on every settings save
  that carries `id_host`, `keycloak-hostname.path` restarting Keycloak)
  the entity ID is the public name.
- MEASURED, first boot on AHV with the fixed cloud-init: network up from
  cloud-init, SSH as `admin` with the key, every unit active, Keycloak
  answering 90 s after boot; the wizard on tty1 (Marco set the network
  there on the first VM, when cloud-init had not).
- MEASURED, setup through the API on the appliance: Keycloak bootstrap
  13 s; Prism Central bootstrap 4 s once IAM answered (two earlier
  attempts: 151 s timeout on the policy update, then 500 "Failed to
  store new cookie in IDF"); SAML trust 4 s with the admin credentials
  after the service account's 403 (as in write-up 15); Apply, back in
  running mode. Tenant `acme`: six steps in seconds, the visibility check
  hit the same 500, the provisioning retried and completed; carol
  created. Prism Central answered that 500 intermittently for about 20
  minutes after the deletion of the old identity provider, SAML group
  and service account.
- MEASURED, Prism Central housekeeping: a SAML identity provider cannot
  be deleted while a SAML user group exists (IAM refuses with "users or
  user groups associated"); v4 deletes need `If-Match` (428 without);
  extIds are derived from names (the new `portico` service account got
  the old one's extId).

## Implications for the design

- The appliance has no root password and the console wizard on tty1;
  Marco found typing the break-glass password there awkward: the first
  access moves to the web with a factory password (`admin` /
  `portico/4u`, Nutanix's convention) and a forced change at the first
  login; the wizard keeps the option. Design 4.2 updated.
- Keycloak's public hostname is a settings concern, not an Apply
  concern (design 4.9 updated).
- The image build is the CI's: a release is a tag, nothing is built on a
  workstation.

## How to re-measure after an upgrade

`gh workflow run image.yml -f version=0.0.0-dev` (or a push touching
`image/`), then `source lab/env.sh && lab/appliance/deploy.sh --run <id>`
with `~/portico-secrets-appliance/` holding `prism-pw`, `lab-ca.crt`,
`lab-ca.key` and `portico-admin-pw`; the setup with
`PORTICO_SECRETS=~/portico-secrets-appliance LAB_VM_IP=10.23.210.191
LAB_REALM=portico LAB_SERVICE_ACCOUNT=portico LAB_SAML_IDP=portico-keycloak
LAB_PROJECT_PREFIX=portico lab/portico/bootstrap-acceptance.sh` and the
tenant with the `LAB_*` variables of `lab/tenants/acceptance.sh`.
`lab/appliance/destroy.sh` removes the VM and the image.
