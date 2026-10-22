#!/usr/bin/env bash
# Sites usability measurements (write-up 24, item 3): what in the v4
# clusters API tells a Nutanix Objects storage container apart from a
# container that holds VM disks. Lab only, read-only, credentials from
# lab/env.sh.
#   source lab/env.sh && lab/sites/container-kinds.sh
# Prints, per cluster, one line per storage container with the fields
# that could mark it (name, owner, flags), then the keys every container
# carries, so a field this list misses is still seen.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
usage() { sed -n '6p' "$0" | sed 's/^# *//' >&2; }
case "${1:-}" in -h|--help) usage; exit 0 ;; esac
: "${PC_URL:?source lab/env.sh first}"
umask 077; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
CM=/api/clustermgmt/v4.0
lab/pc.sh GET "$CM/config/clusters" 2>/dev/null | jq -r '.data[] | [.extId, .name, (.config.clusterFunction | tostring)] | @tsv' > "$tmp/clusters"
lab/pc.sh GET "$CM/config/storage-containers?\$limit=100" > "$tmp/sc" 2>"$tmp/code"
echo "storage containers: $(tail -1 "$tmp/code")"
while IFS=$'\t' read -r id name fn; do
  echo "== cluster $name $fn"
  jq -r --arg c "$id" '.data[]? | select(.clusterExtId == $c) |
    [.name, ("owner=" + (.ownerExtId // "-")), ("marked-for-removal=" + (.isMarkedForRemoval // false | tostring)),
     ("encrypted=" + (.isEncrypted // false | tostring)), ("compression=" + (.isCompressionEnabled // false | tostring)),
     ("erasure=" + (.erasureCode // "-")), ("replication=" + (.replicationFactor // 0 | tostring)),
     ("nfs-whitelist=" + ((.nfsWhitelistAddress // []) | length | tostring)),
     ("internal=" + (.isInternal // false | tostring))] | @tsv' "$tmp/sc"
done < "$tmp/clusters"
echo "== keys present on the containers"
jq -r '[.data[]? | keys[]] | group_by(.) | map("\(.[0]) \(length)") | .[]' "$tmp/sc"
