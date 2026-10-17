#!/usr/bin/env bash
# Create the `portico` service account in Prism Central with an API key and
# the Project Manager authorization policy (design 4.5 / 6.7).
#   lab/auth/create-service-account.sh            create (idempotent)
#   lab/auth/create-service-account.sh delete     remove key, policy entry, account
# The API key is written once to ~/portico-secrets/portico-sa-key (0600):
# Prism Central never returns it again. Requires `source lab/env.sh` as admin.
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
IAM0="/api/iam/v4.0"; IAM1="/api/iam/v4.1.b3"
SA_NAME="portico"; KEY_NAME="portico-lab-key"; POLICY_NAME="Project Manager_acp"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
etag_of() { local h; h=$(mktemp); PC_HEADERS_FILE="$h" lab/pc.sh GET "$1" >/dev/null 2>&1; grep -i '^etag:' "$h" | awk '{print $2}' | tr -d '\r'; rm -f "$h"; }
sa_extid() { lab/pc.sh GET "$IAM0/authn/users" 2>/dev/null | jq -r --arg n "$SA_NAME" '.data[] | select(.username==$n) | .extId'; }
policy_extid() {
  local p; p=$(lab/pc.sh GET "$IAM1/authz/authorization-policies" 2>/dev/null | jq -r --arg n "$POLICY_NAME" '.data[] | select(.displayName==$n) | .extId')
  [ -n "$p" ] || { echo "no policy named $POLICY_NAME on this Prism Central" >&2; exit 1; }
  echo "$p"
}
# post PATH JSON OUTFILE -> prints the code; fails unless 2xx.
post() { local c; lab/pc.sh POST "$1" "$2" 2>"$tmp/code" > "$3"; c=$(tr -d '\n' <"$tmp/code"); [[ "$c" == "HTTP 2"* ]] || { echo "$c" >&2; cat "$3" >&2; return 1; }; }

set_policy_identities() {  # $1 = policy extId, $2 = jq expression producing the identities array
  lab/pc.sh GET "$IAM1/authz/authorization-policies/$1" 2>/dev/null \
    | jq '.data | del(.["$reserved"],.["$objectType"],.links,.tenantId,.createdBy,.createdTime,.lastUpdatedTime,.extId,.isSystemDefined,.clientName,.assignedUserGroupsCount,.assignedUsersCount) | .entities |= map(del(.["$reserved"]))' \
    | jq "$2" > "$tmp/pol.json"
  IF_MATCH="$(etag_of "$IAM1/authz/authorization-policies/$1")" lab/pc.sh PUT "$IAM1/authz/authorization-policies/$1" "$tmp/pol.json" 2>"$tmp/code" | jq -r '.data.message // .data.error[0].message'
  [[ "$(tr -d '\n' <"$tmp/code")" == "HTTP 2"* ]] || { echo "policy update failed" >&2; exit 1; }
}

case "${1:-create}" in
  create)
    sa=$(sa_extid)
    if [ -z "$sa" ]; then
      jq -n --arg n "$SA_NAME" '{username:$n, userType:"SERVICE_ACCOUNT", displayName:"Portico service account", description:"Portico: tenant provisioning", emailId:"portico@lab.portico.test"}' > "$tmp/sa.json"
      cp "$tmp/sa.json" docs/lab/payloads/service-account-create.json
      post "$IAM1/authn/users" "$tmp/sa.json" "$tmp/resp.json" || exit 1
      sa=$(jq -r '.data.extId' "$tmp/resp.json"); echo "service account created: $sa"
    else echo "service account exists: $sa"; fi
    if ! lab/pc.sh GET "$IAM1/authn/users/$sa/keys" 2>/dev/null | jq -e --arg k "$KEY_NAME" '.data[]? | select(.name==$k and .status=="VALID")' >/dev/null; then
      jq -n --arg k "$KEY_NAME" '{name:$k, keyType:"API_KEY", description:"Portico lab API key"}' > "$tmp/k.json"
      cp "$tmp/k.json" docs/lab/payloads/service-account-key-create.json
      post "$IAM1/authn/users/$sa/keys" "$tmp/k.json" "$tmp/resp.json" || exit 1
      umask 077; jq -er '.data.keyDetails | "apiKey=" + .apiKey' "$tmp/resp.json" > "$S/portico-sa-key"
      echo "api key created: $(jq -r '.data.extId' "$tmp/resp.json") (saved to $S/portico-sa-key, shown once)"
    else echo "api key exists"; fi
    pol=$(policy_extid) || exit 1
    if lab/pc.sh GET "$IAM1/authz/authorization-policies/$pol" 2>/dev/null | jq -e --arg u "$sa" '.data.identities[].identityFilter.user.uuid.anyof? // [] | index($u)' >/dev/null; then
      echo "already in $POLICY_NAME"
    else
      set_policy_identities "$pol" ".identities |= (map(if .identityFilter.user.uuid then .identityFilter.user.uuid.anyof += [\"$sa\"] else . end))"
    fi
    echo "test: PC_API_KEY=\$(cut -d= -f2- $S/portico-sa-key) lab/pc.sh GET /api/multidomain/v4.4.b1/config/projects"
    ;;
  delete)
    sa=$(sa_extid); [ -n "$sa" ] || { echo "no service account $SA_NAME"; exit 0; }
    pol=$(policy_extid) || exit 1
    set_policy_identities "$pol" ".identities |= map(if .identityFilter.user.uuid then .identityFilter.user.uuid.anyof -= [\"$sa\"] else . end)"
    for k in $(lab/pc.sh GET "$IAM1/authn/users/$sa/keys" 2>/dev/null | jq -r '.data[]?.extId'); do
      IF_MATCH="$(etag_of "$IAM1/authn/users/$sa/keys/$k")" lab/pc.sh DELETE "$IAM1/authn/users/$sa/keys/$k" 2>&1 >/dev/null | tr -d '\n'; echo " key $k deleted"
    done
    IF_MATCH="$(etag_of "$IAM0/authn/users/$sa")" lab/pc.sh DELETE "$IAM0/authn/users/$sa" 2>&1 >/dev/null | tr -d '\n'; echo " service account deleted"
    ;;
  *) echo "usage: $0 [create|delete]" >&2; exit 2 ;;
esac
