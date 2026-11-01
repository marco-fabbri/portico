#!/usr/bin/env bash
# The BGP gateway of the transit VPC (write-up 37): a Nutanix Network
# Gateway VM on the VLAN with the BGP service for the transit VPC, a remote
# gateway standing for the site's router (the lab's portico-edge), and the
# eBGP session between them that advertises the routed prefix with the
# transit VPC's external address as next hop. The router then needs no
# static route to the transit VPC, which is Nutanix's documented path for
# externally routable prefixes (Flow Virtual Networking Guide 7.6, "BGP
# gateways"). On the router side the peer is FRR (edge-router.sh writes it).
#   source lab/env.sh && lab/network/bgp-gateway.sh create|session|show|delete
# Measurement verbs (write-up 38): update-session LOCAL_EXTID REMOTE_EXTID,
# update-remote ADDR [ASN], subnet-gateway SUBNET_NAME ADDR, image-url NAME
# URL, edge-on-external NAME ADDR/PREFIX GW (a throwaway Rocky VM with its
# NIC on the external subnet, EXT_SUBNET, default external-routed),
# delete-vm NAME, delete-image NAME. PC_API_KEY (lab/pc.sh) runs them as
# the service account.
# Optional: LOCAL_NAME (portico-transit-bgp), LOCAL_IP (10.23.210.195),
# LOCAL_GW (10.23.210.254), LOCAL_ASN (65010), LOCAL_SUBNET (MGMT),
# VPC_NAME (portico-transit), REMOTE_NAME (portico-edge), REMOTE_IP
# (10.23.210.194), REMOTE_ASN (65020), SESSION_NAME (portico-transit-to-edge),
# CLUSTER (the first AOS cluster), PROJECT (the owning project's extId,
# default the system project `_internal`, 00000000-0000-0000-0000-000000000000:
# without it the API accepts the create but the workflow cannot read back
# the gateway image it just registered and fails, write-up 37).
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}"
# shellcheck source=lab/vm/lib.sh
source lab/vm/lib.sh
NET=/api/networking/v4.4/config
local_name="${LOCAL_NAME:-portico-transit-bgp}"; local_ip="${LOCAL_IP:-10.23.210.195}"
local_gw="${LOCAL_GW:-10.23.210.254}"; local_asn="${LOCAL_ASN:-65010}"
remote_name="${REMOTE_NAME:-portico-edge}"; remote_ip="${REMOTE_IP:-10.23.210.194}"; remote_asn="${REMOTE_ASN:-65020}"
session_name="${SESSION_NAME:-portico-transit-to-edge}"
project="${PROJECT:-00000000-0000-0000-0000-000000000000}"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

gateway_extid() { extid_by_name "$NET/gateways" "$1"; }
session_extid() { extid_by_name "$NET/bgp-sessions" "$1"; }
vpc_extid() { extid_by_name "$NET/vpcs" "$1"; }
subnet_extid() { extid_by_name "$NET/subnets" "$1"; }

# post PATH JSON -> waits for the task; prints its status
post() {
  local t
  t=$(lab/pc.sh POST "$1" "$2" 2>"$tmp/err" | jq -r '.data.extId // empty')
  [ -n "$t" ] || { echo "refused: $(tail -1 "$tmp/err")" >&2; return 1; }
  wait_task "$t" "${3:-1800}"
}

case "${1:-}" in
  create)
    if [ -z "$(gateway_extid "$remote_name")" ]; then
      jq -n --arg n "$remote_name" --arg ip "$remote_ip" --argjson asn "$remote_asn" '{
        name:$n, description:"Portico lab: the router in front of the transit VPC (write-up 37)",
        services:{"$objectType":"networking.v4.config.RemoteNetworkServices",
          remoteBgpService:{address:{ipv4:{value:$ip}}, asn:$asn}}}' > "$tmp/remote.json"
      echo "remote gateway $remote_name: $(post "$NET/gateways" "$tmp/remote.json" 300)"
    else echo "remote gateway $remote_name exists"; fi
    if [ -z "$(gateway_extid "$local_name")" ]; then
      vpc=$(vpc_extid "${VPC_NAME:-portico-transit}"); [ -n "$vpc" ] || { echo "no VPC ${VPC_NAME:-portico-transit}" >&2; exit 1; }
      s=$(subnet_extid "${LOCAL_SUBNET:-MGMT}"); [ -n "$s" ] || { echo "no subnet ${LOCAL_SUBNET:-MGMT}" >&2; exit 1; }
      cl="${CLUSTER:-$(lab/pc.sh GET /api/clustermgmt/v4.0/config/clusters 2>/dev/null | jq -r '.data[] | select(.config.clusterFunction|index("AOS")) | .extId' | head -1)}"
      # A VLAN-attached gateway: one interface on the VLAN subnet, the BGP
      # service for the VPC, the interface address as the service address.
      # The gateway VM's image is downloaded by Prism Central on the first
      # create and kept in the image service under the gateway's name (a
      # dark site uploads the bundle through LCM first).
      jq -n --arg n "$local_name" --arg c "$cl" --arg s "$s" --arg ip "$local_ip" --arg gw "$local_gw" --arg vpc "$vpc" --arg p "$project" --argjson asn "$local_asn" '{
        name:$n, description:"Portico lab: BGP gateway of the transit VPC (write-up 37)",
        projectExtId:$p,
        deployment:{clusterReference:$c,
          interfaces:[{subnetReference:$s, ipAddress:{ipv4:{value:$ip, prefixLength:24}}, defaultGatewayAddress:{ipv4:{value:$gw}}}],
          shouldSynchronizeSystemDnsServers:true, shouldSynchronizeSystemNtpServers:true},
        services:{"$objectType":"networking.v4.config.LocalNetworkServices",
          localBgpService:{asn:$asn, vpcReference:$vpc},
          serviceAddresses:[{ipv4:{value:$ip}}]}} | if $p == "" then del(.projectExtId) else . end' > "$tmp/local.json"
      echo "local gateway $local_name: $(post "$NET/gateways" "$tmp/local.json" 1800)"
    else echo "local gateway $local_name exists"; fi ;;
  session)
    l=$(gateway_extid "$local_name"); r=$(gateway_extid "$remote_name")
    if [ -z "$l" ] || [ -z "$r" ]; then echo "create the gateways first" >&2; exit 1; fi
    # The session create is refused while the gateway VM still boots
    # ("Unable to determine version of gateway"): wait for its status UP.
    for _ in $(seq 1 30); do
      st=$(lab/pc.sh GET "$NET/gateways/$l" 2>/dev/null | jq -r '.data.status.state // "-"')
      [ "$st" = UP ] && break; echo "gateway $local_name: $st, waiting"; sleep 20
    done
    if [ -z "$(session_extid "$session_name")" ]; then
      jq -n --arg n "$session_name" --arg l "$l" --arg r "$r" --arg ip "$local_ip" '{
        name:$n, description:"Portico lab: the routed prefix of the transit VPC, advertised to the router (write-up 37)",
        localGatewayReference:$l, remoteGatewayReference:$r,
        localGatewayInterfaceIpAddress:{ipv4:{value:$ip}},
        shouldAdvertiseAllExternallyRoutablePrefixes:true}' > "$tmp/session.json"
      echo "session $session_name: $(post "$NET/bgp-sessions" "$tmp/session.json" 300)"
    else echo "session $session_name exists"; fi ;;
  show)
    echo "gateways:"; lab/pc.sh GET "$NET/gateways" 2>/dev/null | jq -r '.data[]? | "  \(.name) \(.extId) status=\(.status.state // "-")\(if .status.message then " (" + .status.message + ")" else "" end) vm=\(.vmReference // "-") version=\(.installedSoftwareVersion // "-")"'
    echo "sessions:"; lab/pc.sh GET "$NET/bgp-sessions" 2>/dev/null | jq -r '.data[]? | "  \(.name) \(.extId) status=\(.status.state // "-") advertise-all=\(.shouldAdvertiseAllExternallyRoutablePrefixes // "-")"'
    for sid in $(lab/pc.sh GET "$NET/bgp-sessions" 2>/dev/null | jq -r '.data[]?.extId'); do
      echo "routes of session $sid:"
      lab/pc.sh GET "$NET/bgp-sessions/$sid/bgp-routes" 2>/dev/null | jq -r '.data[]? | "  \(.destination.ipv4.ip.value)/\(.destination.ipv4.prefixLength) via \(.nexthop.nexthopIpAddress.ipv4.value // "-") \(.routeType // "-")"'
    done ;;
  delete)
    s=$(session_extid "$session_name")
    if [ -n "$s" ]; then
      t=$(IF_MATCH="$(etag_of "$NET/bgp-sessions/$s")" lab/pc.sh DELETE "$NET/bgp-sessions/$s" 2>/dev/null | jq -r '.data.extId // empty')
      if [ -n "$t" ]; then echo "delete session: $(wait_task "$t" 300)"; else echo "delete session: refused"; fi
    fi
    for g in "$local_name" "$remote_name"; do
      id=$(gateway_extid "$g"); [ -n "$id" ] || continue
      t=$(IF_MATCH="$(etag_of "$NET/gateways/$id")" lab/pc.sh DELETE "$NET/gateways/$id" 2>/dev/null | jq -r '.data.extId // empty')
      if [ -n "$t" ]; then echo "delete gateway $g: $(wait_task "$t" 900)"; else echo "delete gateway $g: refused"; fi
    done ;;
  update-session)
    sid=$(session_extid "$session_name"); [ -n "$sid" ] || { echo "no session $session_name" >&2; exit 1; }
    lab/pc.sh GET "$NET/bgp-sessions/$sid" 2>/dev/null | jq --arg l "${2:?LOCAL_EXTID}" --arg r "${3:?REMOTE_EXTID}" '.data | .localGatewayReference=$l | .remoteGatewayReference=$r | del(.localGateway, .remoteGateway, .status, .links, .["$reserved"], .["$objectType"], .tenantId)' > "$tmp/s.json"
    t=$(IF_MATCH="$(etag_of "$NET/bgp-sessions/$sid")" lab/pc.sh PUT "$NET/bgp-sessions/$sid" "$tmp/s.json" 2>"$tmp/err" | jq -r '.data.extId // empty')
    if [ -n "$t" ]; then echo "update session: $(wait_task "$t" 300)"; else echo "update session refused: $(tail -1 "$tmp/err")"; fi ;;
  update-remote)
    id=$(gateway_extid "$remote_name"); [ -n "$id" ] || { echo "no gateway $remote_name" >&2; exit 1; }
    lab/pc.sh GET "$NET/gateways/$id" 2>/dev/null | jq --arg ip "${2:?ADDR}" --argjson asn "${3:-$remote_asn}" '.data | .services.remoteBgpService.address.ipv4.value=$ip | .services.remoteBgpService.asn=$asn | del(.status, .links, .["$reserved"], .["$objectType"], .tenantId, .vmReference, .installedSoftwareVersion)' > "$tmp/g.json"
    t=$(IF_MATCH="$(etag_of "$NET/gateways/$id")" lab/pc.sh PUT "$NET/gateways/$id" "$tmp/g.json" 2>"$tmp/err" | jq -r '.data.extId // empty')
    if [ -n "$t" ]; then echo "update remote gateway: $(wait_task "$t" 300)"; else echo "update remote gateway refused: $(tail -1 "$tmp/err")"; fi ;;
  subnet-gateway)
    sid=$(subnet_extid "${2:?SUBNET_NAME}"); [ -n "$sid" ] || { echo "no subnet $2" >&2; exit 1; }
    lab/pc.sh GET "$NET/subnets/$sid" 2>/dev/null | jq --arg gw "${3:?ADDR}" '.data | .ipConfig[0].ipv4.defaultGatewayIp.value=$gw | del(.links, .["$reserved"], .["$objectType"], .tenantId, .clusterName, .hypervisorType, .virtualSwitch, .vpc, .ipUsage, .migrationState, .bridgeName, .reservedIpAddresses, .dynamicIpAddresses)' > "$tmp/sub.json"
    t=$(IF_MATCH="$(etag_of "$NET/subnets/$sid")" lab/pc.sh PUT "$NET/subnets/$sid" "$tmp/sub.json" 2>"$tmp/err" | jq -r '.data.extId // empty')
    if [ -n "$t" ]; then echo "update subnet gateway: $(wait_task "$t" 300)"; else echo "update subnet gateway refused: $(tail -1 "$tmp/err")"; fi ;;
  image-url)
    jq -n --arg n "${2:?NAME}" --arg u "${3:?URL}" '{name:$n, type:"DISK_IMAGE", description:"Portico lab: image import by URL (write-up 38)", source:{"$objectType":"vmm.v4.content.UrlSource", url:$u}}' > "$tmp/img.json"
    t=$(lab/pc.sh POST /api/vmm/v4.0/content/images "$tmp/img.json" 2>"$tmp/err" | jq -r '.data.extId // empty')
    if [ -n "$t" ]; then start=$(date +%s); echo "image from URL: $(wait_task "$t" 1800) in $(( $(date +%s) - start )) s"; else echo "image from URL refused: $(tail -1 "$tmp/err")"; fi ;;
  edge-on-external)
    name="${2:?NAME}"; addr="${3:?ADDR/PREFIX}"; gw="${4:?GW}"
    s=$(subnet_extid "${EXT_SUBNET:-external-routed}"); [ -n "$s" ] || { echo "no subnet ${EXT_SUBNET:-external-routed}" >&2; exit 1; }
    img=$(image_extid "${IMAGE:-rocky-9-genericcloud}"); [ -n "$img" ] || { echo "no image" >&2; exit 1; }
    cl="${CLUSTER:-$(lab/pc.sh GET /api/clustermgmt/v4.0/config/clusters 2>/dev/null | jq -r '.data[] | select(.config.clusterFunction|index("AOS")) | .extId' | head -1)}"
    printf '#cloud-config\nhostname: %s\nssh_pwauth: false\nusers: []\nbootcmd:\n  - |\n    cat > /etc/NetworkManager/system-connections/eth0.nmconnection <<NM\n    [connection]\n    id=eth0\n    type=ethernet\n    interface-name=eth0\n    [ipv4]\n    method=manual\n    address1=%s,%s\n    [ipv6]\n    method=disabled\n    NM\n  - chmod 600 /etc/NetworkManager/system-connections/eth0.nmconnection\n  - nmcli connection reload\n  - nmcli connection up eth0 || true\n' "$name" "$addr" "$gw" | base64 | tr -d '\n' > "$tmp/ud"
    jq -n --arg n "$name" --arg c "$cl" --arg i "$img" --arg s "$s" --arg ip "${addr%/*}" --rawfile ud "$tmp/ud" '{
      name:$n, numSockets:1, numCoresPerSocket:1, memorySizeBytes:1073741824, cluster:{extId:$c},
      bootConfig:{"$objectType":"vmm.v4.ahv.config.LegacyBoot"},
      disks:[{diskAddress:{busType:"SCSI", index:0}, backingInfo:{"$objectType":"vmm.v4.ahv.config.VmDisk", diskSizeBytes:10737418240, dataSource:{reference:{"$objectType":"vmm.v4.ahv.config.ImageReference", imageExtId:$i}}}}],
      nics:[{backingInfo:{"$objectType":"vmm.v4.ahv.config.EmulatedNic", model:"VIRTIO"}, networkInfo:{nicType:"NORMAL_NIC", subnet:{extId:$s}, ipv4Config:{shouldAssignIp:true, ipAddress:{value:$ip, prefixLength:32}}}}],
      guestCustomization:{config:{"$objectType":"vmm.v4.ahv.config.CloudInit", datasourceType:"CONFIG_DRIVE_V2", cloudInitScript:{"$objectType":"vmm.v4.ahv.config.Userdata", value:$ud}}}}' > "$tmp/vm.json"
    t=$(lab/pc.sh POST /api/vmm/v4.0/ahv/config/vms "$tmp/vm.json" 2>"$tmp/err" | jq -r '.data.extId // empty')
    if [ -z "$t" ]; then echo "VM create refused: $(tail -1 "$tmp/err")"; exit 1; fi
    echo "VM create: $(wait_task "$t" 300)"; vm=$(vm_extid "$name"); echo "power on: $(wait_task "$(vm_action "$vm" power-on)" 120)"; echo "vm $name $vm on $addr" ;;
  delete-vm)
    vm=$(vm_extid "${2:?NAME}"); [ -n "$vm" ] || { echo "no vm $2"; exit 0; }
    t=$(vm_action "$vm" power-off); [ -n "$t" ] && wait_task "$t" 120 >/dev/null
    t=$(IF_MATCH="$(etag_of "/api/vmm/v4.0/ahv/config/vms/$vm")" lab/pc.sh DELETE "/api/vmm/v4.0/ahv/config/vms/$vm" 2>/dev/null | jq -r '.data.extId // empty'); echo "delete vm $2: $(wait_task "$t" 300)" ;;
  delete-image)
    id=$(image_extid "${2:?NAME}"); [ -n "$id" ] || { echo "no image $2"; exit 0; }
    t=$(IF_MATCH="$(etag_of "/api/vmm/v4.0/content/images/$id")" lab/pc.sh DELETE "/api/vmm/v4.0/content/images/$id" 2>/dev/null | jq -r '.data.extId // empty'); echo "delete image $2: $(wait_task "$t" 300)" ;;
  *) echo "usage: lab/network/bgp-gateway.sh create|session|show|delete|update-session|update-remote|subnet-gateway|image-url|edge-on-external|delete-vm|delete-image" >&2; exit 2 ;;
esac
