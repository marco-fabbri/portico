#!/usr/bin/env bash
# Shared helpers for lab/vm scripts. Source after lab/env.sh.

# wait_task TASK_EXTID -> prints the final status, exits 1 if not SUCCEEDED
wait_task() {
  local t="$1" st
  while :; do
    st=$(lab/pc.sh GET "/api/prism/v4.0/config/tasks/$t" 2>/dev/null | jq -r '.data.status // "UNKNOWN"')
    case "$st" in
      SUCCEEDED) echo "$st"; return 0 ;;
      FAILED|CANCELED) echo "$st"; lab/pc.sh GET "/api/prism/v4.0/config/tasks/$t" 2>/dev/null | jq -c '.data.errorMessages // .data.legacyErrorMessage' >&2; return 1 ;;
      *) sleep 5 ;;
    esac
  done
}

# vm_extid NAME -> extId or empty
vm_extid() {
  lab/pc.sh GET "/api/vmm/v4.0/ahv/config/vms?\$filter=name%20eq%20'$1'" 2>/dev/null | jq -r '.data[0].extId // empty'
}

# image_extid NAME -> extId or empty
image_extid() {
  lab/pc.sh GET "/api/vmm/v4.0/content/images?\$filter=name%20eq%20'$1'" 2>/dev/null | jq -r '.data[0].extId // empty'
}

# etag_of PATH -> ETag header value of a GET
etag_of() {
  local h; h=$(mktemp)
  PC_HEADERS_FILE="$h" lab/pc.sh GET "$1" >/dev/null 2>&1
  grep -i '^etag:' "$h" | awk '{print $2}' | tr -d '\r'; rm -f "$h"
}

# vm_action EXTID ACTION -> runs POST .../$actions/ACTION with If-Match
vm_action() {
  local id="$1" action="$2" etag
  etag=$(etag_of "/api/vmm/v4.0/ahv/config/vms/$id")
  IF_MATCH="$etag" lab/pc.sh POST "/api/vmm/v4.0/ahv/config/vms/$id/\$actions/$action" 2>/dev/null | jq -r '.data.extId'
}
