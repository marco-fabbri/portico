#!/usr/bin/env bash
# A local Prism Central user who is Project Admin of one project only, to
# measure what a tenant's role allows (tenant networking design, section 6
# item 3) without going through Portico. Lab only.
#   source lab/env.sh && lab/projects/probe-user.sh create|delete PROJECT
# The password is generated into PORTICO_SECRETS/portico-probe-user-pw (0600)
# and never printed; run the tenant's calls with
#   PC_USER=portico-probe-user PC_PASS="$(cat .../portico-probe-user-pw)" lab/pc.sh ...
# without sourcing lab/env.sh again (it resets PC_PASS).
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}"
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh
action="${1:?usage: probe-user.sh create|delete PROJECT}"
pname="${2:?usage: probe-user.sh create|delete PROJECT}"
[[ "$pname" == portico-* ]] || { echo "refusing: project name must start with portico-: $pname" >&2; exit 2; }
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
user="portico-probe-user"; pwfile="$S/portico-probe-user-pw"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

user_extid() { lab/pc.sh GET "$IAM1/authn/users?\$limit=100" 2>/dev/null | jq -r --arg u "$user" '.data[]? | select(.username == $u) | .extId'; }
local_idp() { lab/pc.sh GET "$IAM1/authn/users/00000000-0000-0000-0000-000000000000" 2>/dev/null | jq -r '.data.idpId'; }

case "$action" in
  create)
    pj=$(project_extid "$pname"); [ -n "$pj" ] || { echo "no project $pname" >&2; exit 1; }
    role=$(role_extid "Project Admin"); [ -n "$role" ] || { echo "no role Project Admin" >&2; exit 1; }
    u=$(user_extid)
    if [ -z "$u" ]; then
      umask 077
      printf 'Pp1!%s\n' "$(openssl rand -base64 18 | tr -d '/+=\n')" > "$pwfile"
      jq -n --arg u "$user" --rawfile p "$pwfile" '{username:$u, userType:"LOCAL", password:($p|rtrimstr("\n")),
        firstName:"Portico", lastName:"Probe", displayName:"Portico probe user", emailId:"probe@portico.test"}' > "$tmp/u.json"
      code=$(post "$IAM1/authn/users" "$tmp/u.json" "$tmp/resp") || { echo "create user: $code $(jq -c '.data.error // .' "$tmp/resp")" >&2; exit 1; }
      u=$(jq -r '.data.extId' "$tmp/resp"); echo "create user: $code $u (password in $pwfile)"
    else echo "user exists: $u"; fi
    if [ -z "$(lab/pc.sh GET "$IAM1/authz/role-memberships?\$limit=100" 2>/dev/null | jq -r --arg u "$u" --arg p "$pj" '.data[]? | select(.identityExtId == $u and .projectExtId == $p) | .extId')" ]; then
      jq -n --arg u "$u" --arg i "$(local_idp)" --arg p "$pj" --arg r "$role" \
        '{identityExtId:$u, identityType:"USER", idpExtId:$i, projectExtId:$p, roleExtId:$r, scopeTemplateName:"ProjectsScopeTemplate", scopeTemplateNameValues:[{name:"projectExtId", value:$p}]}' > "$tmp/rm.json"
      code=$(post "$IAM1/authz/role-memberships" "$tmp/rm.json" "$tmp/resp") || { echo "role membership: $code $(jq -c '.data.error // .' "$tmp/resp")" >&2; exit 1; }
      echo "role membership: $code $(jq -r '.data.extId' "$tmp/resp") ($user -> Project Admin in $pname)"
    else echo "role membership exists"; fi
    ;;
  delete)
    u=$(user_extid)
    for m in $(lab/pc.sh GET "$IAM1/authz/role-memberships?\$limit=100" 2>/dev/null | jq -r --arg u "$u" '.data[]? | select(.identityExtId == $u) | .extId'); do
      IF_MATCH="$(etag_of "$IAM1/authz/role-memberships/$m")" lab/pc.sh DELETE "$IAM1/authz/role-memberships/$m" 2>&1 >/dev/null | tr -d '\n'; echo " role membership $m"
    done
    if [ -n "$u" ]; then IF_MATCH="$(etag_of "$IAM0/authn/users/$u")" lab/pc.sh DELETE "$IAM0/authn/users/$u" 2>&1 >/dev/null | tr -d '\n'; echo " user $user"; fi
    rm -f "$pwfile"
    ;;
  *) echo "usage: $0 create|delete PROJECT" >&2; exit 2 ;;
esac
