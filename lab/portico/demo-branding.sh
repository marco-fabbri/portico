#!/usr/bin/env bash
# Demo branding (write-up 23): change some fields of the branding through
# the admin API, keep the rest, and check what the tenants then get. Lab only.
#   source lab/env.sh && lab/portico/demo-branding.sh [--logo PNG|SVG] [--arch] [--no-logo]
#     [--msp-name T] [--header-text T] [--tagline T] [--background C] [--text C] [--accent C]
#   source lab/env.sh && lab/portico/demo-branding.sh --restore FILE
# Before a change the current branding is saved to
# $PORTICO_SECRETS/branding-before-<UTC timestamp>.json (0600, outside the
# repository); --restore FILE puts such a file back. Checks, one line each,
# got (expected): HTTP 200; the fields not given unchanged; the login page
# shows the header text (or none); the theme CSS holds the three colours;
# /.portico/logo is the uploaded file, the arch, or 404. A readability
# warning is printed and does not fail the run. Exit 1 on any mismatch.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
usage() { sed -n '4,6p' "$0" | sed 's/^# *//' >&2; }
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
logo=; logo_mode=; restore=; given=()
umask 077; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
echo '{}' > "$tmp/set.json"
# setf KEY VALUE: the field KEY takes the string VALUE.
setf() { jq --arg k "$1" --arg v "$2" '.[$k]=$v' "$tmp/set.json" > "$tmp/set.next" && mv "$tmp/set.next" "$tmp/set.json"; given+=("$1"); }
while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --logo) logo="${2:?--logo needs a PNG or SVG file}"; logo_mode="${logo_mode:+x}file"; shift 2 ;;
    --arch) logo_mode="${logo_mode:+x}arch"; shift ;;
    --no-logo) logo_mode="${logo_mode:+x}none"; shift ;;
    --msp-name) setf msp_name "${2?}"; shift 2 ;;
    --header-text) setf header_text "${2?}"; shift 2 ;;
    --tagline) setf tagline "${2?}"; shift 2 ;;
    --background) setf background_color "${2:?}"; shift 2 ;;
    --text) setf text_color "${2:?}"; shift 2 ;;
    --accent) setf accent_color "${2:?}"; shift 2 ;;
    --restore) restore="${2:?--restore needs a file}"; shift 2 ;;
    *) usage; exit 2 ;;
  esac
done
case "$logo_mode" in x*) echo "demo-branding.sh: --logo, --arch and --no-logo are exclusive" >&2; exit 2 ;; esac
if [ -n "$restore" ] && { [ -n "$logo_mode" ] || [ ${#given[@]} -gt 0 ]; }; then echo "demo-branding.sh: --restore takes no other option" >&2; exit 2; fi
if [ -z "$restore" ] && [ -z "$logo_mode" ] && [ ${#given[@]} -eq 0 ]; then usage; exit 2; fi
: "${LAB_VM_IP:?source lab/env.sh first}" "${LAB_PRISM_HOST:?}" "${LAB_ID_HOST:?}"
fail=0
line() { local ok=ok; [ "$2" = "$3" ] || { ok=FAIL; fail=1; }; printf '%-4s %-52s -> %s (%s)\n' "$ok" "$1" "$2" "$3"; }
C=(-s --cacert "$S/lab-ca.crt" --resolve "$LAB_PRISM_HOST:443:$LAB_VM_IP" --resolve "$LAB_ID_HOST:443:$LAB_VM_IP")
FIELDS='["msp_name","header_text","tagline","background_color","text_color","accent_color","portico_logo","logo_type","logo_data","icon_data","min_display_ms"]'

lab/tenants/api.sh GET /api/v1/settings/branding > "$tmp/before.json" 2>/dev/null || { echo "demo-branding.sh: cannot read the branding" >&2; exit 1; }
backup="$S/branding-before-$(date -u +%Y%m%dT%H%M%SZ).json"
jq 'del(.warning)' "$tmp/before.json" > "$backup" || exit 1
echo "saved the current branding to $backup"

if [ -n "$restore" ]; then
  jq 'del(.warning)' "$restore" > "$tmp/put.json" || { echo "demo-branding.sh: $restore is not JSON" >&2; exit 1; }
  cp "$tmp/put.json" "$tmp/want.json"; keep=   # a restore gives every field
else
  case "$logo_mode" in
    file)
      case "$logo" in
        *.png|*.PNG) ctype=image/png ;;
        *.svg|*.SVG) ctype=image/svg+xml ;;
        *) echo "demo-branding.sh: --logo takes a .png or .svg file" >&2; exit 2 ;;
      esac
      base64 < "$logo" | tr -d '\n' > "$tmp/logo.b64" || exit 1
      jq --arg t "$ctype" --rawfile d "$tmp/logo.b64" '.portico_logo=false | .logo_type=$t | .logo_data=$d' "$tmp/set.json" > "$tmp/set.next" ;;
    arch) jq '.portico_logo=true | .logo_type="" | .logo_data=""' "$tmp/set.json" > "$tmp/set.next" ;;
    none) jq '.portico_logo=false | .logo_type="" | .logo_data=""' "$tmp/set.json" > "$tmp/set.next" ;;
    *) cp "$tmp/set.json" "$tmp/set.next" ;;
  esac
  mv "$tmp/set.next" "$tmp/set.json"
  jq -s '(.[0] | del(.warning)) * .[1]' "$tmp/before.json" "$tmp/set.json" > "$tmp/put.json"
  cp "$tmp/put.json" "$tmp/want.json"
  keep=$(jq -c --argjson f "$FIELDS" 'keys as $k | $f - $k' "$tmp/set.json")
fi

code=$(lab/tenants/api.sh PUT /api/v1/settings/branding "$tmp/put.json" 2>&1 >"$tmp/after.json" | tail -1)
line "PUT /api/v1/settings/branding" "${code#HTTP }" 200
warning=$(jq -r '.warning // empty' "$tmp/after.json")
[ -n "$warning" ] && echo "warning: $warning"
# The fields not given keep their values; the given ones take what was passed.
sum() { jq -c --argjson f "$1" '[$f[] as $k | {($k): .[$k]}] | add' "$2" | shasum | cut -c1-12; }
[ -n "$keep" ] && line "fields not given unchanged" "$(sum "$keep" "$tmp/after.json")" "$(sum "$keep" "$tmp/before.json")"
line "fields given saved as passed" "$(sum "$FIELDS" "$tmp/after.json")" "$(sum "$FIELDS" "$tmp/want.json")"

# The login page: the header text Keycloak shows, or none. Portico's
# header text is the one typed, else the MSP name when there is no logo.
want_text=$(jq -r 'if .header_text != "" then .header_text elif (.portico_logo or .logo_data != "") then "(none)" else .msp_name end' "$tmp/after.json")
curl "${C[@]}" -L -c "$tmp/jar" -b "$tmp/jar" -H 'Accept: text/html' -o "$tmp/login.html" "https://$LAB_PRISM_HOST/"
got_text=$(python3 -c 'import sys,re,html; m=re.search(r"class=\"portico-text\">(.*?)</span>", open(sys.argv[1]).read()); print(html.unescape(m.group(1)) if m else "(none)")' "$tmp/login.html")
line "login page header text" "$got_text" "$want_text"
css=$(grep -o 'href="[^"]*/css/portico.css"' "$tmp/login.html" | head -1 | sed 's/^href="//; s/"$//')
if [ -n "$css" ]; then
  curl "${C[@]}" -o "$tmp/portico.css" "https://$LAB_ID_HOST$css"
  for k in background_color text_color accent_color; do
    c=$(jq -r ".$k" "$tmp/after.json")
    has=no; grep -qiF "$c" "$tmp/portico.css" && has=yes
    line "theme CSS holds $k $c" "$has" yes
  done
else
  line "login page links portico.css" none portico.css
fi

# /.portico/logo on the Prism Central name.
lcode=$(curl "${C[@]}" -o "$tmp/logo" -w '%{http_code} %{content_type}' "https://$LAB_PRISM_HOST/.portico/logo")
if [ "$(jq -r '.logo_data' "$tmp/after.json")" != "" ]; then
  line "/.portico/logo" "$lcode" "200 $(jq -r '.logo_type' "$tmp/after.json")"
  line "/.portico/logo is the uploaded file" "$(shasum < "$tmp/logo" | cut -c1-12)" "$(jq -r '.logo_data' "$tmp/after.json" | base64 -d | shasum | cut -c1-12)"
elif [ "$(jq -r '.portico_logo' "$tmp/after.json")" = true ]; then
  line "/.portico/logo" "$lcode" "200 image/svg+xml"
  tc=$(jq -r '.text_color' "$tmp/after.json")
  arch=no; grep -qiF "fill=\"$tc\"" "$tmp/logo" && arch=yes
  line "/.portico/logo is the arch in $tc" "$arch" yes
else
  line "/.portico/logo" "${lcode%% *}" 404
fi
[ "$fail" -eq 0 ] && echo "restore with: lab/portico/demo-branding.sh --restore $backup"
exit "$fail"
