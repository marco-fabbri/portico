#!/usr/bin/env bash
# Lab: the appliance image built by .github/workflows/image.yml on NTNX-01
# (Milestone 3 acceptance). Downloads the workflow artifacts, serves the
# qcow2 from the lab VM over HTTP for Prism Central's image service, and
# runs deploy/tofu/nutanix with the lab values. Requires `source lab/env.sh`
# and `gh` logged in. Lab only.
#   lab/appliance/deploy.sh [--run ID | --image PATH] [--version V]
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_VM_IP:?source lab/env.sh first}" "${PC_URL:?}" "${PC_USER:?}" "${PC_PASS:?}"
# shellcheck source=lab/lib.sh
source lab/lib.sh
RUN=""; IMAGE=""; VERSION="0.0.0-dev"
while [ $# -gt 0 ]; do
  case "$1" in
    --run) RUN=${2:?}; shift 2 ;;
    --image) IMAGE=${2:?}; shift 2 ;;
    --version) VERSION=${2:?}; shift 2 ;;
    *) echo "usage: lab/appliance/deploy.sh [--run ID | --image PATH] [--version V]" >&2; exit 2 ;;
  esac
done
APPLIANCE_IP="${LAB_APPLIANCE_IP:-10.23.210.191}"
GATEWAY="${LAB_GATEWAY:-10.23.210.254}"
SUBNET_NAME="${LAB_SUBNET_NAME:-MGMT}"
CLUSTER_NAME="${LAB_CLUSTER_NAME:-NTNX-01}"
A=lab/appliance
umask 077

# 1. The image: a workflow artifact (default: the latest successful build) or a local file.
if [ -z "$IMAGE" ]; then
  if [ -z "$RUN" ]; then
    RUN=$(gh run list --workflow image.yml --status success --limit 1 --json databaseId --jq '.[0].databaseId')
    [ -n "$RUN" ] || { echo "no successful image build; give --run ID or --image PATH" >&2; exit 1; }
  fi
  rm -rf "$A/artifact"; mkdir -p "$A/artifact"
  # 2.7 GB from the artifact store: a timeout mid-way is common over a VPN.
  for attempt in 1 2 3; do
    gh run download "$RUN" -n "portico-$VERSION" -D "$A/artifact" && break
    echo "download attempt $attempt failed; retrying" >&2; rm -rf "$A/artifact"; mkdir -p "$A/artifact"; sleep 5
  done
  IMAGE="$A/artifact/portico-$VERSION.qcow2"
fi
[ -s "$IMAGE" ] || { echo "image not found: $IMAGE" >&2; exit 1; }
(cd "$(dirname "$IMAGE")" && sha256sum -c --quiet "$(basename "$IMAGE").sha256" 2>/dev/null) && echo "checksum ok" || echo "checksum file absent or mismatch: continuing with the artifact as downloaded"

# 2. The address must be free (as lab/vm/create-vm.sh checks).
if ping -c 2 -W 1 "$APPLIANCE_IP" >/dev/null 2>&1; then echo "refusing: $APPLIANCE_IP answers ping" >&2; exit 2; fi

# 3. Serve the image from the lab VM: Prism Central downloads it from a URL.
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=5 "lab@$LAB_VM_IP")
stage=$(remote_stage_dir "lab@$LAB_VM_IP")
echo "copying $(du -h "$IMAGE" | cut -f1) to the lab VM"
scp -q -o BatchMode=yes "$IMAGE" "lab@$LAB_VM_IP:$stage/portico-$VERSION.qcow2"
"${SSH[@]}" "sudo firewall-cmd -q --add-port=8000/tcp; cd '$stage' && (nohup python3 -m http.server 8000 >/dev/null 2>&1 & echo \$! > '$stage/http.pid')"
echo "$stage" > "$A/.stage"
IMAGE_URL="http://$LAB_VM_IP:8000/portico-$VERSION.qcow2"

# 4. The OpenTofu variables: nothing secret in the file, the password in the environment.
host=${PC_URL#https://}; host=${host%%:*}
sshkey=""
for k in "$HOME/.ssh/id_ed25519.pub" "$HOME/.ssh/id_rsa.pub"; do [ -r "$k" ] && { sshkey=$(cat "$k"); break; }; done
cat > "$A/terraform.tfvars" <<VARS
nutanix_endpoint     = "$host"
nutanix_username     = "$PC_USER"
nutanix_insecure     = true
nutanix_cluster_name = "$CLUSTER_NAME"
nutanix_subnet_name  = "$SUBNET_NAME"
nutanix_subnet_ipam  = false
portico_version      = "$VERSION"
portico_image_url    = "$IMAGE_URL"
vm_name              = "portico-appliance"
network = {
  address = "$APPLIANCE_IP"
  prefix  = 24
  gateway = "$GATEWAY"
  dns     = ["1.1.1.1", "1.0.0.1"]
}
ssh_public_key = "$sshkey"
VARS

# 5. Apply.
export TF_VAR_nutanix_password="$PC_PASS"
t0=$(date +%s)
tofu -chdir=deploy/tofu/nutanix init -input=false >/dev/null
tofu -chdir=deploy/tofu/nutanix apply -input=false -auto-approve -var-file="$PWD/$A/terraform.tfvars" -state="$PWD/$A/terraform.tfstate"
echo "apply done in $(( $(date +%s) - t0 )) s (image upload included)"

# 6. Wait for the admin listener (cloud-init network path, Review Focus 4).
t1=$(date +%s)
for _ in $(seq 1 90); do
  if curl -sk -o /dev/null -w '%{http_code}' "https://$APPLIANCE_IP:8443/healthz" 2>/dev/null | grep -q 200; then
    echo "appliance ready: healthz 200 after $(( $(date +%s) - t1 )) s from apply; admin UI https://$APPLIANCE_IP:8443/ (user admin: set the password on the VM console first)"
    echo "console: Prism Central > VMs > portico-appliance > Launch console"
    exit 0
  fi
  sleep 2
done
echo "the appliance did not answer on 8443 within 180 s: open its console" >&2; exit 1
