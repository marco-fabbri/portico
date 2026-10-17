#!/usr/bin/env bash
# Shared helpers for every lab script. Source after lab/env.sh, from the
# repository root (the scripts `cd` there first). Lab only.

# wait_task TASK_EXTID [MAX_SECONDS] -> prints the final status; exit 1 if
# the task fails, is cancelled, or does not finish within MAX_SECONDS (600).
wait_task() {
  local t="$1" max="${2:-600}" waited=0 st
  while :; do
    st=$(lab/pc.sh GET "/api/prism/v4.0/config/tasks/$t" 2>/dev/null | jq -r '.data.status // "UNKNOWN"')
    case "$st" in
      SUCCEEDED) echo "$st"; return 0 ;;
      FAILED|CANCELED) echo "$st"; lab/pc.sh GET "/api/prism/v4.0/config/tasks/$t" 2>/dev/null | jq -c '.data.errorMessages // .data.legacyErrorMessage' >&2; return 1 ;;
    esac
    if [ "$waited" -ge "$max" ]; then echo "task $t did not finish in ${max}s (last status $st)" >&2; return 1; fi
    sleep 5; waited=$((waited + 5))
  done
}

# etag_of PATH -> ETag header value of a GET (needed by v4 PUT/DELETE/$actions)
etag_of() {
  local h; h=$(mktemp)
  PC_HEADERS_FILE="$h" lab/pc.sh GET "$1" >/dev/null 2>&1
  grep -i '^etag:' "$h" | awk '{print $2}' | tr -d '\r'; rm -f "$h"
}

# remote_stage_dir USER@HOST -> prints a private directory created on the VM
# for files in transit (secrets, keys). The caller removes it with
# `ssh USER@HOST rm -rf DIR`, also on error.
remote_stage_dir() {
  ssh -o BatchMode=yes -o ConnectTimeout=5 "$1" 'umask 077; mktemp -d'
}
