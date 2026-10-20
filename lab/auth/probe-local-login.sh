#!/usr/bin/env bash
# Milestone 1 acceptance test: the local-login paths of Prism Central
# measured in docs/lab write-up 06 must be refused by Portico on the public
# name, the identity host must expose only the realm, and the internal
# pass-through must still accept local credentials.
# Prints one line per request: METHOD PATH -> got (expected). Exit 1 if any
# line differs.
#   lab/auth/probe-local-login.sh [BASE_URL]   default https://$LAB_PRISM_HOST
# Requires `source lab/env.sh` (admin password for the login attempts).
# LAB_REALM names the realm (default portico-lab). LAB_PASSTHROUGH=loopback
# (the appliance's default, passthrough_listen on 127.0.0.1) expects the
# internal pass-through NOT to answer on LAB_VM_IP:9440; the default,
# exposed, expects it to accept local credentials there.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_PRISM_HOST:?source lab/env.sh first}" "${LAB_ID_HOST:?}" "${LAB_VM_IP:?}" "${PC_PASS:?}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
base="${1:-https://$LAB_PRISM_HOST}"
realm="${LAB_REALM:-portico-lab}"
host="${base#https://}"; host="${host%%/*}"
C=(-s --cacert "$S/lab-ca.crt" --resolve "$host:443:$LAB_VM_IP" --resolve "$LAB_ID_HOST:443:$LAB_VM_IP" -o /dev/null)
umask 077; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
# Credentials go to curl through config files, never on the command line.
printf 'user = "admin:%s"\n' "$PC_PASS" > "$tmp/basic.cfg"
printf 'data-urlencode = "j_username=admin"\ndata-urlencode = "j_password=%s"\n' "$PC_PASS" > "$tmp/form.cfg"
printf '{"username":"admin","password":"%s"}' "$PC_PASS" > "$tmp/vc.json"
[ -s "$S/portico-sa-key" ] && printf 'header = "X-ntnx-api-key: %s"\n' "$(cut -d= -f2- "$S/portico-sa-key")" > "$tmp/apikey.cfg"

fail=0
check() {  # check METHOD LABEL EXPECTED_CODE [EXPECTED_LOCATION_PREFIX] -- curl args
  local method="$1" label="$2" want="$3" loc="" got
  shift 3
  if [ "$1" != "--" ]; then loc="$1"; shift; fi
  shift  # the "--"
  got=$(curl "${C[@]}" -w '%{http_code} %{redirect_url}' "$@")
  local code="${got%% *}" redirect="${got#* }" ok="ok"
  [ "$code" = "$want" ] || ok="FAIL"
  if [ -n "$loc" ] && [[ "$redirect" != "$loc"* ]]; then ok="FAIL"; fi
  [ "$ok" = ok ] || fail=1
  printf '%-4s %-6s %-82s -> %s (%s)\n' "$ok" "$method" "$label" "$code" "$want"
}

# Public name of Prism Central
check GET  "/console/ (no session)" 302 "$base/.portico/login?rd=" -- -H 'Accept: text/html' "$base/console/"
check POST "/api/iam/v4.1.b3/authn/login-requests/<id>/\$actions/validate-credentials (admin)" 403 -- \
  -X POST -H 'Content-Type: application/json' --data-binary "@$tmp/vc.json" "$base/api/iam/v4.1.b3/authn/login-requests/$(uuidgen | tr '[:upper:]' '[:lower:]')/\$actions/validate-credentials"
check POST "/PrismGateway/j_spring_security_check (admin form)" 403 -- -X POST --config "$tmp/form.cfg" "$base/PrismGateway/j_spring_security_check"
for p in /api/iam/v4.0/authn/users /api/nutanix/v3/users/me /PrismGateway/services/rest/v1/cluster /api/clustermgmt/v4.0/config/clusters; do
  check GET "$p (Basic admin)" 403 -- --config "$tmp/basic.cfg" "$base$p"
done
# Milestone 2c: the landing page and the SAML start exist only behind a session.
check GET  "/ui/iam/providers (no session)" 302 "$base/.portico/login?rd=" -- -H 'Accept: text/html' "$base/ui/iam/providers"
check GET  "/.portico/saml-start (no session)" 302 "$base/.portico/login?rd=" -- -H 'Accept: text/html' "$base/.portico/saml-start"
# Any other Authorization scheme is refused too (write-up 12: the console sends none).
check GET "/api/nutanix/v3/vms/list (Authorization: Bearer)" 403 -- -H 'Authorization: Bearer not-a-token' "$base/api/nutanix/v3/vms/list"
if [ -s "$tmp/apikey.cfg" ]; then
  check GET "/api/multidomain/v4.4.b1/config/projects (X-ntnx-api-key)" 403 -- --config "$tmp/apikey.cfg" "$base/api/multidomain/v4.4.b1/config/projects"
fi
check POST "/api/nutanix/v3/groups (no session, no credentials)" 401 -- -X POST -H 'Content-Type: application/json' -d '{}' "$base/api/nutanix/v3/groups"

# Logout: Portico sends the browser to Keycloak's end-session endpoint, which
# must accept the post-logout URI (400 "invalid redirect uri" otherwise).
logout_loc=$(curl "${C[@]}" -w '%{redirect_url}' "$base/.portico/logout")
check GET "/.portico/logout" 302 "https://$LAB_ID_HOST/realms/$realm/protocol/openid-connect/logout?" -- "$base/.portico/logout"
# Without a Keycloak session the end-session endpoint goes straight back to
# the Prism Central host (a browser with a session gets a confirmation page).
check GET "end-session URL from the logout redirect (through the identity host)" 302 "$base/" -- "$logout_loc"

# Public name of the identity provider
check GET "https://$LAB_ID_HOST/admin/master/console/" 403 -- "https://$LAB_ID_HOST/admin/master/console/"
check GET "https://$LAB_ID_HOST/realms/$realm/" 200 -- "https://$LAB_ID_HOST/realms/$realm/"

# Internal pass-through (lab certificate has no IP SAN: -k). On the
# appliance it is on the loopback: nothing answers on the VM's address.
if [ "${LAB_PASSTHROUGH:-exposed}" = loopback ]; then
  check GET "https://$LAB_VM_IP:9440/PrismGateway/services/rest/v1/cluster (Basic admin, pass-through on loopback)" 000 -- -k --connect-timeout 5 --config "$tmp/basic.cfg" "https://$LAB_VM_IP:9440/PrismGateway/services/rest/v1/cluster"
else
  check GET "https://$LAB_VM_IP:9440/PrismGateway/services/rest/v1/cluster (Basic admin, internal)" 200 -- -k --config "$tmp/basic.cfg" "https://$LAB_VM_IP:9440/PrismGateway/services/rest/v1/cluster"
fi

if [ $fail -eq 0 ]; then echo "PASS: every line matched"; else echo "FAIL: see lines marked FAIL" >&2; exit 1; fi
