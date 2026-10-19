# Appliance image

`portico-<version>.qcow2`: Rocky Linux 9 with Portico, Keycloak,
PostgreSQL, cloudflared and the console wizard installed, hardened with
OpenSCAP (`oscap/`), built from the boot ISO with `portico.ks` by Packer
(`portico.pkr.hcl`, two stages: anaconda from its kernel and initrd booted
directly, then provisioning over SSH on the installed disk). Legacy BIOS boot, 40 GiB disk, 2 vCPU and 6 GiB RAM
recommended at creation.

| File | Role |
|---|---|
| `portico.ks` | Kickstart: the CIS partition layout on LVM, packages, the build user, tmpfs and cloud-init settings; `%pre` refuses a disk under 40 GiB |
| `portico.pkr.hcl` | Packer: QEMU build from the boot ISO, `deploy/install.sh --no-start`, `scripts/harden.sh`, the report download, `scripts/cleanup.sh`, the sha256 |
| `oscap/tailoring.xml`, `oscap/README.md` | The CIS Level 1 Server profile with the documented exceptions |
| `scripts/harden.sh` | OpenSCAP remediation and evaluation, report to `output/` |
| `scripts/cleanup.sh`, `scripts/poweroff.sh` | Close the image: no cache, log, host key, machine id, build network or build user |
| `build.sh` | The same build on an x86_64 host with KVM |

The workflow `.github/workflows/image.yml` builds it on a tag `v*` (and by
hand) and attaches the image, its checksum, the OpenSCAP report and the
binary tarball to the GitHub release. The image carries no secret and no
public name: `install.sh` generates the Keycloak secrets and the internal
TLS pair at the first boot, the setup page stores the rest.
