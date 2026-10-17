#!/usr/bin/env bash
# Inventory of the lab Prism Central: version, clusters, IAM objects,
# Projects 2.0. Writes docs/lab/raw/inventory-<date>.json (gitignored) and
# prints one summary line per endpoint. A 404 is a finding, not an error.
# Requires `source lab/env.sh`.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
: "${PC_URL:?source lab/env.sh first}"

out="docs/lab/raw/inventory-$(date +%Y-%m-%d).json"
mkdir -p docs/lab/raw
echo '{}' > "$out"

# key|method|path|body-file
endpoints=(
  "version_v1|GET|/PrismGateway/services/rest/v1/cluster|"
  "clusters|GET|/api/clustermgmt/v4.0/config/clusters|"
  "users|GET|/api/iam/v4.0/authn/users|"
  "roles|GET|/api/iam/v4.0/authz/roles?\$limit=100|"
  "authorization_policies|GET|/api/iam/v4.0/authz/authorization-policies|"
  "saml_identity_providers|GET|/api/iam/v4.0/authn/saml-identity-providers|"
  "directory_services|GET|/api/iam/v4.0/authn/directory-services|"
  "saml_sp_metadata|GET|/api/iam/v4.0/authn/saml-sp-metadata|"
  "role_memberships|GET|/api/iam/v4.1.b3/authz/role-memberships|"
  "projects_v2|GET|/api/multidomain/v4.4.b1/config/projects|"
  "resource_groups|GET|/api/multidomain/v4.4.b1/config/resource-groups|"
  "projects_v3|POST|/api/nutanix/v3/projects/list|lab/payloads/v3-projects-list.json"
)
mkdir -p lab/payloads
echo '{"kind":"project","length":50}' > lab/payloads/v3-projects-list.json

for e in "${endpoints[@]}"; do
  IFS='|' read -r key method path body <<<"$e"
  resp=$(lab/pc.sh "$method" "$path" ${body:+"$body"} 2>/tmp/inv_code); code=$(tr -d '\n' </tmp/inv_code)
  if head -c 20 <<<"$resp" | grep -q '<!doctype\|<html'; then
    kind="HTML (UI catch-all, endpoint does not exist)"; json='null'
  elif jq -e . >/dev/null 2>&1 <<<"$resp"; then
    kind=$(jq -r 'if .data then (.data|if type=="array" then (length|tostring)+" items" else "object" end) elif .entities then (.entities|length|tostring)+" entities (v3)" else "json" end' <<<"$resp"); json="$resp"
  else
    kind="non-JSON ($(wc -c <<<"$resp" | tr -d ' ') bytes)"; json=$(jq -Rs . <<<"$resp")
  fi
  printf '%-26s %-6s %-55s %s  %s\n' "$key" "$method" "$path" "$code" "$kind"
  jq --arg k "$key" --arg c "$code" --arg p "$path" --argjson b "$json" '.[$k] = {path:$p, http:$c, body:$b}' "$out" > "$out.tmp" && mv "$out.tmp" "$out"
done
echo "written: $out"
