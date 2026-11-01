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
  *) echo "usage: lab/network/bgp-gateway.sh create|session|show|delete" >&2; exit 2 ;;
esac
