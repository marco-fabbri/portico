#!/usr/bin/env bash
# Shared helpers for lab/vm scripts. Source after lab/env.sh.
# shellcheck source=lab/lib.sh
source lab/lib.sh

# vm_extid NAME -> extId or empty
vm_extid() {
  lab/pc.sh GET "/api/vmm/v4.0/ahv/config/vms?\$filter=name%20eq%20'$1'" 2>/dev/null | jq -r '.data[0].extId // empty'
}

# image_extid NAME -> extId or empty
image_extid() {
  lab/pc.sh GET "/api/vmm/v4.0/content/images?\$filter=name%20eq%20'$1'" 2>/dev/null | jq -r '.data[0].extId // empty'
}

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
