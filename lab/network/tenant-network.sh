#!/usr/bin/env bash
# Build, show or remove the ready network of one tenant on one site, the
# way Portico would (write-up 34): a VPC in the tenant's project attached to
# the site's NAT external subnet, an internal overlay subnet with the VPC's
# DHCP, a default route to the external subnet, and one floating IP.
# Lab only.
#   source lab/env.sh && lab/network/tenant-network.sh create|show|delete PROJECT CIDR
# PROJECT must be a portico-* project (lab/projects/create.sh). CIDR is the
# tenant's /24, e.g. 10.100.1.0/24. Optional: EXT_SUBNET (external-nat),
# DNS (1.1.1.1). Uses lab/pc.sh: admin from lab/env.sh, or PC_API_KEY for
# the service account's view.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}"
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh

action="${1:?usage: tenant-network.sh create|show|delete PROJECT CIDR}"
project="${2:?usage: tenant-network.sh create|show|delete PROJECT CIDR}"
cidr="${3:?usage: tenant-network.sh create|show|delete PROJECT CIDR}"
[[ "$project" == portico-* ]] || { echo "refusing: project name must start with portico-: $project" >&2; exit 2; }
ext_name="${EXT_SUBNET:-external-nat}"
dns="${DNS:-1.1.1.1}"
net="${cidr%/*}"; prefix="${cidr#*/}"; base="${net%.*}"
NET=/api/networking/v4.4/config
vpc_name="$project-vpc"; subnet_name="$project-net"; fip_name="$project-fip"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

by_name() { lab/pc.sh GET "$NET/$1" 2>/dev/null | jq -r --arg n "$2" '.data[]? | select(.name == $n) | .extId'; }

# create PATH NAME JSON_FILE -> the entity's extId once its task succeeded
create() {
  local code task
  code=$(post "$NET/$1" "$3" "$tmp/resp") || { echo "create $2: $code $(jq -c '.data.error // .' "$tmp/resp")" >&2; return 1; }
  task=$(jq -r '.data.extId' "$tmp/resp")
  if ! st=$(wait_task "$task"); then echo "create $2: $st" >&2; return 1; fi
  echo "create $2: $st" >&2
  by_name "$1" "$2"
}

# share_ext ACTION(share|unshare) EXT_EXTID PROJECT_EXTID: Prism Central refuses
# a VPC whose external subnet is not shared with the VPC's project
# (NETWORKING-10056), so the subnet is shared with the tenant's project first.
share_ext() {
  local etag task
  jq -n --arg p "$3" '{projectExtId: $p}' >"$tmp/share"
  etag=$(etag_of "$NET/subnets/$2")
  task=$(IF_MATCH="$etag" lab/pc.sh POST "$NET/subnets/$2/\$actions/$1" "$tmp/share" 2>"$tmp/err" | jq -r '.data.extId // empty')
  if [ -n "$task" ]; then echo "$1 external subnet: $(wait_task "$task")" >&2; else echo "$1 external subnet: refused, $(tail -1 "$tmp/err")" >&2; fi
}

# default_route VPC_EXTID EXT_EXTID PROJECT_EXTID: a VPC created through the
# API has only its local routes; without 0.0.0.0/0 to the external subnet
# nothing leaves it, neither SNAT nor floating IP replies (measured).
default_route() {
  local rt task
  rt=$(lab/pc.sh GET "$NET/route-tables" 2>/dev/null | jq -r --arg v "$1" '.data[]? | select(.vpcReference == $v) | .extId' | head -1)
  [ -n "$rt" ] || { echo "default route: no route table for the VPC" >&2; return 1; }
  if lab/pc.sh GET "$NET/route-tables/$rt/routes" 2>/dev/null | jq -e '.data[]? | select(.destination.ipv4.prefixLength == 0)' >/dev/null; then return 0; fi
  jq -n --arg v "$1" --arg e "$2" --arg p "$3" --arg rt "$rt" '{
    name: "default", routeType: "STATIC", vpcReference: $v, routeTableReference: $rt, projectExtId: $p,
    destination: {ipv4: {ip: {value: "0.0.0.0"}, prefixLength: 0}},
    nexthop: {nexthopType: "EXTERNAL_SUBNET", nexthopReference: $e}}' >"$tmp/route"
  task=$(lab/pc.sh POST "$NET/route-tables/$rt/routes" "$tmp/route" 2>"$tmp/err" | jq -r '.data.extId // empty')
  if [ -n "$task" ]; then echo "default route: $(wait_task "$task")" >&2; else echo "default route: refused, $(tail -1 "$tmp/err")" >&2; return 1; fi
}

# remove PATH EXTID LABEL
remove() {
  local etag task
  etag=$(etag_of "$NET/$1/$2")
  task=$(IF_MATCH="$etag" lab/pc.sh DELETE "$NET/$1/$2" 2>/dev/null | jq -r '.data.extId // empty')
  if [ -n "$task" ]; then echo "delete $3: $(wait_task "$task")"; else echo "delete $3: refused"; fi
}

show() {
  local v s f
  v=$(by_name vpcs "$vpc_name"); s=$(by_name subnets "$subnet_name"); f=$(by_name floating-ips "$fip_name")
  [ -n "$v" ] && lab/pc.sh GET "$NET/vpcs/$v" 2>/dev/null | jq -r '.data |
    "vpc \(.name) project=\(.projectExtId // "-") snat=\([.snatIps[]?.ipv4.value] | join(",")) external=\([.externalSubnets[]? | "\(.subnetReference) ips=\([.externalIps[]?.ipv4.value] | join(","))"] | join(";")) dns=\([.commonDhcpOptions.domainNameServers[]?.ipv4.value] | join(","))"'
  [ -n "$s" ] && lab/pc.sh GET "$NET/subnets/$s" 2>/dev/null | jq -r '.data |
    "subnet \(.name) type=\(.subnetType) project=\(.projectExtId // "-") cidr=\(.ipConfig[0].ipv4.ipSubnet.ip.value)/\(.ipConfig[0].ipv4.ipSubnet.prefixLength) gateway=\(.ipConfig[0].ipv4.defaultGatewayIp.value // "-")"'
  [ -n "$f" ] && lab/pc.sh GET "$NET/floating-ips/$f" 2>/dev/null | jq -r '.data |
    "floating-ip \(.name) ip=\(.floatingIp.ipv4.value // .floatingIpValue // "-") project=\(.projectExtId // "-") status=\(.associationStatus // "-")"'
  ext=$(by_name subnets "$ext_name")
  [ -n "$ext" ] && lab/pc.sh GET "$NET/subnets/$ext" 2>/dev/null | jq -r '.data | "external \(.name) sharedWithProjects=\(.sharedWithProjects // [] | join(","))"'
  return 0
}

case "$action" in
  create)
    pj=$(project_extid "$project"); [ -n "$pj" ] || { echo "no project $project (lab/projects/create.sh)" >&2; exit 1; }
    ext=$(by_name subnets "$ext_name"); [ -n "$ext" ] || { echo "no external subnet $ext_name" >&2; exit 1; }
    shared=$(lab/pc.sh GET "$NET/subnets/$ext" 2>/dev/null | jq -r --arg p "$pj" '.data.sharedWithProjects // [] | index($p) // empty')
    [ -n "$shared" ] || share_ext share "$ext" "$pj"
    vpc=$(by_name vpcs "$vpc_name")
    if [ -z "$vpc" ]; then
      jq -n --arg n "$vpc_name" --arg p "$pj" --arg e "$ext" --arg d "$dns" '{
        name: $n, vpcType: "REGULAR", projectExtId: $p,
        externalSubnets: [{subnetReference: $e}],
        commonDhcpOptions: {domainNameServers: [{ipv4: {value: $d}}]}}' >"$tmp/vpc"
      vpc=$(create vpcs "$vpc_name" "$tmp/vpc") || exit 1
    fi
    default_route "$vpc" "$ext" "$pj" || exit 1
    if [ -z "$(by_name subnets "$subnet_name")" ]; then
      jq -n --arg n "$subnet_name" --arg p "$pj" --arg v "$vpc" --arg net "$net" \
        --argjson pre "$prefix" --arg gw "$base.1" --arg s "$base.10" --arg e "$base.250" '{
        name: $n, subnetType: "OVERLAY", projectExtId: $p, vpcReference: $v,
        ipConfig: [{ipv4: {ipSubnet: {ip: {value: $net}, prefixLength: $pre},
          defaultGatewayIp: {value: $gw},
          poolList: [{startIp: {value: $s}, endIp: {value: $e}}]}}]}' >"$tmp/subnet"
      create subnets "$subnet_name" "$tmp/subnet" >/dev/null || exit 1
    fi
    if [ -z "$(by_name floating-ips "$fip_name")" ]; then
      jq -n --arg n "$fip_name" --arg p "$pj" --arg e "$ext" \
        '{name: $n, projectExtId: $p, externalSubnetReference: $e}' >"$tmp/fip"
      create floating-ips "$fip_name" "$tmp/fip" >/dev/null || exit 1
    fi
    show ;;
  show) show
    v=$(by_name vpcs "$vpc_name")
    [ -n "$v" ] && rt=$(lab/pc.sh GET "$NET/route-tables" 2>/dev/null | jq -r --arg v "$v" '.data[]? | select(.vpcReference == $v) | .extId' | head -1) \
      && lab/pc.sh GET "$NET/route-tables/$rt/routes" 2>/dev/null | jq -r '.data[]? | "route \(.destination.ipv4.ip.value)/\(.destination.ipv4.prefixLength) \(.routeType) -> \(.nexthop.nexthopType)"'
    ;;
  delete)
    f=$(by_name floating-ips "$fip_name"); [ -n "$f" ] && remove floating-ips "$f" "$fip_name"
    s=$(by_name subnets "$subnet_name"); [ -n "$s" ] && remove subnets "$s" "$subnet_name"
    v=$(by_name vpcs "$vpc_name"); [ -n "$v" ] && remove vpcs "$v" "$vpc_name"
    pj=$(project_extid "$project"); ext=$(by_name subnets "$ext_name")
    [ -n "$pj" ] && [ -n "$ext" ] && share_ext unshare "$ext" "$pj"
    show ;;
  *) echo "usage: tenant-network.sh create|show|delete PROJECT CIDR" >&2; exit 2 ;;
esac
