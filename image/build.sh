#!/usr/bin/env bash
# Build the appliance image locally: an x86_64 host with KVM, Packer and
# QEMU. The release workflow runs the same build on GitHub Actions.
#   image/build.sh VERSION BINARY
set -euo pipefail
cd "$(dirname "$0")"
: "${1:?version}" "${2:?path of the linux/amd64 portico binary}"
packer init portico.pkr.hcl
PACKER_LOG=${PACKER_LOG:-0} packer build -var "version=$1" -var "binary=$2" portico.pkr.hcl
ls -la output
