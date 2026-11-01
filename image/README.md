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

## Edge router image

`portico-edge-<version>.qcow2`: the edge router Portico builds for a site
whose router cannot peer by BGP (prefix-routing design 4.3 and 4.4).
Rocky Linux 9 GenericCloud with FRR and nftables, hardened with the same
CIS Level 1 Server profile (`oscap/edge-tailoring.xml`), cloud-init
enabled, no user, no password, no SSH server and no SSH host key: the
edge gets its address, its BGP neighbour and its firewall from the
cloud-init Portico sends when it creates the VM
(`internal/peering/edge_userdata.tmpl`). Legacy BIOS boot, 10 GiB disk,
1 vCPU and 1 GiB RAM, about 1 GiB of image.

| File | Role |
|---|---|
| `edge.pkr.hcl` | Packer: the GenericCloud image booted with a NoCloud seed for the build user, `scripts/edge-provision.sh install`, `scripts/harden.sh` with the edge's tailoring, the report download, `scripts/edge-provision.sh cleanup`, the sha256 |
| `scripts/edge-provision.sh` | `install`: FRR, nftables, cloud-init without a default user or network rendering; `cleanup` and `poweroff`: close the image, remove the build user and the SSH server |
| `oscap/edge-tailoring.xml`, `oscap/edge-expected-failures.txt` | The appliance's exceptions, plus the SSH server rules set aside |
| `scripts/edge-boottest.sh`, `scripts/edge-boottest-user-data.yaml` | Boot test on KVM from the serial console with the user data Portico sends (the fixture is kept equal to it by a test of `internal/peering`) |

The workflow builds it in a job of its own next to the appliance and
attaches it to the release with its checksum and report. Portico imports
it into a site's image service by URL from the release, or finds it by
name when the MSP uploaded it (design 4.4).
