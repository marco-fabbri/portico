#!/usr/bin/env bash
# The router in front of the transit VPC when the site's router cannot be
# changed (write-up 35): a small VM on the VLAN that is the gateway of the
# no-NAT external subnet, routes the routed prefix to the transit VPC and
# NATs it to its own address, so the site's router only sees one more host
# of the VLAN. One router, one address: fine for a test site, a single
# point of failure for an MSP, whose own router or BGP gateway does this.
#   source lab/env.sh && lab/network/edge-router.sh create|show|delete
# Optional: EDGE_NAME (portico-edge), EDGE_IP (10.23.210.194), EDGE_GW
# (10.23.210.254), EDGE_DNS (1.1.1.1), ERP (100.96.0.0/24), TRANSIT_IP
# (10.23.210.200), EDGE_SUBNET (MGMT), IMAGE (rocky-9-genericcloud),
# SSH_KEY_FILE (~/.ssh/id_rsa.pub; the user is `admin`). The external
# subnet then gets EXT_GATEWAY=$EDGE_IP (lab/network/external-subnet.sh).
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}"
# shellcheck source=lab/vm/lib.sh
source lab/vm/lib.sh
NET=/api/networking/v4.4/config
VMM=/api/vmm/v4.0/ahv/config
name="${EDGE_NAME:-portico-edge}"; ip="${EDGE_IP:-10.23.210.194}"; gw="${EDGE_GW:-10.23.210.254}"
dns="${EDGE_DNS:-1.1.1.1}"; erp="${ERP:-100.96.0.0/24}"; transit="${TRANSIT_IP:-10.23.210.200}"
key_file="${SSH_KEY_FILE:-$HOME/.ssh/id_rsa.pub}"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

subnet_extid() { lab/pc.sh GET "$NET/subnets?\$limit=100" 2>/dev/null | jq -r --arg n "$1" '.data[]? | select(.name == $n) | .extId' 2>/dev/null; }

user_data() {
  cat <<EOF
#cloud-config
hostname: $name
users:
  - name: admin
    groups: [wheel]
    sudo: ["ALL=(ALL) NOPASSWD:ALL"]
    lock_passwd: true
    ssh_authorized_keys:
      - $(<"$key_file")
ssh_pwauth: false
bootcmd:
  - |
    cat > /etc/NetworkManager/system-connections/eth0.nmconnection <<'NM'
    [connection]
    id=eth0
    type=ethernet
    interface-name=eth0
    [ipv4]
    method=manual
    address1=$ip/24,$gw
    dns=$dns;
    route1=$erp,$transit
    [ipv6]
    method=disabled
    NM
  - chmod 600 /etc/NetworkManager/system-connections/eth0.nmconnection
  - nmcli connection reload
  - nmcli connection up eth0 || true
write_files:
  - path: /etc/sysctl.d/90-edge.conf
    content: |
      net.ipv4.ip_forward = 1
      net.ipv4.conf.all.send_redirects = 0
      net.ipv4.conf.default.send_redirects = 0
      net.ipv4.conf.eth0.send_redirects = 0
  - path: /etc/nftables/edge.nft
    content: |
      table ip edge {
        chain postrouting {
          type nat hook postrouting priority srcnat; policy accept;
          ip saddr $erp ip daddr != $erp oifname "eth0" masquerade
        }
      }
runcmd:
  - sysctl --system
  - command -v nft || dnf -y install nftables
  - echo 'include "/etc/nftables/edge.nft"' >> /etc/sysconfig/nftables.conf
  - systemctl enable --now nftables
EOF
}

case "${1:-}" in
  create)
    vm=$(vm_extid "$name")
    if [ -z "$vm" ]; then
      s=$(subnet_extid "${EDGE_SUBNET:-MGMT}"); [ -n "$s" ] || { echo "no subnet ${EDGE_SUBNET:-MGMT}" >&2; exit 1; }
      img=$(image_extid "${IMAGE:-rocky-9-genericcloud}"); [ -n "$img" ] || { echo "no image" >&2; exit 1; }
      cl="${CLUSTER:-$(lab/pc.sh GET /api/clustermgmt/v4.0/config/clusters 2>/dev/null | jq -r '.data[] | select(.config.clusterFunction|index("AOS")) | .extId' | head -1)}"
      user_data | base64 | tr -d '\n' > "$tmp/ud"
      jq -n --arg n "$name" --arg c "$cl" --arg i "$img" --arg s "$s" --arg ip "$ip" --rawfile ud "$tmp/ud" '{
        name:$n, numSockets:1, numCoresPerSocket:1, memorySizeBytes:1073741824,
        cluster:{extId:$c},
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
      vm=$(vm_extid "$name")
    fi
    t=$(vm_action "$vm" power-on); [ -z "$t" ] || [ "$t" = null ] || echo "power on: $(wait_task "$t" 120)"
    echo "VM $name ($vm) at $ip: routes $erp via $transit, NATs it to $ip, gateway $gw" ;;
  show)
    vm=$(vm_extid "$name")
    if [ -z "$vm" ]; then echo "no VM $name"; else
      lab/pc.sh GET "$VMM/vms/$vm" 2>/dev/null | jq -r '.data | "\(.name) \(.powerState)"'
    fi ;;
  delete)
    vm=$(vm_extid "$name"); [ -n "$vm" ] || { echo "no VM $name"; exit 0; }
    t=$(vm_action "$vm" power-off); [ -z "$t" ] || [ "$t" = null ] || echo "power off: $(wait_task "$t" 120)"
    t=$(IF_MATCH="$(etag_of "$VMM/vms/$vm")" lab/pc.sh DELETE "$VMM/vms/$vm" 2>/dev/null | jq -r '.data.extId // empty')
    if [ -n "$t" ]; then echo "delete VM: $(wait_task "$t" 300)"; else echo "delete VM: refused"; fi ;;
  *) echo "usage: $0 create|show|delete" >&2; exit 2 ;;
esac
