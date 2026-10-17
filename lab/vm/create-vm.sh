#!/usr/bin/env bash
# Create the lab VM portico-lab on NTNX-01 through the v4 API.
# Idempotent: exits 0 with "already exists" if the VM is there.
# Requires `source lab/env.sh`.
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}" "${LAB_VM_IP:?}"
# shellcheck source=lab/vm/lib.sh
source lab/vm/lib.sh

VM_NAME="portico-lab"
IMAGE_NAME="rocky-9-genericcloud"
IMAGE_URL="https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud-Base.latest.x86_64.qcow2"
SUBNET_NAME="${LAB_SUBNET_NAME:-MGMT}"
GATEWAY="10.23.210.254"
SSH_KEY_FILE="${SSH_KEY_FILE:-$HOME/.ssh/id_rsa.pub}"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

if [ -n "$(vm_extid "$VM_NAME")" ]; then echo "already exists: $VM_NAME"; exit 0; fi

# 1. Image
img=$(image_extid "$IMAGE_NAME")
if [ -z "$img" ]; then
  echo "creating image $IMAGE_NAME from $IMAGE_URL"
  jq -n --arg n "$IMAGE_NAME" --arg u "$IMAGE_URL" \
    '{name:$n, type:"DISK_IMAGE", source:{"$objectType":"vmm.v4.content.UrlSource", url:$u}}' > "$tmp/img.json"
  task=$(lab/pc.sh POST /api/vmm/v4.0/content/images "$tmp/img.json" 2>"$tmp/code" | jq -r '.data.extId')
  echo "image task $task ($(tr -d '\n' <"$tmp/code")), waiting"
  wait_task "$task" >/dev/null
  img=$(image_extid "$IMAGE_NAME")
fi
echo "image: $img"

# 2. Cluster and subnet
cluster=$(lab/pc.sh GET /api/clustermgmt/v4.0/config/clusters 2>/dev/null | jq -r '.data[] | select(.config.clusterFunction|index("AOS")) | .extId' | head -1)
subnet=$(lab/pc.sh GET /api/networking/v4.0/config/subnets 2>/dev/null | jq -r --arg n "$SUBNET_NAME" '.data[] | select(.name==$n) | .extId' | head -1)
echo "cluster: $cluster  subnet($SUBNET_NAME): $subnet"
[ -n "$cluster" ] && [ -n "$subnet" ]

# 3. VM
sed -e "s|__SSH_KEY__|$(cat "$SSH_KEY_FILE")|" -e "s|__IP__|$LAB_VM_IP|" -e "s|__GW__|$GATEWAY|" lab/vm/cloud-init.yaml > "$tmp/user-data"
userdata=$(base64 < "$tmp/user-data" | tr -d '\n')
jq -n --arg n "$VM_NAME" --arg c "$cluster" --arg s "$subnet" --arg i "$img" --arg ud "$userdata" '{
  name: $n, numSockets: 2, numCoresPerSocket: 1, memorySizeBytes: 6442450944,
  cluster: {extId: $c},
  bootConfig: {"$objectType":"vmm.v4.ahv.config.LegacyBoot"},
  disks: [{
    diskAddress: {busType:"SCSI", index:0},
    backingInfo: {"$objectType":"vmm.v4.ahv.config.VmDisk", diskSizeBytes: 42949672960,
      dataSource: {reference: {"$objectType":"vmm.v4.ahv.config.ImageReference", imageExtId: $i}}}
  }],
  nics: [{
    backingInfo: {"$objectType":"vmm.v4.ahv.config.EmulatedNic", model:"VIRTIO"},
    networkInfo: {nicType:"NORMAL_NIC", subnet:{extId:$s}}
  }],
  guestCustomization: {config: {"$objectType":"vmm.v4.ahv.config.CloudInit", datasourceType:"CONFIG_DRIVE_V2",
    cloudInitScript: {"$objectType":"vmm.v4.ahv.config.Userdata", value: $ud}}}
}' > "$tmp/vm.json"
task=$(lab/pc.sh POST /api/vmm/v4.0/ahv/config/vms "$tmp/vm.json" 2>"$tmp/code" | tee "$tmp/vm-resp.json" | jq -r '.data.extId // empty')
if [ -z "$task" ]; then echo "VM create failed ($(tr -d '\n' <"$tmp/code")):"; cat "$tmp/vm-resp.json"; exit 1; fi
echo "vm task $task, waiting"; wait_task "$task" >/dev/null
vm=$(vm_extid "$VM_NAME"); echo "vm: $vm"

# 4. Power on
t=$(vm_action "$vm" power-on); wait_task "$t" >/dev/null
echo "powered on: $VM_NAME at $LAB_VM_IP"
