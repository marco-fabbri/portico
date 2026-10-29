#!/usr/bin/env bash
# Create the NAT external subnet that tenant VPCs use on one site (write-up
# 34). Lab only. The subnet sits on the same cluster and virtual switch as
# the site's existing VLAN subnet (MGMT), with an IP pool for SNAT and
# floating IPs. Idempotent: an existing subnet with the same name is shown,
# not changed.
#   source lab/env.sh && lab/network/external-subnet.sh NAME POOL_START POOL_END
# Other site: PC_URL=https://10.23.210.185:9440 and PC_PASS from nyc1-pw.
# Optional: EXT_VLAN (0: untagged on the host), EXT_NET (10.23.210.0),
# EXT_PREFIX (24), EXT_GATEWAY (10.23.210.254), EXT_BASE (MGMT), EXT_NAT (true;
# false makes the routed, no-NAT external subnet of NVD-2219's transit VPC).
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}"
# shellcheck source=lab/lib.sh
source lab/lib.sh

name="${1:?usage: external-subnet.sh NAME POOL_START POOL_END}"
start="${2:?usage: external-subnet.sh NAME POOL_START POOL_END}"
end="${3:?usage: external-subnet.sh NAME POOL_START POOL_END}"
vlan="${EXT_VLAN:-0}"
net="${EXT_NET:-10.23.210.0}"
prefix="${EXT_PREFIX:-24}"
gateway="${EXT_GATEWAY:-10.23.210.254}"
base="${EXT_BASE:-MGMT}"
api=/api/networking/v4.4/config/subnets

tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
lab/pc.sh GET "$api" >"$tmp/list" 2>/dev/null
existing=$(jq -r --arg n "$name" '.data[]? | select(.name == $n) | .extId' "$tmp/list")
if [ -n "$existing" ]; then
  echo "subnet $name exists: $existing"
else
  cluster=$(jq -r --arg n "$base" '.data[]? | select(.name == $n) | .clusterReference' "$tmp/list")
  vswitch=$(jq -r --arg n "$base" '.data[]? | select(.name == $n) | .virtualSwitchReference' "$tmp/list")
  if [ -z "$cluster" ] || [ -z "$vswitch" ]; then
    echo "no subnet $base to take the cluster and virtual switch from" >&2; exit 1
  fi
  jq -n --arg name "$name" --argjson vlan "$vlan" --arg cluster "$cluster" \
    --arg vswitch "$vswitch" --arg net "$net" --argjson prefix "$prefix" \
    --arg gw "$gateway" --arg start "$start" --arg end "$end" --arg nat "${EXT_NAT:-true}" '{
      name: $name, subnetType: "VLAN", networkId: $vlan,
      clusterReference: $cluster, virtualSwitchReference: $vswitch,
      isExternal: true, isNatEnabled: ($nat == "true"),
      ipConfig: [{ipv4: {
        ipSubnet: {ip: {value: $net}, prefixLength: $prefix},
        defaultGatewayIp: {value: $gw},
        poolList: [{startIp: {value: $start}, endIp: {value: $end}}]}}]
    }' >"$tmp/body"
  lab/pc.sh POST "$api" "$tmp/body" >"$tmp/resp" 2>"$tmp/err" || true
  task=$(jq -r '.data.extId // empty' "$tmp/resp" 2>/dev/null || true)
  if [ -z "$task" ]; then
    echo "create refused: $(tail -1 "$tmp/err")" >&2
    jq -c '.data.error // .' "$tmp/resp" >&2 || true
    exit 1
  fi
  echo "create task $task: $(wait_task "$task")"
fi
lab/pc.sh GET "$api" 2>/dev/null | jq -r --arg n "$name" '.data[]? | select(.name == $n) |
  "\(.name) \(.extId) vlan=\(.networkId) external=\(.isExternal) nat=\(.isNatEnabled) advanced=\(.isAdvancedNetworking)
  cidr=\(.ipConfig[0].ipv4.ipSubnet.ip.value)/\(.ipConfig[0].ipv4.ipSubnet.prefixLength) gateway=\(.ipConfig[0].ipv4.defaultGatewayIp.value)
  pool=\(.ipConfig[0].ipv4.poolList[0].startIp.value)-\(.ipConfig[0].ipv4.poolList[0].endIp.value)"'
