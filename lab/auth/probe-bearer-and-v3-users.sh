#!/usr/bin/env bash
# Milestone 2a measurements deferred by the Milestone 1 review (write-up 10):
#   1. does Prism Central issue a token that a client can replay as
#      `Authorization: Bearer` without cookies?
#   2. does v3 `users/list` enumerate every account for a Project Admin?
# Both run on the internal pass-through (no Portico session needed: the
# question is what Prism Central accepts), as the local user
# portico-lab-bob (Project Admin of tenant A). Prints one line per probe:
# got (expected); the expected values are the ones measured on pc.7.6.0.6,
# so the script is also the re-measure procedure. Exit 1 on any mismatch.
#   lab/auth/probe-bearer-and-v3-users.sh
# Requires `source lab/env.sh` and ~/portico-secrets/bob-pw. Token and
# cookie values are never printed, only their names and lengths.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_VM_IP:?source lab/env.sh first}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
[ -s "$S/bob-pw" ] || { echo "missing $S/bob-pw" >&2; exit 1; }
base="https://$LAB_VM_IP:9440"
umask 077; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
C=(-s -k -o /dev/null)
Cb=(-s -k)   # when the body is needed
bobpw="$(tr -d '\r\n' < "$S/bob-pw")"
printf 'user = "portico-lab-bob:%s"\n' "$bobpw" > "$tmp/basic.cfg"
printf '{"username":"portico-lab-bob","password":"%s"}' "$bobpw" > "$tmp/vc.json"
unset bobpw

fail=0
line() {  # line LABEL GOT WANT
  local ok=ok; [ "$2" = "$3" ] || { ok=FAIL; fail=1; }
  printf '%-4s %-70s -> %s (%s)\n' "$ok" "$1" "$2" "$3"
}

# --- 1. local login through the pass-through, with a cookie jar
jar="$tmp/jar"
curl "${C[@]}" -L --max-redirs 5 -c "$jar" -b "$jar" "$base/console/"
sid=$(awk '$6=="iam-sessionid"{print $7}' "$jar" | tail -1)
line "login request id cookie (iam-sessionid) after GET /console/" "$([ -n "$sid" ] && echo present || echo absent)" present
code=$(curl "${Cb[@]}" -c "$jar" -b "$jar" -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  --data-binary "@$tmp/vc.json" -o "$tmp/vc.out" "$base/api/iam/v4.1.b3/authn/login-requests/$sid/\$actions/validate-credentials")
line "validate-credentials (bob)" "$code" 200
authcode=$(jq -r '.authCodeUri // .data.authCodeUri // empty' "$tmp/vc.out" 2>/dev/null)
line "authCodeUri in the response" "$([ -n "$authcode" ] && echo present || echo absent)" present
[ -n "$authcode" ] && curl "${C[@]}" -L --max-redirs 5 -c "$jar" -b "$jar" "$authcode"
# Session proof: a v3 call with the cookies only.
code=$(curl "${C[@]}" -b "$jar" -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{"kind":"vm"}' "$base/api/nutanix/v3/vms/list")
line "v3 vms/list with the session cookies" "$code" 200
# What the session consists of (names and value lengths only).
# HttpOnly cookies are stored as "#HttpOnly_<domain>" lines: keep them (7 fields).
names=$(awk -F'\t' 'NF==7 {printf "%s(%d) ", $6, length($7)}' "$jar")
echo "     cookies after login: ${names:-none}"
fields=$(jq -r 'paths(type=="string") | map(tostring) | join(".")' "$tmp/vc.out" 2>/dev/null | grep -iE 'token' | tr '\n' ' ')
echo "     token-like JSON fields in the validate-credentials response: ${fields:-none}"

# --- 2. replay every cookie value, and any token-like JSON field, as a Bearer token
bearer_ok=0
while IFS=$'\t' read -r _ _ _ _ _ name value; do
  [ -n "${value:-}" ] || continue
  printf 'header = "Authorization: Bearer %s"\n' "$value" > "$tmp/bearer.cfg"
  code=$(curl "${C[@]}" --config "$tmp/bearer.cfg" -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{"kind":"vm"}' "$base/api/nutanix/v3/vms/list")
  [ "$code" = 200 ] && bearer_ok=1
  echo "     Bearer <$name> on v3 vms/list, no cookies -> $code"
done < <(awk -F'\t' 'NF==7' "$jar")
for f in $fields; do
  v=$(jq -r --arg p "$f" 'getpath($p | split("."))' "$tmp/vc.out")
  printf 'header = "Authorization: Bearer %s"\n' "$v" > "$tmp/bearer.cfg"
  code=$(curl "${C[@]}" --config "$tmp/bearer.cfg" -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{"kind":"vm"}' "$base/api/nutanix/v3/vms/list")
  [ "$code" = 200 ] && bearer_ok=1
  echo "     Bearer <$f> on v3 vms/list, no cookies -> $code"
done
line "Bearer replay of any session value accepted (1 = yes)" "$bearer_ok" 0

# --- 3. user enumeration as bob (Basic), v3 and v4
code=$(curl "${Cb[@]}" --config "$tmp/basic.cfg" -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{"kind":"user"}' -o "$tmp/v3users.json" "$base/api/nutanix/v3/users/list")
total=$(jq -r '.metadata.total_matches // "n/a"' "$tmp/v3users.json" 2>/dev/null)
entities=$(jq -r '.entities | length' "$tmp/v3users.json" 2>/dev/null)
line "v3 users/list (Basic bob)" "$code" 200
# total_matches counts every account, entities holds only bob's own: v3 leaks the count, not the list.
line "v3 users/list entities returned (bob's own only = 1)" "$entities" 1
line "v3 users/list total_matches (count of all accounts: 9 on the lab)" "$total" 9
count=$(curl "${Cb[@]}" --config "$tmp/basic.cfg" "$base/api/iam/v4.0/authn/users" | jq -r '.data | length')
# Write-up 05 measured 7; alice's SAML user and the recreated service account make 9.
line "v4.0 authn/users count (Basic bob): every account" "$count" 9

if [ $fail -eq 0 ]; then echo "PASS: every line matched"; else echo "FAIL: see lines marked FAIL" >&2; exit 1; fi
