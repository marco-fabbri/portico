#!/usr/bin/env bash
# Connections on the appliance through Portico's admin API (write-up 25).
#   lab/sites/connections.sh suspend SITE [TENANT]   suspend, read, resume
#   lab/sites/connections.sh cycle SITE [SLUG]       tenant with no site,
#     connect, a VM in its project, Disconnect refused, VM removed,
#     Disconnect, tenant Delete
# Both write (Portico's state, and for `cycle` Prism Central and Keycloak):
# ask first. Lab only. Needs `source lab/env.sh` with PC_URL pointing at the
# site's Prism Central (the VM is created and removed there with the lab
# admin's credentials; Portico itself only uses its service account) and the
# appliance's LAB_* variables (write-up 19) for lab/tenants/api.sh and
# lab/saml/kc.sh. The user's temporary password is never printed or kept.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
usage() { sed -n '3,7p' "$0" | sed 's/^# *//' >&2; }
case "${1:-}" in -h|--help) usage; exit 0 ;; suspend|cycle) ;; *) usage; exit 2 ;; esac
: "${PC_URL:?source lab/env.sh first}" "${LAB_VM_IP:?}"
# shellcheck source=lab/lib.sh
source lab/lib.sh
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh
umask 077; tmp=$(mktemp -d)
A=lab/tenants/api.sh
mode="$1"; site="${2:?site id}"

# api METHOD PATH [JSON] -> body in $tmp/out, "HTTP <code>" on stdout
api() { $A "$@" >"$tmp/out" 2>"$tmp/code"; tail -1 "$tmp/code"; }
show() { printf '%-44s %s %s\n' "$1" "$2" "$(head -c 300 "$tmp/out" | tr '\n' ' ')"; }
placement() { $A GET "/api/v1/tenants/$1" 2>/dev/null | jq -r --arg s "$site" '[.placements[]? | select(.site==$s) | .status][0] // "none"'; }
# wait_placement TENANT UNTIL_REGEX MAX_SECONDS -> last status
wait_placement() {
  local st w=0
  while :; do
    st=$(placement "$1"); [[ "$st" =~ $2 ]] && break
    [ "$w" -ge "$3" ] && break; sleep 5; w=$((w + 5))
  done
  echo "$st (${w}s)"
}

if [ "$mode" = suspend ]; then
  t="${3:-acme}"; trap 'rm -rf "$tmp"' EXIT
  show "sessions on $site" "$(api GET "/api/v1/sites/$site/sessions")"
  show "suspend $t on $site" "$(api POST "/api/v1/tenants/$t/sites/$site/suspend")"
  show "suspend again (expect 409)" "$(api POST "/api/v1/tenants/$t/sites/$site/suspend")"
  echo "placement: $(placement "$t")"
  show "resume $t on $site" "$(api POST "/api/v1/tenants/$t/sites/$site/resume")"
  echo "placement: $(placement "$t")"
  exit 0
fi

slug="${3:-beta}"; vm=
cleanup() {
  if [ -n "$vm" ]; then
    t=$(IF_MATCH="$(etag_of "/api/vmm/v4.0/ahv/config/vms/$vm")" lab/pc.sh DELETE "/api/vmm/v4.0/ahv/config/vms/$vm" 2>/dev/null | jq -r '.data.extId // empty')
    [ -n "$t" ] && echo "cleanup: vm delete $(wait_task "$t" 300 2>&1)"
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT

jq -n --arg s "$slug" '{slug: $s, display_name: ("Lab " + $s), contacts: "ops@\($s).example", sites: []}' >"$tmp/t.json"
show "create $slug with no site (409 when rerun)" "$(api POST /api/v1/tenants "$tmp/t.json")"
st=$(wait_placement "$slug" '^none$' 5); echo "placement on $site: $st"
for _ in $(seq 1 24); do [ "$($A GET "/api/v1/tenants/$slug" 2>/dev/null | jq -r .status)" = active ] && break; sleep 5; done
echo "tenant status: $($A GET "/api/v1/tenants/$slug" 2>/dev/null | jq -r .status)"
jq -n --arg s "$slug" '{email: ("user@" + $s + ".example"), first_name: "Lab", last_name: "User"}' >"$tmp/u.json"
code=$(api POST "/api/v1/tenants/$slug/users" "$tmp/u.json"); echo "create user: $code $(jq -c '{id, email}' "$tmp/out" 2>/dev/null)"; : >"$tmp/out"

jq -n --arg s "$site" '{site: $s}' >"$tmp/c.json"
show "connect $slug to $site" "$(api POST "/api/v1/tenants/$slug/sites" "$tmp/c.json")"
st=$(wait_placement "$slug" '^(active|failed)$' 600); echo "placement: $st"
# A failed connection (a slow or briefly broken Prism Central) is retried once.
if [[ "$st" == failed* ]]; then
  echo "reason: $($A GET "/api/v1/tenants/$slug" 2>/dev/null | jq -r --arg s "$site" '.placements[] | select(.site==$s) | .last_error')"
  show "retry $slug on $site" "$(api POST "/api/v1/tenants/$slug/sites/$site/retry")"
  sleep 5  # the retry runs in the background: let the row leave "failed"
  echo "placement: $(wait_placement "$slug" '^(active|failed)$' 600)"
fi
# The lookup is retried: a busy Prism Central answers a list with a 504 now and then.
project="portico-$slug"; p=
for _ in 1 2 3; do p=$(project_extid "$project" 2>/dev/null); [ -n "$p" ] && break; sleep 5; done
echo "project $project in Prism Central: ${p:-not found}; description: $(lab/pc.sh GET "$MD/config/projects/$p" 2>/dev/null | jq -r '.data.description')"
echo "SAML group tenant-$slug: $(lab/pc.sh GET "$IAM0/authn/user-groups" 2>/dev/null | jq -c --arg n "tenant-$slug" '[.data[]? | select(.name==$n) | {extId, idpId}]')"
[ -n "$p" ] || exit 1

cl=$(lab/pc.sh GET "/api/clustermgmt/v4.0/config/clusters" 2>/dev/null | jq -r '[.data[] | select(.config.clusterFunction | index("AOS"))][0].extId')
jq -n --arg n "$project-vm" --arg c "$cl" --arg p "$p" '{name: $n, numSockets: 1, numCoresPerSocket: 1, memorySizeBytes: 536870912, cluster: {extId: $c}, project: {extId: $p}}' >"$tmp/vm.json"
code=$(post "/api/vmm/v4.0/ahv/config/vms" "$tmp/vm.json" "$tmp/pc"); echo "create vm in project (admin): $code"
t=$(jq -r '.data.extId // empty' "$tmp/pc"); [ -n "$t" ] && echo "vm task: $(wait_task "$t" 300 2>&1)"
vm=$(lab/pc.sh GET "/api/vmm/v4.0/ahv/config/vms?\$filter=$(jq -rn --arg v "name eq '$project-vm'" '$v|@uri')" 2>/dev/null | jq -r '.data[0].extId // empty')
echo "vm: ${vm:-not found}"

show "contents" "$(api GET "/api/v1/tenants/$slug/sites/$site/contents")"
show "disconnect with a vm (expect 409)" "$(api DELETE "/api/v1/tenants/$slug/sites/$site")"
show "delete tenant while connected (expect 409)" "$(api DELETE "/api/v1/tenants/$slug")"

t=$(IF_MATCH="$(etag_of "/api/vmm/v4.0/ahv/config/vms/$vm")" lab/pc.sh DELETE "/api/vmm/v4.0/ahv/config/vms/$vm" 2>/dev/null | jq -r '.data.extId // empty')
echo "vm delete (admin): $([ -n "$t" ] && wait_task "$t" 300 2>&1)"; vm=

show "contents" "$(api GET "/api/v1/tenants/$slug/sites/$site/contents")"
show "disconnect empty (expect 202)" "$(api DELETE "/api/v1/tenants/$slug/sites/$site")"
echo "placement: $(wait_placement "$slug" '^(none|remove-failed)$' 600)"
$A GET "/api/v1/tenants/$slug" 2>/dev/null | jq -c '{status, placements, steps: [.steps[]? | select(.status != "done") ]}'
echo "project $project in Prism Central: $(project_extid "$project" | grep -q . && echo still there || echo gone)"
echo "SAML group tenant-$slug: $(user_group_extid "tenant-$slug" | grep -q . && echo still there || echo gone)"

show "delete tenant (expect 202)" "$(api DELETE "/api/v1/tenants/$slug")"
for _ in $(seq 1 24); do [ "$(api GET "/api/v1/tenants/$slug")" = "HTTP 404" ] && break; sleep 5; done
show "tenant after delete" "$(api GET "/api/v1/tenants/$slug")"
echo "Keycloak group tenant-$slug: $(lab/saml/kc.sh GET "/${LAB_REALM:-portico}/groups?search=tenant-$slug&exact=true" 2>/dev/null | jq -r 'if length == 0 then "gone" else "still there" end')"
echo "Keycloak user user@$slug.example: $(lab/saml/kc.sh GET "/${LAB_REALM:-portico}/users?email=user@$slug.example&exact=true" 2>/dev/null | jq -r 'if length == 0 then "gone" else "still there" end')"
