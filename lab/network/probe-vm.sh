#!/usr/bin/env bash
# A VM on a tenant's ready network, to check the data path (write-up 34,
# tenant networking design section 6): DHCP on the overlay subnet, the
# Internet through the VPC's SNAT, and the floating IP from the VLAN.
#   source lab/env.sh && lab/network/probe-vm.sh create|check|delete PROJECT
# PROJECT has a ready network from lab/network/tenant-network.sh. The VM
# `<project>-vm` (1 vCPU, 1 GiB, IMAGE, default rocky-9-genericcloud) gets
# the user `probe` with SSH_KEY_FILE (default ~/.ssh/id_rsa.pub), and the
# floating IP `<project>-fip` is associated with its NIC. `check` connects
# to the floating IP from this host, or through LAB_JUMP (user@host on the
# VLAN). Lab only.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}"
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh
# shellcheck source=lab/vm/lib.sh
source lab/vm/lib.sh

action="${1:?usage: probe-vm.sh create|check|delete PROJECT}"
project="${2:?usage: probe-vm.sh create|check|delete PROJECT}"
[[ "$project" == portico-* ]] || { echo "refusing: project name must start with portico-: $project" >&2; exit 2; }
NET=/api/networking/v4.4/config
VMM=/api/vmm/v4.0/ahv/config
vm_name="$project-vm"; subnet_name="$project-net"; fip_name="$project-fip"
key_file="${SSH_KEY_FILE:-$HOME/.ssh/id_rsa.pub}"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

by_name() { lab/pc.sh GET "$NET/$1" 2>/dev/null | jq -r --arg n "$2" '.data[]? | select(.name == $n) | .extId'; }
fip_value() { lab/pc.sh GET "$NET/floating-ips/$1" 2>/dev/null | jq -r '.data.floatingIp.ipv4.value // .data.floatingIpValue // empty'; }

# set_fip FIP_EXTID [NIC_EXTID]: associate with the NIC, or disassociate
set_fip() {
  local etag task
  lab/pc.sh GET "$NET/floating-ips/$1" 2>/dev/null | jq --arg nic "${2:-}" '.data
    | del(.["$reserved"], .links, .tenantId, .metadata, .associationStatus, .externalSubnet, .vmNic, .vpc, .vmNicReference, .vpcReference, .privateIp, .["$associationItemDiscriminator"])
    | if $nic == "" then del(.association) else .association = {"$objectType":"networking.v4.config.VmNicAssociation", vmNicReference:$nic} end' > "$tmp/fip.json"
  etag=$(etag_of "$NET/floating-ips/$1")
  task=$(IF_MATCH="$etag" lab/pc.sh PUT "$NET/floating-ips/$1" "$tmp/fip.json" 2>"$tmp/err" | jq -r '.data.extId // empty')
  if [ -n "$task" ]; then wait_task "$task"; else echo "refused, $(tail -1 "$tmp/err")"; return 1; fi
}

case "$action" in
  create)
    p=$(project_extid "$project"); [ -n "$p" ] || { echo "no project $project" >&2; exit 1; }
    s=$(by_name subnets "$subnet_name"); [ -n "$s" ] || { echo "no subnet $subnet_name" >&2; exit 1; }
    f=$(by_name floating-ips "$fip_name"); [ -n "$f" ] || { echo "no floating IP $fip_name" >&2; exit 1; }
    img=$(image_extid "${IMAGE:-rocky-9-genericcloud}"); [ -n "$img" ] || { echo "no image" >&2; exit 1; }
    cl=$(lab/pc.sh GET /api/clustermgmt/v4.0/config/clusters 2>/dev/null | jq -r '.data[] | select(.config.clusterFunction|index("AOS")) | .extId' | head -1)
    vm=$(vm_extid "$vm_name")
    if [ -z "$vm" ]; then
      printf '#cloud-config\nusers:\n  - name: probe\n    sudo: ALL=(ALL) NOPASSWD:ALL\n    ssh_authorized_keys:\n      - %s\n' "$(<"$key_file")" | base64 | tr -d '\n' > "$tmp/ud"
      jq -n --arg n "$vm_name" --arg c "$cl" --arg p "$p" --arg i "$img" --arg s "$s" --rawfile ud "$tmp/ud" '{
        name:$n, numSockets:1, numCoresPerSocket:1, memorySizeBytes:1073741824,
        cluster:{extId:$c}, project:{extId:$p},
        bootConfig:{"$objectType":"vmm.v4.ahv.config.LegacyBoot"},
        disks:[{diskAddress:{busType:"SCSI", index:0}, backingInfo:{"$objectType":"vmm.v4.ahv.config.VmDisk", diskSizeBytes:10737418240,
          dataSource:{reference:{"$objectType":"vmm.v4.ahv.config.ImageReference", imageExtId:$i}}}}],
        nics:[{backingInfo:{"$objectType":"vmm.v4.ahv.config.EmulatedNic", model:"VIRTIO"},
          networkInfo:{nicType:"NORMAL_NIC", subnet:{extId:$s}}}],
        guestCustomization:{config:{"$objectType":"vmm.v4.ahv.config.CloudInit", datasourceType:"CONFIG_DRIVE_V2",
          cloudInitScript:{"$objectType":"vmm.v4.ahv.config.Userdata", value:$ud}}}}' > "$tmp/vm.json"
      t=$(lab/pc.sh POST "$VMM/vms" "$tmp/vm.json" 2>"$tmp/err" | jq -r '.data.extId // empty')
      [ -n "$t" ] || { echo "VM create refused, $(tail -1 "$tmp/err")" >&2; exit 1; }
      echo "create VM: $(wait_task "$t" 300)"
      vm=$(vm_extid "$vm_name")
    fi
    t=$(vm_action "$vm" power-on); [ -z "$t" ] || [ "$t" = null ] || echo "power on: $(wait_task "$t" 120)"
    nic=$(lab/pc.sh GET "$VMM/vms/$vm" 2>/dev/null | jq -r '.data.nics[0].extId')
    echo "VM $vm_name, NIC $nic, address $(lab/pc.sh GET "$VMM/vms/$vm" 2>/dev/null | jq -r '.data.nics[0].networkInfo.ipv4Config.ipAddress.value // "none yet"')"
    echo "associate $fip_name: $(set_fip "$f" "$nic")"
    echo "floating IP: $(fip_value "$f")"
    ;;
  check)
    f=$(by_name floating-ips "$fip_name"); ip=$(fip_value "$f")
    [ -n "$ip" ] || { echo "no floating IP value" >&2; exit 1; }
    jump=(); [ -z "${LAB_JUMP:-}" ] || jump=(-J "$LAB_JUMP")
    ssh "${jump[@]}" -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "probe@$ip" \
      'echo "address: $(ip -4 -o addr show scope global | awk "{print \$4}")"; echo "route: $(ip route | head -1)"; echo "resolv: $(grep -m1 nameserver /etc/resolv.conf)"; echo "internet: $(curl -s -o /dev/null -w "%{http_code} in %{time_total}s" https://dl.rockylinux.org/)"'
    ;;
  delete)
    f=$(by_name floating-ips "$fip_name")
    [ -z "$f" ] || echo "disassociate $fip_name: $(set_fip "$f")"
    vm=$(vm_extid "$vm_name")
    if [ -n "$vm" ]; then
      t=$(vm_action "$vm" power-off); [ -z "$t" ] || [ "$t" = null ] || echo "power off: $(wait_task "$t" 120)"
      t=$(IF_MATCH="$(etag_of "$VMM/vms/$vm")" lab/pc.sh DELETE "$VMM/vms/$vm" 2>/dev/null | jq -r '.data.extId // empty')
      if [ -n "$t" ]; then echo "delete VM: $(wait_task "$t" 300)"; else echo "delete VM: refused"; fi
    fi
    ;;
  *) echo "usage: $0 create|check|delete PROJECT" >&2; exit 2 ;;
esac
