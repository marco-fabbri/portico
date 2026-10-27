#!/usr/bin/env bash
# Shared helpers for lab/vm scripts. Source after lab/env.sh.
# shellcheck source=lab/lib.sh
source lab/lib.sh

# extid_by_name PATH NAME -> extId of the first entity of the list at PATH
# named NAME, or empty. The list is read page by page and matched here:
# on pc.7.6 a $filter on the vmm lists answered 504 "upstream request
# timeout" for minutes while the plain list answered (2026-10-27).
extid_by_name() {
  local path="$1" name="$2" page=0 body n id
  while :; do
    body=$(lab/pc.sh GET "$path?\$page=$page&\$limit=100" 2>/dev/null) || return 0
    id=$(printf '%s' "$body" | jq -r --arg n "$name" '[.data[]? | select(.name == $n)][0].extId // empty')
    if [ -n "$id" ]; then echo "$id"; return 0; fi
    n=$(printf '%s' "$body" | jq -r '.data | length? // 0')
    if [ "$n" -lt 100 ]; then return 0; fi
    page=$((page + 1))
  done
}

# vm_extid NAME -> extId or empty
vm_extid() { extid_by_name /api/vmm/v4.0/ahv/config/vms "$1"; }

# image_extid NAME -> extId or empty
image_extid() { extid_by_name /api/vmm/v4.0/content/images "$1"; }

# render_cloud_init SSHKEY IP GATEWAY -> prints lab/vm/cloud-init.yaml with the
# placeholders replaced. The replacement is quoted so that `|`, `&` and `\`
# in the key comment stay literal (bash 5.2+ patsub_replacement).
render_cloud_init() {
  local tpl; tpl=$(<lab/vm/cloud-init.yaml)
  tpl=${tpl//__SSH_KEY__/"$1"}; tpl=${tpl//__IP__/"$2"}; tpl=${tpl//__GW__/"$3"}
  printf '%s\n' "$tpl"
}

# vm_action EXTID ACTION -> runs POST .../$actions/ACTION with If-Match
vm_action() {
  local id="$1" action="$2" etag
  etag=$(etag_of "/api/vmm/v4.0/ahv/config/vms/$id")
  IF_MATCH="$etag" lab/pc.sh POST "/api/vmm/v4.0/ahv/config/vms/$id/\$actions/$action" 2>/dev/null | jq -r '.data.extId'
}
