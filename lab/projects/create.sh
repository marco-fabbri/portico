#!/usr/bin/env bash
# Create a Projects 2.0 project with a resource group (cluster + container).
#   lab/projects/create.sh NAME [CLUSTER_EXTID] [CONTAINER_EXTID]
# Defaults: first AOS cluster, first non-system storage container.
# Idempotent on the project name. Requires `source lab/env.sh`.
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}"
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh
name="${1:?project name}"
# Lab safety: only projects named portico-lab-* are ever touched.
[[ "$name" == portico-lab-* ]] || { echo "refusing: project name must start with portico-lab-: $name" >&2; exit 2; }
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

pj=$(project_extid "$name")
if [ -z "$pj" ]; then
  # 'id' (a slug) is required by the v4.4.b1 schema: "missing property 'id'".
  jq -n --arg n "$name" '{id:$n, name:$n, description:("Portico lab project " + $n)}' > "$tmp/p.json"
  cp "$tmp/p.json" docs/lab/payloads/project-create.json
  code=$(post "$MD/config/projects" "$tmp/p.json" "$tmp/resp.json") || { echo "$code"; cat "$tmp/resp.json"; exit 1; }
  wait_task "$(jq -r .data.extId "$tmp/resp.json")" >/dev/null
  pj=$(project_extid "$name"); echo "project created: $name $pj"
else echo "project exists: $name $pj"; fi

cluster="${2:-$(lab/pc.sh GET /api/clustermgmt/v4.0/config/clusters 2>/dev/null | jq -r '.data[] | select(.config.clusterFunction|index("AOS")) | .extId' | head -1)}"
container="${3:-$(lab/pc.sh GET /api/clustermgmt/v4.0/config/storage-containers 2>/dev/null | jq -r '[.data[] | select(.name|test("SelfService|NutanixManagementShare|NutanixMetadata")|not)] | .[0] | (.containerExtId // .extId)')}"
if [ -z "$(resource_group_extids "$pj")" ]; then
  jq -n --arg n "$name-rg" --arg p "$pj" --arg c "$cluster" --arg s "$container" \
    '{name:$n, projectExtId:$p, placementTargets:[{clusterExtId:$c, storageContainers:[{extId:$s}]}]}' > "$tmp/rg.json"
  cp "$tmp/rg.json" docs/lab/payloads/resource-group-create.json
  code=$(post "$MD/config/resource-groups" "$tmp/rg.json" "$tmp/resp.json") || { echo "$code"; cat "$tmp/resp.json"; exit 1; }
  wait_task "$(jq -r .data.extId "$tmp/resp.json")" >/dev/null
  echo "resource group created: cluster=$cluster container=$container"
else echo "resource group exists"; fi
