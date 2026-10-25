#!/usr/bin/env bash
# Bind a SAML group to a role inside a project (role membership).
#   lab/projects/bind-group.sh PROJECT_NAME GROUP_NAME [ROLE_DISPLAY_NAME]
# Creates the SAML user group in Prism Central if missing, shares the SAML
# IdP with the project, then creates the role membership with the
# ProjectsScopeTemplate (name found in the Nutanix Terraform provider docs;
# the scope-templates endpoint is mTLS-only on pc.7.6).
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}"
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh
pname="${1:?project name}"; gname="${2:?group name}"; rname="${3:-Project Admin}"
# Lab safety: only projects named portico-* (the Portico prefix) are ever touched.
[[ "$pname" == portico-* ]] || { echo "refusing: project name must start with portico-: $pname" >&2; exit 2; }
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

pj=$(project_extid "$pname"); [ -n "$pj" ] || { echo "no project $pname" >&2; exit 1; }
idp=$(saml_idp_extid); [ -n "$idp" ] || { echo "no SAML IdP in Prism Central" >&2; exit 1; }
role=$(role_extid "$rname"); [ -n "$role" ] || { echo "no role $rname" >&2; exit 1; }

grp=$(user_group_extid "$gname")
if [ -z "$grp" ]; then
  jq -n --arg n "$gname" --arg i "$idp" '{name:$n, groupType:"SAML", idpId:$i}' > "$tmp/g.json"
  code=$(post "$IAM0/authn/user-groups" "$tmp/g.json" "$tmp/resp.json") || { echo "$code"; cat "$tmp/resp.json"; exit 1; }
  grp=$(jq -r .data.extId "$tmp/resp.json"); echo "saml group created: $gname $grp"
else echo "saml group exists: $gname $grp"; fi

shared=$(lab/pc.sh GET "$IAM1/authn/saml-identity-providers/$idp" 2>/dev/null | jq -r '.data | (.isSharedWithAllProjects|tostring) + " " + ((.sharedWithProjects // [])|join(","))')
if [[ "$shared" != "true"* ]] && [[ "$shared" != *"$pj"* ]]; then
  jq -n --arg p "$pj" '{projectExtId:$p}' > "$tmp/s.json"
  IF_MATCH="$(etag_of "$IAM1/authn/saml-identity-providers/$idp")" lab/pc.sh POST "$IAM1/authn/config/saml-identity-provider/$idp/\$actions/share" "$tmp/s.json" 2>/dev/null | jq -r '.data.message'
else echo "idp already shared with project"; fi

existing=$(lab/pc.sh GET "$IAM1/authz/role-memberships" 2>/dev/null | jq -r --arg g "$grp" --arg p "$pj" --arg r "$role" '.data[]? | select(.identityExtId==$g and .projectExtId==$p and .roleExtId==$r) | .extId')
if [ -z "$existing" ]; then
  jq -n --arg g "$grp" --arg i "$idp" --arg p "$pj" --arg r "$role" \
    '{identityExtId:$g, identityType:"GROUP", idpExtId:$i, projectExtId:$p, roleExtId:$r, scopeTemplateName:"ProjectsScopeTemplate", scopeTemplateNameValues:[{name:"projectExtId", value:$p}]}' > "$tmp/rm.json"
  cp "$tmp/rm.json" docs/lab/payloads/role-membership-create.json
  code=$(post "$IAM1/authz/role-memberships" "$tmp/rm.json" "$tmp/resp.json") || { echo "$code"; cat "$tmp/resp.json"; exit 1; }
  echo "role membership created: $(jq -r .data.extId "$tmp/resp.json") ($gname -> $rname in $pname)"
else echo "role membership exists: $existing"; fi
