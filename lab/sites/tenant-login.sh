#!/usr/bin/env bash
# Sites measurements (write-up 20): a tenant user's login through the
# public path with curl only, for the cookie table of Prism Central. Lab
# only. Prints each hop's status and every Set-Cookie as
#   name path httpOnly sameSite maxAge size
# (never a value). The jar stays in --keep-jar DIR for the next steps.
#   source lab/env.sh && lab/sites/tenant-login.sh --ensure-probe acme
#   source lab/env.sh && lab/sites/tenant-login.sh acme [--keep-jar DIR]
# The probe user portico-probe-<slug>@lab.portico.test is a member of
# tenant-<slug>; its password is generated once into
# $PORTICO_SECRETS/probe-<slug>-pw (0600) and never printed.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_VM_IP:?source lab/env.sh first}" "${LAB_PRISM_HOST:?}" "${LAB_ID_HOST:?}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
REALM="${LAB_REALM:-portico}"
umask 077

if [ "${1:-}" = "--ensure-probe" ]; then
  slug="${2:?usage: tenant-login.sh --ensure-probe SLUG}"
  email="portico-probe-$slug@lab.portico.test"; pwf="$S/probe-$slug-pw"
  if [ ! -s "$pwf" ]; then
    # 24 characters from letters and digits, with each class present.
    LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 21 > "$pwf"; printf 'Aa1' >> "$pwf"
  fi
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  id=$(lab/saml/kc.sh GET "/$REALM/users?email=$email&exact=true" 2>/dev/null | jq -r '.[0].id // empty')
  if [ -z "$id" ]; then
    jq -n --arg e "$email" --rawfile p "$pwf" '{username:$e, email:$e, emailVerified:true, enabled:true, firstName:"Portico", lastName:"Probe",
      credentials:[{type:"password", value:($p|rtrimstr("\n")), temporary:false}]}' > "$tmp/user.json"
    lab/saml/kc.sh POST "/$REALM/users" "$tmp/user.json" >/dev/null 2>"$tmp/code"
    echo "create user: $(tail -1 "$tmp/code")"
    id=$(lab/saml/kc.sh GET "/$REALM/users?email=$email&exact=true" 2>/dev/null | jq -r '.[0].id // empty')
  else
    jq -n --rawfile p "$pwf" '{type:"password", value:($p|rtrimstr("\n")), temporary:false}' > "$tmp/pw.json"
    lab/saml/kc.sh PUT "/$REALM/users/$id/reset-password" "$tmp/pw.json" >/dev/null 2>"$tmp/code"
    echo "reset password: $(tail -1 "$tmp/code")"
  fi
  gid=$(lab/saml/kc.sh GET "/$REALM/groups?search=tenant-$slug&exact=true" 2>/dev/null | jq -r '.[0].id // empty')
  if [ -z "$id" ] || [ -z "$gid" ]; then echo "user or group tenant-$slug missing" >&2; exit 1; fi
  lab/saml/kc.sh PUT "/$REALM/users/$id/groups/$gid" >/dev/null 2>"$tmp/code"
  echo "probe user $email in tenant-$slug: $(tail -1 "$tmp/code")"
  exit
fi

slug="${1:?usage: tenant-login.sh SLUG [--keep-jar DIR]}"; keep=""
[ "${2:-}" = "--keep-jar" ] && keep="${3:?}"
email="portico-probe-$slug@lab.portico.test"; pwf="$S/probe-$slug-pw"
[ -s "$pwf" ] || { echo "no $pwf: run --ensure-probe $slug first" >&2; exit 1; }
if [ -n "$keep" ]; then mkdir -p "$keep"; chmod 700 "$keep"; d="$keep"; else d=$(mktemp -d); trap 'rm -rf "$d"' EXIT; fi
jar="$d/jar"; : > "$jar"
CURL=(curl -sk -b "$jar" -c "$jar" --resolve "$LAB_PRISM_HOST:443:$LAB_VM_IP" --resolve "$LAB_ID_HOST:443:$LAB_VM_IP" -H 'Accept: text/html,application/xhtml+xml')
hop=0
# fetch METHOD URL [curl args...]: one request, no redirect following;
# prints status and the Set-Cookie table; leaves body in $d/body and
# the Location in $loc.
fetch() {
  local method=$1 url=$2; shift 2
  hop=$((hop + 1))
  "${CURL[@]}" -X "$method" -D "$d/h" -o "$d/body" "$@" "$url"
  [ -n "$keep" ] && cp "$d/body" "$d/hop-$hop.html"
  local code; code=$(awk 'toupper($1) ~ /^HTTP/ {c=$2} END {print c}' "$d/h")
  loc=$(awk 'tolower($1)=="location:" {sub(/\r$/,""); print $2}' "$d/h" | tail -1)
  printf 'hop %2d %-4s %s %s\n' "$hop" "$method" "$code" "$(sed -E 's/([?&](code|state|session_code|SAMLRequest|RelayState|execution|tab_id|client_data)=)[^&]*/\1…/g' <<<"${url%%#*}")"
  grep -i '^set-cookie:' "$d/h" | while IFS= read -r line; do
    line=${line%$'\r'}
    c=${line#*: }; name=${c%%=*}; val=${c#*=}; val=${val%%;*}
    attrs=$(tr '[:upper:]' '[:lower:]' <<<"${c#*;}")
    path=$(grep -oE 'path=[^;]*' <<<"$attrs" | head -1); path=${path#path=}
    ss=$(grep -oE 'samesite=[^;]*' <<<"$attrs" | head -1); ss=${ss#samesite=}
    ma=$(grep -oE 'max-age=[^;]*' <<<"$attrs" | head -1); ma=${ma#max-age=}
    [ -z "$ma" ] && grep -q 'expires=' <<<"$attrs" && ma=expires
    printf '       set-cookie %-28s path=%-12s httponly=%-3s samesite=%-6s maxage=%-8s size=%s\n' "$name" "${path:--}" "$(grep -q httponly <<<"$attrs" && echo yes || echo no)" "${ss:--}" "${ma:-session}" "${#val}"
  done
}
unhtml() { python3 -c 'import html, sys; sys.stdout.write(html.unescape(sys.stdin.read().strip()))'; }
abs() { case "$1" in http*) echo "$1" ;; /*) echo "$2$1" ;; *) echo "$2/$1" ;; esac; }
base="https://$LAB_PRISM_HOST"

url="$base/"; method=GET; refetch=1
for _ in $(seq 1 25); do
  # refetch=0: a form was just submitted and answered a page, not a
  # redirect; read that page instead of fetching again.
  [ "$refetch" = 1 ] && fetch "$method" "$url"
  refetch=1
  if [ -n "$loc" ]; then
    case "$loc" in http*) url="$loc" ;; *) url=$(abs "$loc" "$(sed -E 's#^(https://[^/]+).*#\1#' <<<"$url")") ;; esac
    method=GET; continue
  fi
  # Keycloak login form.
  if grep -q 'id="kc-form-login"' "$d/body"; then
    action=$(grep -oE 'id="kc-form-login"[^>]*action="[^"]*"' "$d/body" | sed -E 's/.*action="([^"]*)".*/\1/; s/&amp;/\&/g')
    fetch POST "$action" --data-urlencode "username=$email" --data-urlencode "password@$pwf" --data-urlencode "credentialId="
    url=$(abs "$loc" "https://$LAB_ID_HOST"); method=GET
    [ -n "$loc" ] && continue
    echo "login refused (no redirect after the form)"; exit 1
  fi
  # Portico's landing page: meta refresh to the SAML start.
  if next=$(grep -oE 'url=/[^"]*' "$d/body" | head -1) && [ -n "$next" ]; then
    url="$base${next#url=}"; method=GET; continue
  fi
  # SAML auto-post form (Keycloak to Prism Central's ACS, or the request).
  if grep -q 'name="SAMLResponse"\|name="SAMLRequest"' "$d/body"; then
    action=$(grep -oE '<form[^>]*action="[^"]*"' "$d/body" | head -1 | sed -E 's/.*action="([^"]*)".*/\1/' | unhtml)
    args=()
    for f in SAMLResponse SAMLRequest RelayState; do
      # The form escapes its values as HTML (&#x2b; for +, &#x3d; for =).
      v=$(grep -oE "name=\"$f\"[^>]*value=\"[^\"]*\"" "$d/body" | head -1 | sed -E 's/.*value="([^"]*)".*/\1/' | unhtml)
      [ -n "$v" ] && args+=(--data-urlencode "$f=$v")
    done
    # Prism Central sends its request with a GET form (redirect binding);
    # Keycloak answers with a POST form to the ACS.
    fm=$(grep -oiE '<form[^>]*method="[^"]*"' "$d/body" | head -1 | sed -E 's/.*method="([^"]*)".*/\1/' | tr '[:lower:]' '[:upper:]')
    if [ "$fm" = GET ]; then fetch GET "$action" -G "${args[@]}"; else fetch POST "$action" "${args[@]}"; fi
    if [ -n "$loc" ]; then
      url=$(abs "$loc" "$(sed -E 's#^(https://[^/]+).*#\1#' <<<"$action")"); method=GET
    else
      url="$action"; refetch=0
    fi
    continue
  fi
  break
done
# The console itself, with the session the login made.
url="$base/console/"; fetch GET "$url"
last=$(awk 'toupper($1) ~ /^HTTP/ {c=$2} END {print c}' "$d/h")
echo "final: $last ${url%%\?*}"
echo "cookies in the jar for $LAB_PRISM_HOST:"
awk -v h="$LAB_PRISM_HOST" -F'\t' 'NF==7 && index($1,h) {printf "       %-28s path=%-12s secure=%s size=%d\n", $6, $3, $4, length($7)}' "$jar"
[ "$last" = 200 ] && grep -q '/console' <<<"$url"
