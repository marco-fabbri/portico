#!/usr/bin/env bash
# The MSP side of NVD-2219's shared transit model (tenant networking design,
# section 6 item 2): a transit VPC on the site's NAT VLAN external subnet,
# with its default route, and a NAT overlay external subnet in it that
# tenant VPCs attach to instead of the VLAN one. Lab only.
#   source lab/env.sh && lab/network/transit.sh create|show|delete
#   lab/network/transit.sh publish|unpublish PRIVATE_IP
# publish: a floating IP of the VLAN external subnet on the transit VPC,
# associated with PRIVATE_IP of the overlay external subnet (a tenant's
# floating IP there), so the tenant's VM is reached from the VLAN.
# Optional: EXT_SUBNET (external-nat), OEN_CIDR (100.64.0.0/24), OEN_NAME
# (portico-transit-oen): one overlay external subnet per tenant is made by
# running create with another name and CIDR; delete removes OEN_NAME, and
# the transit VPC once no overlay external subnet is left in it.
# NVD-2219's routed form: EXT_SUBNET a no-NAT VLAN external subnet,
# TRANSIT_IP the transit VPC's fixed address on it (the next hop of the
# upstream router's route; Prism Central wants as many fixed addresses as
# active gateways, so one gateway), ERP the routed prefix the overlay external
# subnets are cut from (e.g. 10.23.211.0/24, one /28 per tenant); a
# tenant's floating IP is then reachable without publish.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}"
# shellcheck source=lab/lib.sh
source lab/lib.sh
NET=/api/networking/v4.4/config
ext_name="${EXT_SUBNET:-external-nat}"
cidr="${OEN_CIDR:-100.64.0.0/24}"
net="${cidr%/*}"; prefix="${cidr#*/}"
# gateway: first address of the CIDR; pool: second to the one before broadcast
ip2n() { local a b c d; IFS=. read -r a b c d <<<"$1"; echo $(( (a << 24) + (b << 16) + (c << 8) + d )); }
n2ip() { echo "$(( $1 >> 24 & 255 )).$(( $1 >> 16 & 255 )).$(( $1 >> 8 & 255 )).$(( $1 & 255 ))"; }
first=$(ip2n "$net"); last=$(( first + (1 << (32 - prefix)) - 1 ))
gw=$(n2ip $(( first + 1 ))); pool_s=$(n2ip $(( first + 2 ))); pool_e=$(n2ip $(( last - 1 )))
vpc_name="portico-transit"; oen_name="${OEN_NAME:-portico-transit-oen}"; pub_name="portico-transit-pub"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

by_name() { lab/pc.sh GET "$NET/$1" 2>/dev/null | jq -r --arg n "$2" '.data[]? | select(.name == $n) | .extId'; }

# create PATH NAME JSON_FILE -> the entity's extId once its task succeeded
create() {
  local task
  task=$(lab/pc.sh POST "$NET/$1" "$3" 2>"$tmp/err" | jq -r '.data.extId // empty')
  [ -n "$task" ] || { echo "create $2: refused, $(tail -1 "$tmp/err")" >&2; return 1; }
  echo "create $2: $(wait_task "$task")" >&2
  by_name "$1" "$2"
}

remove() {
  local task
  task=$(IF_MATCH="$(etag_of "$NET/$1/$2")" lab/pc.sh DELETE "$NET/$1/$2" 2>"$tmp/err" | jq -r '.data.extId // empty')
  if [ -n "$task" ]; then echo "delete $3: $(wait_task "$task")"; else echo "delete $3: refused, $(tail -1 "$tmp/err")"; fi
}

route_table() { lab/pc.sh GET "$NET/route-tables" 2>/dev/null | jq -r --arg v "$1" '.data[]? | select(.vpcReference == $v) | .extId' | head -1; }

show() {
  local v o p rt
  v=$(by_name vpcs "$vpc_name"); o=$(by_name subnets "$oen_name"); p=$(by_name floating-ips "$pub_name")
  [ -n "$v" ] && lab/pc.sh GET "$NET/vpcs/$v" 2>/dev/null | jq -r '.data | "vpc \(.name) type=\(.vpcType) erp=\([.externallyRoutablePrefixes[]? | "\(.ipv4.ip.value)/\(.ipv4.prefixLength)"] | join(",")) external=\([.externalSubnets[]? | "\(.subnetReference) ips=\([.externalIps[]?.ipv4.value] | join(","))"] | join(";"))"'
  [ -n "$v" ] && rt=$(route_table "$v") && [ -n "$rt" ] && lab/pc.sh GET "$NET/route-tables/$rt/routes" 2>/dev/null | jq -r '.data[]? | "route \(.destination.ipv4.ip.value)/\(.destination.ipv4.prefixLength) \(.routeType) -> \(.nexthop.nexthopType)"'
  [ -n "$o" ] && lab/pc.sh GET "$NET/subnets/$o" 2>/dev/null | jq -r '.data | "subnet \(.name) type=\(.subnetType) external=\(.isExternal) nat=\(.isNatEnabled) cidr=\(.ipConfig[0].ipv4.ipSubnet.ip.value)/\(.ipConfig[0].ipv4.ipSubnet.prefixLength) shared=\(.sharedWithProjects // [] | join(","))"'
  [ -n "$p" ] && lab/pc.sh GET "$NET/floating-ips/$p" 2>/dev/null | jq -r '.data | "floating-ip \(.name) ip=\(.floatingIp.ipv4.value // "-") status=\(.associationStatus // "-")"'
  return 0
}

case "${1:-}" in
  create)
    ext=$(by_name subnets "$ext_name"); [ -n "$ext" ] || { echo "no external subnet $ext_name" >&2; exit 1; }
    v=$(by_name vpcs "$vpc_name")
    if [ -z "$v" ]; then
      jq -n --arg n "$vpc_name" --arg e "$ext" --arg ip "${TRANSIT_IP:-}" --arg erp "${ERP:-}" '{name:$n, vpcType:"TRANSIT",
        externalSubnets:[{subnetReference:$e} + (if $ip == "" then {} else {externalIps:[{ipv4:{value:$ip}}], activeGatewayCount:1} end)]}
        + (if $erp == "" then {} else {externallyRoutablePrefixes:[{ipv4:{ip:{value:($erp|split("/")[0])}, prefixLength:($erp|split("/")[1]|tonumber)}}]} end)' > "$tmp/vpc"
      v=$(create vpcs "$vpc_name" "$tmp/vpc"); [ -n "$v" ] || exit 1
    fi
    rt=$(route_table "$v")
    if ! lab/pc.sh GET "$NET/route-tables/$rt/routes" 2>/dev/null | jq -e '.data[]? | select(.destination.ipv4.prefixLength == 0)' >/dev/null; then
      jq -n --arg v "$v" --arg e "$ext" --arg rt "$rt" '{name:"default", routeType:"STATIC", vpcReference:$v, routeTableReference:$rt,
        destination:{ipv4:{ip:{value:"0.0.0.0"}, prefixLength:0}}, nexthop:{nexthopType:"EXTERNAL_SUBNET", nexthopReference:$e}}' > "$tmp/route"
      task=$(lab/pc.sh POST "$NET/route-tables/$rt/routes" "$tmp/route" 2>"$tmp/err" | jq -r '.data.extId // empty')
      if [ -n "$task" ]; then echo "default route: $(wait_task "$task")" >&2; else echo "default route: refused, $(tail -1 "$tmp/err")" >&2; exit 1; fi
    fi
    if [ -z "$(by_name subnets "$oen_name")" ]; then
      jq -n --arg n "$oen_name" --arg v "$v" --arg net "$net" --argjson pre "$prefix" --arg gw "$gw" --arg s "$pool_s" --arg e "$pool_e" '{
        name:$n, subnetType:"OVERLAY", vpcReference:$v, isExternal:true, isNatEnabled:true,
        ipConfig:[{ipv4:{ipSubnet:{ip:{value:$net}, prefixLength:$pre}, defaultGatewayIp:{value:$gw},
          poolList:[{startIp:{value:$s}, endIp:{value:$e}}]}}]}' > "$tmp/oen"
      create subnets "$oen_name" "$tmp/oen" >/dev/null || exit 1
    fi
    show ;;
  publish)
    ip="${2:?usage: transit.sh publish PRIVATE_IP}"
    ext=$(by_name subnets "$ext_name"); v=$(by_name vpcs "$vpc_name")
    if [ -z "$(by_name floating-ips "$pub_name")" ]; then
      jq -n --arg n "$pub_name" --arg e "$ext" --arg v "$v" --arg ip "$ip" '{name:$n, externalSubnetReference:$e,
        association:{"$objectType":"networking.v4.config.PrivateIpAssociation", vpcReference:$v, privateIp:{ipv4:{value:$ip}}}}' > "$tmp/pub"
      create floating-ips "$pub_name" "$tmp/pub" >/dev/null || exit 1
    fi
    show ;;
  unpublish)
    p=$(by_name floating-ips "$pub_name"); [ -z "$p" ] || remove floating-ips "$p" "$pub_name" ;;
  show) show ;;
  delete)
    p=$(by_name floating-ips "$pub_name"); [ -z "$p" ] || remove floating-ips "$p" "$pub_name"
    o=$(by_name subnets "$oen_name"); [ -z "$o" ] || remove subnets "$o" "$oen_name"
    v=$(by_name vpcs "$vpc_name")
    if [ -n "$v" ] && [ -z "$(lab/pc.sh GET "$NET/subnets" 2>/dev/null | jq -r --arg v "$v" '.data[]? | select(.vpcReference == $v) | .extId')" ]; then remove vpcs "$v" "$vpc_name"; fi
    show ;;
  *) echo "usage: $0 create|show|delete | publish|unpublish PRIVATE_IP" >&2; exit 2 ;;
esac
