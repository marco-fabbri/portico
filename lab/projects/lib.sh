#!/usr/bin/env bash
# Shared helpers for Projects 2.0 lab scripts. Source after lab/env.sh.
# Measured on pc.7.6.0.6: projects and resource groups live in
# /api/multidomain/v4.4.b1, role memberships in /api/iam/v4.1.b3,
# SAML groups and roles in /api/iam/v4.0.
MD="/api/multidomain/v4.4.b1"
IAM0="/api/iam/v4.0"
IAM1="/api/iam/v4.1.b3"
# shellcheck source=lab/lib.sh
source lab/lib.sh

project_extid() { lab/pc.sh GET "$MD/config/projects" 2>/dev/null | jq -r --arg n "$1" '.data[] | select(.name==$n) | .extId'; }
resource_group_extids() { lab/pc.sh GET "$MD/config/resource-groups" 2>/dev/null | jq -r --arg p "$1" '.data[]? | select(.projectExtId==$p) | .extId'; }
role_extid() { lab/pc.sh GET "$IAM0/authz/roles?\$filter=displayName%20eq%20'$(jq -rn --arg n "$1" '$n|@uri')'" 2>/dev/null | jq -r '.data[0].extId // empty'; }
# The lab IdP only, by name: never "the first IdP" on a Prism Central that may have others.
saml_idp_extid() { lab/pc.sh GET "$IAM1/authn/saml-identity-providers" 2>/dev/null | jq -r --arg n "${LAB_SAML_IDP:-portico-keycloak}" '.data[]? | select(.name==$n) | .extId'; }
user_group_extid() { lab/pc.sh GET "$IAM0/authn/user-groups" 2>/dev/null | jq -r --arg n "$1" '.data[]? | select(.name==$n) | .extId'; }

# post PATH JSON OUTFILE -> prints the HTTP code, writes the body to OUTFILE;
# returns 0 on 2xx. (A function run inside $(...) cannot export variables.)
post() {
  local code cf; cf=$(mktemp)
  lab/pc.sh POST "$1" "$2" 2>"$cf" > "$3"; code=$(tr -d '\n' <"$cf"); rm -f "$cf"
  echo "$code"; [[ "$code" == "HTTP 2"* ]]
}
