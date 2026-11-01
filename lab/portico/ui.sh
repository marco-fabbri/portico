#!/usr/bin/env bash
# Drive the admin UI of the appliance from the command line, as a browser
# would: sign in, read the page's CSRF token, post a form. Lab only; the
# admin API (lab/tenants/api.sh) covers what it covers, this is for the
# sections that exist only in the UI (the Routing section of a site's
# Networking card, write-up 39).
#   lab/portico/ui.sh GET  PATH
#   lab/portico/ui.sh POST PATH [FIELD=VALUE ...]       (PATH like /setup/sites/az-it-mi1/routing/move-router)
# The form fields are URL-encoded here. A POST carries the HX-Request
# header, so the answer is the card fragment the browser would swap in,
# and "HTTP <code>" goes to stderr. The break-glass password
# (PORTICO_SECRETS/portico-admin-pw) goes to curl through a file in a
# private temp dir, never on the command line. The session is kept in
# UI_COOKIES (default: a file in that temp dir, so every call signs in).
# The listener presents its certificate for LAB_PRISM_HOST; LAB_ADMIN_CA
# names the CA file (default PORTICO_SECRETS/lab-ca.crt).
set -euo pipefail
: "${LAB_VM_IP:?source lab/env.sh first}" "${LAB_PRISM_HOST:?}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
method="${1:?usage: ui.sh GET|POST PATH [FIELD=VALUE ...]}"; path="${2:?}"; shift 2
[ -s "$S/portico-admin-pw" ] || { echo "ui.sh: $S/portico-admin-pw missing" >&2; exit 1; }
umask 077
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
jar="${UI_COOKIES:-$tmp/cookies}"
base="https://$LAB_PRISM_HOST:8443"
CURL=(curl --silent --show-error --cacert "${LAB_ADMIN_CA:-$S/lab-ca.crt}" --resolve "$LAB_PRISM_HOST:8443:$LAB_VM_IP" --cookie "$jar" --cookie-jar "$jar")

# Sign in when the session is missing or expired.
signed_in() { "${CURL[@]}" -o /dev/null -w '%{http_code}' "$base/setup" | grep -q '^200$'; }
if ! signed_in; then
  pw=$(tr -d '\r\n' < "$S/portico-admin-pw" | jq -sRr @uri)
  printf 'user=admin&password=%s&next=/setup' "$pw" > "$tmp/login"
  unset pw
  code=$("${CURL[@]}" -o /dev/null -w '%{http_code}' --data "@$tmp/login" "$base/login")
  rm -f "$tmp/login"
  [ "$code" = 303 ] || { echo "ui.sh: sign-in failed (HTTP $code)" >&2; exit 1; }
fi

if [ "$method" = GET ]; then
  "${CURL[@]}" -w '\nHTTP %{http_code}\n' "$base$path" | awk 'prev!="" {print prev} {prev=$0} END {print prev > "/dev/stderr"}'
  exit 0
fi

# The CSRF token of the page the form lives on: the site page for its
# cards, the page itself otherwise.
page="$path"
case "$path" in /setup/sites/*/*) page="/setup/sites/$(cut -d/ -f4 <<<"$path")" ;; esac
csrf=$("${CURL[@]}" "$base$page" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//; s/"$//')
[ -n "$csrf" ] || { echo "ui.sh: no CSRF token on $page" >&2; exit 1; }
args=(--data-urlencode "_csrf=$csrf")
for f in "$@"; do args+=(--data-urlencode "$f"); done
"${CURL[@]}" -H 'HX-Request: true' "${args[@]}" -w '\nHTTP %{http_code}\n' "$base$path" | awk 'prev!="" {print prev} {prev=$0} END {print prev > "/dev/stderr"}'
