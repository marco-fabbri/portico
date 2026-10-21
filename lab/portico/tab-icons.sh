#!/usr/bin/env bash
# Tab icons (write-up 22): the icons Portico serves on both public names
# and on the admin listener, and the favicon of Keycloak's login page,
# which Portico's theme writes. Lab only. Prints one line per check:
# got (expected). Exit 1 on any mismatch.
#   source lab/env.sh && lab/portico/tab-icons.sh [--set PNG | --remove] [--log MINUTES]
# --set PNG stores PNG as the MSP's tab icon, --remove drops it (the arch
# on the accent colour is back); both keep the rest of the branding.
# --log MINUTES lists, after the checks, the login pages and the icon
# requests of the last MINUTES from Portico's access log on the appliance
# (times, hosts, paths and status only), to see which loads of a browser
# fetched an icon. Needs LAB_SSH_USER for --log.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_VM_IP:?source lab/env.sh first}" "${LAB_PRISM_HOST:?}" "${LAB_ID_HOST:?}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
set_png=; remove=0; log=
while [ $# -gt 0 ]; do
  case "$1" in
    --set) set_png="${2:?--set needs a PNG file}"; shift 2 ;;
    --remove) remove=1; shift ;;
    --log) log="${2:?--log needs minutes}"; shift 2 ;;
    *) echo "usage: tab-icons.sh [--set PNG | --remove] [--log MINUTES]" >&2; exit 2 ;;
  esac
done
umask 077; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail=0
line() { local ok=ok; [ "$2" = "$3" ] || { ok=FAIL; fail=1; }; printf '%-4s %-62s -> %s (%s)\n' "$ok" "$1" "$2" "$3"; }
C=(-s --cacert "$S/lab-ca.crt" --resolve "$LAB_PRISM_HOST:443:$LAB_VM_IP" --resolve "$LAB_ID_HOST:443:$LAB_VM_IP" --resolve "$LAB_PRISM_HOST:8443:$LAB_VM_IP")
# get URL FILE: prints "status content-type cache-control" and saves the body.
get() { curl "${C[@]}" -D "$tmp/h" -o "$2" -w '%{http_code} %{content_type}' "$1"; printf ' %s' "$(tr -d '\r' < "$tmp/h" | sed -n 's/^[Cc]ache-[Cc]ontrol: //p')"; }
# ico FILE: "ico N" for an ICO with N images, "not-ico" otherwise.
ico() { python3 -c 'import sys,struct; d=open(sys.argv[1],"rb").read(6); r,t,n=struct.unpack("<HHH",d) if len(d)==6 else (1,0,0); print("ico %d" % n if (r,t)==(0,1) else "not-ico")' "$1"; }
# png FILE: "WxH" of a PNG, "not-png" otherwise.
png() { python3 -c 'import sys,struct; d=open(sys.argv[1],"rb").read(24); print("%dx%d" % struct.unpack(">II",d[16:24]) if d[:8]==b"\x89PNG\r\n\x1a\n" else "not-png")' "$1"; }

if [ -n "$set_png" ] || [ "$remove" = 1 ]; then
  lab/tenants/api.sh GET /api/v1/settings/branding > "$tmp/before.json" 2>/dev/null || { echo "tab-icons.sh: cannot read the branding" >&2; exit 1; }
  if [ -n "$set_png" ]; then
    jq --arg i "$(base64 < "$set_png" | tr -d '\n')" 'del(.warning) | .icon_data=$i' "$tmp/before.json" > "$tmp/put.json"
  else
    jq 'del(.warning) | .icon_data=""' "$tmp/before.json" > "$tmp/put.json"
  fi
  code=$(lab/tenants/api.sh PUT /api/v1/settings/branding "$tmp/put.json" 2>&1 >"$tmp/after.json" | tail -1)
  line "PUT /api/v1/settings/branding" "${code#HTTP }" 200
  line "warning on save" "$(jq -r '.warning // "none"' "$tmp/after.json")" none
  line "rest of the branding unchanged" "$(jq -c '{msp_name,header_text,tagline,background_color,text_color,accent_color,portico_logo,logo_type,logo_data,min_display_ms}' "$tmp/after.json" | shasum | cut -c1-12)" "$(jq -c '{msp_name,header_text,tagline,background_color,text_color,accent_color,portico_logo,logo_type,logo_data,min_display_ms}' "$tmp/before.json" | shasum | cut -c1-12)"
fi

for h in "$LAB_PRISM_HOST" "$LAB_ID_HOST"; do
  line "$h/.portico/favicon.ico" "$(get "https://$h/.portico/favicon.ico" "$tmp/fav-$h")" "200 image/x-icon no-cache"
  line "$h favicon.ico entries" "$(ico "$tmp/fav-$h")" "ico 3"
done
line "$LAB_PRISM_HOST/.portico/apple-touch-icon.png" "$(get "https://$LAB_PRISM_HOST/.portico/apple-touch-icon.png" "$tmp/touch")" "200 image/png no-cache"
line "apple-touch-icon size" "$(png "$tmp/touch")" 180x180
line "admin /static/favicon.ico" "$(get "https://$LAB_PRISM_HOST:8443/static/favicon.ico" "$tmp/admin.ico")" "200 image/x-icon public, max-age=86400"
line "admin favicon.ico entries" "$(ico "$tmp/admin.ico")" "ico 3"
line "admin /static/apple-touch-icon.png" "$(get "https://$LAB_PRISM_HOST:8443/static/apple-touch-icon.png" "$tmp/admin.png")" "200 image/png public, max-age=86400"
same=no; cmp -s "$tmp/admin.ico" "$tmp/fav-$LAB_PRISM_HOST" && same=yes
line "admin icon differs from the tenants' icon" "$same" no

# Keycloak's login page: the favicon its template links is the one
# Portico's theme wrote, i.e. the tenants' favicon.ico.
curl "${C[@]}" -L -c "$tmp/jar" -b "$tmp/jar" -H 'Accept: text/html' -o "$tmp/login.html" "https://$LAB_PRISM_HOST/"
href=$(grep -o '<link rel="icon" href="[^"]*"' "$tmp/login.html" | head -1 | sed 's/.*href="//; s/"$//')
line "login page links an icon" "$([ -n "$href" ] && echo "${href##*/}" || echo none)" favicon.ico
if [ -n "$href" ]; then
  line "Keycloak's favicon" "$(get "https://$LAB_ID_HOST$href" "$tmp/kc.ico" | cut -d' ' -f1)" 200
  same=no; cmp -s "$tmp/kc.ico" "$tmp/fav-$LAB_PRISM_HOST" && same=yes
  line "Keycloak's favicon is Portico's" "$same" yes
fi

if [ -n "$log" ]; then
  : "${LAB_SSH_USER:?set LAB_SSH_USER for --log}"
  # shellcheck disable=SC2206 # LAB_SSH_OPTS is a list of options
  O=(-o BatchMode=yes -o ConnectTimeout=5 -o LogLevel=ERROR ${LAB_SSH_OPTS:-})
  echo "--- login pages and icon requests in the last $log min (UTC)"
  # shellcheck disable=SC2029 # $log expands here on purpose
  ssh "${O[@]}" "$LAB_SSH_USER@$LAB_VM_IP" "sudo -n journalctl -u portico --since '-$log min' --no-pager -o cat" \
    | jq -rR 'fromjson? | objects | select(.msg=="request" and (.path|test("icon|openid-connect/auth$"))) | "\(.time[11:22]) \(.status) \(.host)\(.path)"'
fi
exit "$fail"
