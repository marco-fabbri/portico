#!/usr/bin/env bash
# Sites usability measurements (write-up 24, item 1): what a Projects 2.0
# project holds on pc.7.6, per kind, and whether each kind can be listed
# by project. Lab only, credentials from lab/env.sh (the lab admin, or the
# service account when PC_API_KEY is set). Read-only by default.
#   source lab/env.sh && lab/sites/project-contents.sh [PROJECT_NAME]
#   source lab/env.sh && lab/sites/project-contents.sh --scratch
# One line per kind: kind, server-side count with the filter that worked
# (or the HTTP code of each filter tried), and the client-side count of
# list items whose JSON names the project's extId anywhere.
# --scratch writes to Prism Central: it creates a project
# portico-measure-<UTC time> and one powered-off VM with no disk in it,
# tries to delete the project, records the answer, then deletes the VM
# and the project (a trap cleans up on any exit).
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
usage() { sed -n '6,7p' "$0" | sed 's/^# *//' >&2; }
case "${1:-}" in -h|--help) usage; exit 0 ;; esac
: "${PC_URL:?source lab/env.sh first}"
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh
umask 077; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
who="admin"; [ -n "${PC_API_KEY:-}" ] && who="service account"

# kind|list path (each a v4 list endpoint of pc.7.6; versions as the code
# uses them where it does)
KINDS='vms|/api/vmm/v4.0/ahv/config/vms
images|/api/vmm/v4.0/content/images
templates|/api/vmm/v4.0/content/templates
ovas|/api/vmm/v4.0/content/ovas
volume-groups|/api/volumes/v4.0/config/volume-groups
subnets|/api/networking/v4.0/config/subnets
vpcs|/api/networking/v4.0/config/vpcs
floating-ips|/api/networking/v4.0/config/floating-ips
security-policies|/api/microseg/v4.0/config/policies
address-groups|/api/microseg/v4.0/config/address-groups
service-groups|/api/microseg/v4.0/config/service-groups
categories|/api/prism/v4.0/config/categories
recovery-points|/api/dataprotection/v4.0/config/recovery-points'

# get PATH -> body in $tmp/out, HTTP code on stdout
get() { lab/pc.sh GET "$1" >"$tmp/out" 2>"$tmp/code"; tail -1 "$tmp/code" | sed 's/HTTP //'; }
enc() { jq -rn --arg v "$1" '$v|@uri'; }

count() {  # count PROJECT_EXTID
  local p="$1" kind path f code tried n all
  printf '%-18s %-10s %-34s %s\n' kind server filter "client-side (of listed)"
  while IFS='|' read -r kind path; do
    n=; tried=
    for f in "projectExtId eq '$p'" "project/extId eq '$p'" "ownershipInfo/project/extId eq '$p'"; do
      code=$(get "$path?\$limit=1&\$filter=$(enc "$f")")
      if [ "$code" = 200 ]; then n=$(jq -r '.metadata.totalAvailableResults // 0' "$tmp/out"); tried="$f"; break; fi
      tried="$tried${tried:+, }$code"
    done
    local shown="tried: $tried"; [ -n "$n" ] && shown="$tried"
    code=$(get "$path?\$limit=100")
    if [ "$code" = 200 ]; then
      all=$(jq -r '.metadata.totalAvailableResults // (.data|length) // 0' "$tmp/out")
      local mine; mine=$(jq --arg p "$p" '[.data[]? | select(tostring | contains($p))] | length' "$tmp/out")
      printf '%-18s %-10s %-34s %s (of %s)\n' "$kind" "${n:-none}" "$shown" "$mine" "$all"
    else
      printf '%-18s %-10s %-34s list HTTP %s\n' "$kind" "${n:-none}" "$shown" "$code"
    fi
  done <<<"$KINDS"
}

if [ "${1:-}" != --scratch ]; then
  name="${1:-portico-acme}"
  p=$(project_extid "$name"); [ -n "$p" ] || { echo "no project $name" >&2; exit 1; }
  echo "project $name, as the $who, $(date -u +%FT%TZ)"
  count "$p"
  exit 0
fi

# --scratch: does Prism Central refuse to delete a project that holds a VM?
name="portico-measure-$(date -u +%Y%m%dT%H%M%SZ)"; vm=; p=
cleanup() {
  [ -n "$vm" ] && { t=$(IF_MATCH="$(etag_of "/api/vmm/v4.0/ahv/config/vms/$vm")" lab/pc.sh DELETE "/api/vmm/v4.0/ahv/config/vms/$vm" 2>/dev/null | jq -r '.data.extId // empty'); [ -n "$t" ] && wait_task "$t" 300 >/dev/null 2>&1; }
  if [ -n "$p" ]; then
    for rg in $(resource_group_extids "$p"); do
      t=$(IF_MATCH="$(etag_of "$MD/config/resource-groups/$rg")" lab/pc.sh DELETE "$MD/config/resource-groups/$rg" 2>/dev/null | jq -r '.data.extId // empty'); [ -n "$t" ] && wait_task "$t" 300 >/dev/null 2>&1
    done
    t=$(IF_MATCH="$(etag_of "$MD/config/projects/$p")" lab/pc.sh DELETE "$MD/config/projects/$p" 2>/dev/null | jq -r '.data.extId // empty'); [ -n "$t" ] && wait_task "$t" 300 >/dev/null 2>&1
    echo "cleanup: project still there: $([ -n "$(project_extid "$name")" ] && echo yes || echo no)"
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT
# As lab/projects/create.sh: the project, then a resource group on the
# first AOS cluster and its default-container.
jq -n --arg n "$name" '{id: $n, name: $n, description: "Portico write-up 24, deleted by the script"}' > "$tmp/p.json"
code=$(post "$MD/config/projects" "$tmp/p.json" "$tmp/out"); echo "create project: $code"
t=$(jq -r '.data.extId // empty' "$tmp/out"); [ -n "$t" ] && wait_task "$t" 300 >/dev/null
p=$(project_extid "$name"); [ -n "$p" ] || { echo "project not found after create: $(jq -c '.error // empty' "$tmp/out" | head -c 400)" >&2; exit 1; }
cl=$(lab/pc.sh GET "/api/clustermgmt/v4.0/config/clusters" 2>/dev/null | jq -r '[.data[] | select(.config.clusterFunction | index("AOS"))][0].extId')
sc=$(lab/pc.sh GET "/api/clustermgmt/v4.0/config/storage-containers?\$limit=100" 2>/dev/null | jq -r --arg c "$cl" '[.data[] | select(.clusterExtId == $c and (.name | startswith("default-container")))][0].containerExtId')
jq -n --arg n "$name-rg" --arg p "$p" --arg c "$cl" --arg s "$sc" '{name: $n, projectExtId: $p, placementTargets: [{clusterExtId: $c, storageContainers: [{extId: $s}]}]}' > "$tmp/rg.json"
code=$(post "$MD/config/resource-groups" "$tmp/rg.json" "$tmp/out"); echo "create resource group: $code"
t=$(jq -r '.data.extId // empty' "$tmp/out"); [ -n "$t" ] && wait_task "$t" 300 >/dev/null
jq -n --arg n "$name-vm" --arg c "$cl" --arg p "$p" '{name: $n, numSockets: 1, numCoresPerSocket: 1, memorySizeBytes: 536870912, cluster: {extId: $c}, project: {extId: $p}}' > "$tmp/vm.json"
code=$(post "/api/vmm/v4.0/ahv/config/vms" "$tmp/vm.json" "$tmp/out"); echo "create vm in project: $code $(jq -c '.error // empty' "$tmp/out" | head -c 400)"
t=$(jq -r '.data.extId // empty' "$tmp/out"); [ -n "$t" ] && echo "vm task: $(wait_task "$t" 300 2>"$tmp/err") $(head -c 400 "$tmp/err")"
vm=$(lab/pc.sh GET "/api/vmm/v4.0/ahv/config/vms?\$filter=$(enc "name eq '$name-vm'")" 2>/dev/null | jq -r '.data[0].extId // empty')
echo "vm: ${vm:-not found}"
count "$p"
IF_MATCH="$(etag_of "$MD/config/projects/$p")" lab/pc.sh DELETE "$MD/config/projects/$p" >"$tmp/out" 2>"$tmp/code"
echo "delete project holding a vm: $(tail -1 "$tmp/code") $(jq -c '.error // empty' "$tmp/out" | head -c 600)"
# A v4 delete answers with a task; the refusal, if any, is in the task.
t=$(jq -r '.data.extId // empty' "$tmp/out")
[ -n "$t" ] && echo "delete task: $(wait_task "$t" 120 2>"$tmp/err") $(head -c 600 "$tmp/err")"
still=$(project_extid "$name"); echo "project still there after that delete: $([ -n "$still" ] && echo yes || echo no)"
[ -z "$still" ] && p=
