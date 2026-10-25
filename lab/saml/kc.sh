#!/usr/bin/env bash
# Keycloak admin REST helper for the lab realm. Lab only.
#   lab/saml/kc.sh METHOD PATH [JSON_FILE]     (PATH relative to /admin/realms)
# Runs curl on the lab VM against Keycloak's loopback port: the admin
# console and the master realm are never published on the public name
# (Portico refuses them), so the admin API is reachable only from the VM.
# The admin password and the request body travel over SSH on stdin, never
# on a command line. Token obtained per call.
set -euo pipefail
: "${LAB_VM_IP:?source lab/env.sh first}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
method="${1:?}"; path="${2-}"; body="${3:-}"
# LAB_SSH_OPTS adds ssh options, e.g. "-o UserKnownHostsFile=/dev/null
# -o StrictHostKeyChecking=no" for the appliance, whose host key changes
# with every new VM.
read -r -a extra <<<"${LAB_SSH_OPTS:-}"
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=5 -o LogLevel=ERROR "${extra[@]}" "${LAB_SSH_USER:-admin}@$LAB_VM_IP")
KC="http://127.0.0.1:8080"

# 1. Admin token: the form body (with the password) goes through stdin.
token=$(printf 'client_id=admin-cli&grant_type=password&username=kcadmin&password=%s' \
  "$(jq -rn --arg p "$(cat "$S/keycloak-admin-pw")" '$p|@uri')" \
  | "${SSH[@]}" "curl -s --data-binary @- '$KC/realms/master/protocol/openid-connect/token'" | jq -r .access_token)
if [ -z "$token" ] || [ "$token" = null ]; then echo "kc.sh: no admin token" >&2; exit 1; fi

# 2. The call: bearer token in a curl config file, body as a second file;
#    both are embedded base64 in a small script that reaches the VM on stdin
#    and unpacks them in a private directory.
cfg=$(mktemp); trap 'rm -f "$cfg"' EXIT
{
  printf 'header = "Authorization: Bearer %s"\n' "$token"
  printf 'header = "Content-Type: application/json"\n'
  printf 'request = "%s"\n' "$method"
  printf 'write-out = "\\nHTTP %%{http_code}\\n"\n'
  printf 'url = "%s"\n' "$KC/admin/realms$path"
} > "$cfg"
remote="umask 077; d=\$(mktemp -d); trap 'rm -rf \"\$d\"' EXIT
printf '%s' '$(base64 < "$cfg" | tr -d '\n')' | base64 -d > \"\$d/cfg\"
"
if [ -n "$body" ]; then
  remote+="printf '%s' '$(base64 < "$body" | tr -d '\n')' | base64 -d > \"\$d/body\"
curl -s --config \"\$d/cfg\" --data-binary \"@\$d/body\"
"
else
  remote+="curl -s --config \"\$d/cfg\"
"
fi
"${SSH[@]}" bash -s <<<"$remote" | awk 'prev!="" {print prev} {prev=$0} END {print prev > "/dev/stderr"}'
