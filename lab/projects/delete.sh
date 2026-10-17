#!/usr/bin/env bash
# Delete a lab project: its role memberships, resource groups, then the project.
#   lab/projects/delete.sh PROJECT_NAME [SAML_GROUP_NAME]
# Refuses to touch _internal. Requires `source lab/env.sh`.
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}"
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh
name="${1:?project name}"
# Lab safety: only projects named portico-lab-* are ever touched.
[[ "$name" == portico-lab-* ]] || { echo "refusing: project name must start with portico-lab-: $name" >&2; exit 2; }
pj=$(project_extid "$name"); [ -n "$pj" ] || { echo "no project $name"; exit 0; }

for rm in $(lab/pc.sh GET "$IAM1/authz/role-memberships" 2>/dev/null | jq -r --arg p "$pj" '.data[]? | select(.projectExtId==$p) | .extId'); do
  IF_MATCH="$(etag_of "$IAM1/authz/role-memberships/$rm")" lab/pc.sh DELETE "$IAM1/authz/role-memberships/$rm" 2>&1 >/dev/null | tr -d '\n'; echo " role membership $rm deleted"
done
# The project cannot be deleted while the SAML IdP is shared with it
# (associatedEntities={"shared saml identity providers":1}).
idp=$(saml_idp_extid)
if [ -n "$idp" ] && lab/pc.sh GET "$IAM1/authn/saml-identity-providers/$idp" 2>/dev/null | jq -e --arg p "$pj" '.data.sharedWithProjects // [] | index($p)' >/dev/null; then
  tmp=$(mktemp -d); jq -n --arg p "$pj" '{projectExtId:$p}' > "$tmp/u.json"
  IF_MATCH="$(etag_of "$IAM1/authn/saml-identity-providers/$idp")" lab/pc.sh POST "$IAM1/authn/config/saml-identity-provider/$idp/\$actions/unshare" "$tmp/u.json" 2>/dev/null | jq -r '.data.message'; rm -rf "$tmp"
fi
for rg in $(resource_group_extids "$pj"); do
  IF_MATCH="$(etag_of "$MD/config/resource-groups/$rg")" lab/pc.sh DELETE "$MD/config/resource-groups/$rg" 2>/dev/null | jq -r '.data.extId' | { read -r t; [ -n "$t" ] && wait_task "$t"; }; echo "resource group $rg deleted"
done
IF_MATCH="$(etag_of "$MD/config/projects/$pj")" lab/pc.sh DELETE "$MD/config/projects/$pj" 2>/dev/null | jq -r '.data.extId' | { read -r t; [ -n "$t" ] && wait_task "$t"; }
echo "project $name deleted"
# Optional: also delete the SAML group of this tenant in Prism Central.
if [ -n "${2:-}" ]; then
  g=$(user_group_extid "$2")
  [ -n "$g" ] && IF_MATCH="$(etag_of "$IAM0/authn/user-groups/$g")" lab/pc.sh DELETE "$IAM0/authn/user-groups/$g" 2>&1 >/dev/null | tr -d '\n' && echo " saml group $2 deleted"
fi
