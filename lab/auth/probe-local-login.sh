#!/usr/bin/env bash
# Probe the local-login paths of Prism Central through the public name
# (design 6.2). Prints one line per request: METHOD PATH -> HTTP code.
# Today (nginx stand-in, no policy) the real paths answer 200/302. Once the
# Portico proxy is in place every line must be 403: this script is the
# Milestone 1 acceptance test.
#   lab/auth/probe-local-login.sh [BASE_URL]   default https://$LAB_PRISM_HOST
# Requires `source lab/env.sh` (admin password for the login attempt).
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_PRISM_HOST:?source lab/env.sh first}" "${LAB_VM_IP:?}" "${PC_PASS:?}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
base="${1:-https://$LAB_PRISM_HOST}"
host="${base#https://}"; host="${host%%/*}"
C=(-s --cacert "$S/lab-ca.crt" --resolve "$host:443:$LAB_VM_IP")
umask 077; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
cj="$tmp/cookies"
line() { printf '%-6s %-78s -> %s\n' "$1" "$2" "$3"; }

# 1. Console redirect chain: sets the iam-sessionid cookie (the login request id).
code=$(curl "${C[@]}" -c "$cj" -b "$cj" -o /dev/null -w '%{http_code}' "$base/console/"); line GET /console/ "$code"
loc=$(curl "${C[@]}" -c "$cj" -b "$cj" -D - -o /dev/null "$base/console/" | grep -i '^location:' | awk '{print $2}' | tr -d '\r')
code=$(curl "${C[@]}" -c "$cj" -b "$cj" -o /dev/null -w '%{http_code}' "$base$loc"); line GET "${loc%%\?*}?..." "$code"
sid=$(awk '!/^#/ && $6=="iam-sessionid" {print $7}' "$cj")

# 2. Local login with the form credentials (what the IAM login page does).
printf '{"username":"admin","password":"%s"}' "$PC_PASS" > "$tmp/vc.json"
p="/api/iam/v4.1.b3/authn/login-requests/${sid:-no-session}/\$actions/validate-credentials"
code=$(curl "${C[@]}" -c "$cj" -b "$cj" -o "$tmp/vb" -w '%{http_code}' -X POST -H 'Content-Type: application/json' --data-binary "@$tmp/vc.json" "$base$p")
line POST "/api/iam/v4.1.b3/authn/login-requests/<iam-sessionid>/\$actions/validate-credentials" "$code $(jq -r '.data.authCodeUri // empty' "$tmp/vb" 2>/dev/null | cut -c1-60)"

# 3. Legacy form login (pre-IAM).
code=$(curl "${C[@]}" -o /dev/null -w '%{http_code}' -X POST --data-urlencode "j_username=admin" --data-urlencode "j_password=$PC_PASS" "$base/PrismGateway/j_spring_security_check"); line POST /PrismGateway/j_spring_security_check "$code"

# 4. Basic auth on APIs of three generations.
for p in /api/iam/v4.0/authn/users /api/nutanix/v3/users/me /PrismGateway/services/rest/v1/cluster /api/clustermgmt/v4.0/config/clusters; do
  code=$(curl "${C[@]}" -o /dev/null -w '%{http_code}' -u "admin:$PC_PASS" "$base$p"); line "GET" "$p (Basic admin)" "$code"
done

# 5. API key of the service account (must also be blocked on the public path).
if [ -s "$S/portico-sa-key" ]; then
  code=$(curl "${C[@]}" -o /dev/null -w '%{http_code}' -H "X-ntnx-api-key: $(cut -d= -f2- "$S/portico-sa-key")" "$base/api/multidomain/v4.4.b1/config/projects"); line GET "/api/multidomain/v4.4.b1/config/projects (X-ntnx-api-key)" "$code"
fi
