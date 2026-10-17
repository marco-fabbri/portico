#!/usr/bin/env bash
# Keycloak admin REST helper for the lab realm. Lab only.
#   lab/saml/kc.sh METHOD PATH [JSON_FILE]     (PATH relative to /admin/realms)
# Talks to Keycloak through the VM's SSH tunnel-free path: the public name
# via nginx with the lab CA. Token cached for the process only.
set -euo pipefail
: "${LAB_VM_IP:?source lab/env.sh first}" "${LAB_ID_HOST:?}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
method="${1:?}"; path="${2-}"; body="${3:-}"
base="https://$LAB_ID_HOST"
curl_common=(-s --cacert "$S/lab-ca.crt" --resolve "$LAB_ID_HOST:443:$LAB_VM_IP")
umask 077; cfg=$(mktemp); trap 'rm -f "$cfg"' EXIT
printf 'data = "client_id=admin-cli&grant_type=password&username=kcadmin"\ndata-urlencode = "password=%s"\n' "$(cat "$S/keycloak-admin-pw")" > "$cfg"
token=$(curl "${curl_common[@]}" --config "$cfg" "$base/realms/master/protocol/openid-connect/token" | jq -r .access_token)
[ -n "$token" ] && [ "$token" != null ] || { echo "kc.sh: no admin token" >&2; exit 1; }
args=(-X "$method" -H "Authorization: Bearer $token" -H "Content-Type: application/json" -w '\nHTTP %{http_code}\n')
[ -n "$body" ] && args+=(--data-binary "@$body")
curl "${curl_common[@]}" "${args[@]}" "$base/admin/realms$path" | awk 'prev!="" {print prev} {prev=$0} END {print prev > "/dev/stderr"}'
