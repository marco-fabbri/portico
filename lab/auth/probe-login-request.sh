#!/usr/bin/env bash
# Login-request probe (write-up 13): the shape of the object the console
# reads and writes back to select the SAML provider, measured on the
# internal pass-through with a cookie jar and no credentials. Values
# longer than 12 characters are redacted. Lab only.
#   source lab/env.sh && lab/auth/probe-login-request.sh
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_VM_IP:?source lab/env.sh first}"
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh
base="https://$LAB_VM_IP:9440"
umask 077; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
jar="$tmp/jar"
curl -s -k -o /dev/null -L --max-redirs 5 -c "$jar" -b "$jar" "$base/console/"
sid=$(awk -F'\t' 'NF==7 && $6=="iam-sessionid"{print $7}' "$jar" | tail -1)
[ -n "$sid" ] || { echo "no iam-sessionid cookie"; exit 1; }
idp=$(saml_idp_extid); [ -n "$idp" ] || { echo "no lab IdP"; exit 1; }
code=$(curl -s -k -b "$jar" -H 'Accept: application/json' -o "$tmp/lr.json" -w '%{http_code}' "$base/api/iam/v4.1.b3/authn/login-requests/$sid")
echo "GET login-request: HTTP $code"
jq 'walk(if type=="string" and length>12 then "<redacted \(length)>" else . end)' "$tmp/lr.json"
# PUT the data object back with the provider selected: no If-Match, no
# NTNX-Request-Id (the console sent neither).
jq --arg i "$idp" '.data + {loginProviderId: $i} | del(."$objectType") | del(."$reserved")' "$tmp/lr.json" > "$tmp/put.json"
code=$(curl -s -k -b "$jar" -c "$jar" -H 'Content-Type: application/json' -H 'Accept: application/json' -X PUT --data-binary "@$tmp/put.json" -o "$tmp/put.out" -w '%{http_code}' "$base/api/iam/v4.1.b3/authn/login-requests/$sid")
echo "PUT login-request (loginProviderId set, no If-Match): HTTP $code"
[ "$code" = 200 ] || jq -c . "$tmp/put.out" 2>/dev/null | head -c 300
code=$(curl -s -k -b "$jar" -o /dev/null -w '%{http_code} %{redirect_url}' "$base/api/iam/authn/initiate-login")
echo "GET initiate-login: $code" | sed -E 's/(SAMLRequest|RelayState)=[^&]*/\1=<redacted>/g'
echo "--- /redirect with an external target (pass-through, no session):"
curl -s -k -o /dev/null -w 'HTTP %{http_code} -> %{redirect_url}\n' "$base/redirect?logout=https://example.invalid/"
