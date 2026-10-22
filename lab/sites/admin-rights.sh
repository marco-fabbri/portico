#!/usr/bin/env bash
# Sites usability measurements (write-up 24, item 2): can the account that
# adds a site read its own rights through the v4 API, so Portico checks
# them before creating anything? Lab only, read-only, credentials from
# lab/env.sh (the lab admin; the service account when PC_API_KEY is set).
#   source lab/env.sh && lab/sites/admin-rights.sh [USERNAME]
# Prints: the HTTP code of a few "who am I" candidates; the user found by
# name; the authorization policies whose identities name that user (or
# all users) with their roles; and, per role, whether its operations
# cover users, roles and SAML identity providers.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
usage() { sed -n '6p' "$0" | sed 's/^# *//' >&2; }
case "${1:-}" in -h|--help) usage; exit 0 ;; esac
: "${PC_URL:?source lab/env.sh first}"
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh
umask 077; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
user="${1:-${PC_USER:-admin}}"; [ -n "${PC_API_KEY:-}" ] && user="${1:-portico}"
get() { lab/pc.sh GET "$1" >"$tmp/out" 2>"$tmp/code"; tail -1 "$tmp/code" | sed 's/HTTP //'; }
enc() { jq -rn --arg v "$1" '$v|@uri'; }

echo "== who-am-i candidates"
for p in "$IAM1/authn/users/\$me" "$IAM0/authn/users/\$me" "$IAM1/authn/users/current" "$IAM1/authz/authorization-policies?\$filter=$(enc "identities/any(i: i eq 'me')")"; do
  printf '%-90s %s\n' "$p" "$(get "$p")"
done

echo "== user $user"
code=$(get "$IAM0/authn/users?\$filter=$(enc "username eq '$user'")")
uid=$(jq -r '.data[0].extId // empty' "$tmp/out")
echo "list users: HTTP $code, extId ${uid:-not found}, type $(jq -r '.data[0].userType // "-"' "$tmp/out")"

echo "== authorization policies naming the user (or every user)"
code=$(get "$IAM0/authz/authorization-policies?\$limit=100")
echo "list policies: HTTP $code, $(jq -r '.metadata.totalAvailableResults // (.data|length) // 0' "$tmp/out") policies"
[ "$code" = 200 ] || exit 0
jq -r --arg u "${uid:-none}" --arg n "$user" '.data[] | select((.identities // []) | tostring | (contains($u) or contains($n) or contains("\"*\""))) | [.displayName, .role] | @tsv' "$tmp/out" > "$tmp/pols"
cat "$tmp/pols"

echo "== roles: operations on users, roles, SAML identity providers"
# All operations once (paged), then a local join: one call per operation
# took minutes.
: > "$tmp/allops"
for page in $(seq 0 40); do
  code=$(get "$IAM0/authz/operations?\$limit=100&\$page=$page")
  [ "$code" = 200 ] || break
  jq -r '.data[]? | [.extId, .displayName] | @tsv' "$tmp/out" >> "$tmp/allops"
  [ "$(jq '.data|length' "$tmp/out")" -lt 100 ] && break
done
echo "operations listed: $(wc -l <"$tmp/allops" | tr -d ' ')"
cut -f2 "$tmp/pols" | sort -u | while read -r r; do
  [ -n "$r" ] || continue
  code=$(get "$IAM0/authz/roles/$r")
  name=$(jq -r '.data.displayName // "?"' "$tmp/out")
  jq -r '.data.operations[]?' "$tmp/out" | sort > "$tmp/ops"
  sort "$tmp/allops" | join -t $'\t' "$tmp/ops" - | cut -f2 | grep -i -E 'user|role|identity provider|saml' > "$tmp/hits" || true
  printf '%s (HTTP %s): %s operations, %s on users/roles/IdPs\n' "$name" "$code" "$(wc -l <"$tmp/ops" | tr -d ' ')" "$(wc -l <"$tmp/hits" | tr -d ' ')"
  sed 's/^/    /' "$tmp/hits" | head -40
done
