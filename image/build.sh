#!/usr/bin/env bash
# Build the appliance image, or the edge router image: an x86_64 host
# with KVM, Packer and QEMU (and xorriso or genisoimage for the edge's
# seed). The release workflow runs the same steps on GitHub Actions.
#   image/build.sh VERSION BINARY [MIRROR]    # portico-<version>.qcow2
#   image/build.sh edge VERSION [PACKER_ARGS…]  # portico-edge-<version>.qcow2
#     (e.g. -var base_url=/path/to/the/GenericCloud.qcow2 already downloaded)
set -euo pipefail
cd "$(dirname "$0")"
if [ "${1:-}" = edge ]; then
  : "${2:?version}"
  rm -rf output-edge serial-edge.log
  packer init edge.pkr.hcl
  packer build -color=false -var "version=$2" "${@:3}" edge.pkr.hcl
  tail -n 20 serial-edge.log || true
  ls -la output-edge
  exit 0
fi
: "${1:?version}" "${2:?path of the linux/amd64 portico binary}"
MIRROR=${3:-https://dl.rockylinux.org/pub/rocky/9/BaseOS/x86_64/os}
# Anaconda's kernel and initrd, booted directly (no ISO menu).
mkdir -p boot
for f in vmlinuz initrd.img; do
  [ -s "boot/$f" ] || curl -fsSL --retry 5 --retry-all-errors --http1.1 -o "boot/$f" "$MIRROR/images/pxeboot/$f"
done
ls -la boot
rm -rf output-install output serial-install.log serial-provision.log
packer init portico.pkr.hcl
packer build -color=false -only=qemu.install -var "version=$1" -var "mirror=$MIRROR" portico.pkr.hcl
tail -n 30 serial-install.log || true
packer build -color=false -only=qemu.provision -var "version=$1" -var "binary=$2" portico.pkr.hcl
ls -la output
