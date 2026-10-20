#!/usr/bin/env bash
# Sites measurements (write-up 20, D5): where Prism Central reports its own
# version through the v4 API. Lab only. Uses lab/pc.sh (admin from
# lab/env.sh, or PC_API_KEY when set: the service account's view).
#   source lab/env.sh && lab/sites/pc-version.sh
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}"
echo "--- clustermgmt v4.0 clusters: the PRISM_CENTRAL entry"
lab/pc.sh GET "/api/clustermgmt/v4.0/config/clusters" 2>/dev/null \
  | jq -r '.data[]? | "\(.name)  functions=\(.config.clusterFunction|join(","))  buildInfo.version=\(.config.buildInfo.version // "-")  fullVersion=\(.config.buildInfo.fullVersion // "-")"'
tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
for v in v4.0 v4.4; do
  code=$(lab/pc.sh GET "/api/prism/$v/config/domain-managers" 2>&1 >"$tmp" | tail -1)
  echo "--- prism $v domain-managers: $code"
  [ "$code" = "HTTP 200" ] && jq -r '.data[]? | "name=\(.config.name // "-")  buildInfo.version=\(.config.buildInfo.version // "-")"' "$tmp"
done
