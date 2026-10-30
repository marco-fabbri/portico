#!/usr/bin/env bash
# shellcheck disable=SC2016  # literal $ in jq programs is intended
# The ready network's acceptance on a set-up appliance (write-up 35): a
# tenant connected to a site whose Networking card is on gets its
# network; a probe VM on it is reached on its floating IP and reaches the
# Internet; Disconnect leaves only the MSP's transit objects. Lab only.
#   source lab/env.sh && LAB_PRISM_HOST=portico lab/portico/ready-network.sh [SITE]
# SITE is the appliance's site id (default: the first). The tenant is
# `netcheck`; its VM is created as the site's administrator (lab/env.sh
# picks the password file from PC_URL, which must be the site's Prism
# Central). KEEP=1 stops before the Disconnect.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_VM_IP:?source lab/env.sh first}" "${PC_URL:?}"
slug=netcheck
NET=/api/networking/v4.4/config
umask 077; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail=0
api() { local code; code=$(lab/tenants/api.sh "$@" 2>&1 >"$tmp/out" | tail -1); echo "${code#HTTP }"; }
field() { jq -r "$1" "$tmp/out" 2>/dev/null; }
say() { printf '%-4s %s\n' "$1" "$2"; [ "$1" = FAIL ] && fail=1; return 0; }
now() { date +%s; }

api GET /api/v1/sites >/dev/null
site="${1:-$(field '.sites[0].id')}"
if [ -z "$site" ] || [ "$site" = null ]; then echo "no sites on the appliance" >&2; exit 1; fi

# 1. The tenant on the site, then its connection and its network.
t0=$(now)
jq -n --arg s "$site" '{slug:"netcheck", display_name:"Network check", sites:[{site:$s}]}' > "$tmp/t.json"
code=$(api POST /api/v1/tenants "$tmp/t.json")
case "$code" in 20*) say ok "tenant $slug created on $site" ;; 409) say ok "tenant $slug exists" ;; *) say FAIL "tenant $slug: HTTP $code $(field .error)"; exit 1 ;; esac
pl() { jq -r --arg s "$site" ".placements[] | select(.site == \$s) | $1" "$tmp/out" 2>/dev/null; }
state=""
for _ in $(seq 1 120); do
  api GET "/api/v1/tenants/$slug" >/dev/null
  state=$(pl .network.state)
  case "$(pl .status):$state" in failed:*|*:ready|*:failed) break ;; esac
  sleep 5
done
if [ "$state" = ready ]; then
  say ok "network ready in $(( $(now) - t0 )) s: subnet $(field .subnet), range $(pl .network.transit_prefix), outbound $(pl .network.snat_ip), floating IPs $(pl '[.network.floating_ips[]?.ip] | join(" ")')"
else
  say FAIL "connection $(pl .status), network ${state:-none}: $(pl .last_error) $(pl .network.error)"; exit 1
fi
project=$(pl .project_id)
fip_ip=$(pl '.network.floating_ips[0].ip // empty')
api GET "/api/v1/tenants/$slug/steps" >/dev/null
jq -r '.[]? | select(.name | startswith("net-")) | "     \(.name) \(if .ok then "ok" else "FAILED" end) \(.detail // "")"' "$tmp/out" 2>/dev/null

# 2. The data path, from a probe VM on the network.
if [ -n "$fip_ip" ]; then
  if FIP_NAME="$project-fip-1" lab/network/probe-vm.sh create "$project" > "$tmp/vm.log" 2>&1; then
    say ok "probe VM created on $project-net"
  else
    say FAIL "probe VM: $(tail -1 "$tmp/vm.log")"
  fi
  ok=""
  for _ in $(seq 1 30); do
    if FIP_NAME="$project-fip-1" lab/network/probe-vm.sh check "$project" > "$tmp/check.log" 2>&1; then ok=1; break; fi
    sleep 10
  done
  if [ -n "$ok" ]; then
    say ok "VM reached on $fip_ip"; sed 's/^/     /' "$tmp/check.log"
    if grep -q '^internet: 200' "$tmp/check.log"; then
      say ok "the VM reaches the Internet"
    else
      say FAIL "the VM does not reach the Internet (the router's route and NAT of the routed prefix)"
    fi
  else
    say FAIL "VM not reached on $fip_ip: $(tail -1 "$tmp/check.log")"
  fi
  [ -n "${KEEP:-}" ] && { echo "KEEP set: tenant and VM left in place"; exit "$fail"; }
  if FIP_NAME="$project-fip-1" lab/network/probe-vm.sh delete "$project" > "$tmp/vm.log" 2>&1; then
    say ok "probe VM deleted"
  else
    say FAIL "probe VM delete: $(tail -1 "$tmp/vm.log")"
  fi
else
  say FAIL "no floating IP on the network"
fi
[ -n "${KEEP:-}" ] && exit "$fail"

# 3. Disconnect and delete; nothing of the tenant stays in Prism Central.
t1=$(now)
code=$(api DELETE "/api/v1/tenants/$slug/sites/$site")
case "$code" in 20*) ;; *) say FAIL "disconnect: HTTP $code $(field .error) $(field '.associated // [] | join(", ")')"; exit 1 ;; esac
for _ in $(seq 1 120); do
  api GET "/api/v1/tenants/$slug" >/dev/null
  [ -z "$(pl .status)" ] && break
  [ "$(pl .status)" = failed ] && break
  sleep 5
done
if [ -z "$(pl .status)" ]; then say ok "disconnected in $(( $(now) - t1 )) s"; else say FAIL "disconnect: $(pl .status) $(pl .last_error)"; fi
code=$(api DELETE "/api/v1/tenants/$slug")
case "$code" in 20*) say ok "tenant $slug deleted" ;; *) say FAIL "delete: HTTP $code $(field .error)" ;; esac
# A list Prism Central did not answer is no proof of anything: read again,
# and fail if it never answers.
for kind in subnets vpcs floating-ips; do
  left="?"
  for _ in 1 2 3 4 5; do
    if left=$(lab/pc.sh GET "$NET/$kind?\$limit=100" 2>/dev/null | jq -er --arg p "$project-" '[.data[]? | select((.name // "") | startswith($p)) | .name] | join(" ")' 2>/dev/null); then break; fi
    left="?"; sleep 5
  done
  if [ "$left" = "?" ]; then say FAIL "$kind: Prism Central did not answer the list"
  elif [ -z "$left" ]; then say ok "no $kind of $project left"
  else say FAIL "$kind left: $left"; fi
done
exit "$fail"
