#!/usr/bin/env bash
# Milestone 2e acceptance (write-up 15), on the one-step site setup of
# the sites usability work (write-up 26): drive the bootstrap of a fresh
# appliance through Portico's admin API. Prints one line per check: got
# (expected). Exit 1 on any mismatch. The one-time credentials (Prism
# Central admin, Keycloak admin) are read from PORTICO_SECRETS into JSON
# bodies in a private temp dir with jq --rawfile, never on a command line
# and never written under the repository.
#   source lab/env.sh && lab/portico/bootstrap-acceptance.sh            full run on a fresh appliance
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}" "${LAB_VM_IP:?}" "${LAB_PRISM_HOST:?}" "${LAB_ID_HOST:?}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
REALM="${LAB_REALM:-portico}"
SERVICE_ACCOUNT="${LAB_SERVICE_ACCOUNT:-portico}"
IDP_NAME="${LAB_SAML_IDP:-portico-keycloak}"
PROJECT_PREFIX="${LAB_PROJECT_PREFIX:-portico}"
# The lab's Prism Central is one site (Milestone 4): code, id and name.
SITE_CODE="${LAB_SITE_CODE:-AZ-IT-MI1}"; SITE="$(tr '[:upper:]' '[:lower:]' <<<"$SITE_CODE")"; SITE_NAME="${LAB_SITE_NAME:-Milan 1}"
for f in prism-pw keycloak-admin-pw portico-admin-pw; do [ -s "$S/$f" ] || { echo "missing $S/$f" >&2; exit 1; }; done
umask 077; tmp=$(mktemp -d); trap 'rm -rf "${tmp:?}"' EXIT
fail=0
line() { local ok=ok; [ "$2" = "$3" ] || { ok=FAIL; fail=1; }; printf '%-4s %-70s -> %s (%s)\n' "$ok" "$1" "$2" "$3"; }
api() { local code; code=$(lab/tenants/api.sh "$@" 2>&1 >"$tmp/out" | tail -1); echo "${code#HTTP }"; }
field() { jq -r "$1" "$tmp/out" 2>/dev/null; }
t0() { date +%s; }

# 1. Setup mode on an empty store.
code=$(api GET /api/v1/settings)
line "GET settings on a fresh store" "$code mode=$(field .mode) stored=$(field .stored)" "200 mode=setup stored=false"
jq -n --arg p "$LAB_PRISM_HOST" --arg i "$LAB_ID_HOST" '{portal_host:$p, id_host:$i}' > "$tmp/hosts.json"
code=$(api PUT /api/v1/settings "$tmp/hosts.json")
line "PUT hosts (draft)" "$code stored=$(field .stored) complete=$(field .complete)" "200 stored=true complete=false"
code=$(api POST /api/v1/settings/apply)
line "apply on a draft" "$code" 409

# 2. Keycloak: realm, admin client, OIDC client from the one-time admin credentials.
jq -n --rawfile pw "$S/keycloak-admin-pw" --arg r "$REALM" '{admin_username:"kcadmin", admin_password:($pw|rtrimstr("\n")), realm:$r, display_name:"Portico MSP"}' > "$tmp/kc.json"
s=$(t0); code=$(api POST /api/v1/settings/bootstrap/keycloak "$tmp/kc.json"); d=$(( $(t0) - s ))
line "bootstrap keycloak (${d}s)" "$code ok=$(field .ok) created=$(field '.created|length') stored=$(field .stored)" "200 ok=true created=3 stored=true"
echo "     $(field .message)"

# 3. The tenants card, before any site exists.
jq -n --arg pp "$PROJECT_PREFIX" '{project_prefix:$pp, group_prefix:"tenant-", role:"Project Admin", operation_timeout:"15m"}' > "$tmp/tenants.json"
api GET /api/v1/settings >/dev/null
jq --slurpfile t "$tmp/tenants.json" '{portal_host, id_host, passthrough_site, keycloak:{realm:.keycloak.realm, admin_client_id:.keycloak.admin_client_id}, oidc:{issuer:.oidc.issuer, client_id:.oidc.client_id}, tenants:$t[0]}' "$tmp/out" > "$tmp/put.json"
code=$(api PUT /api/v1/settings "$tmp/put.json")
line "PUT tenants card" "$code missing=$(field '.missing|join(",")')" "200 missing=sites"

# 4. The site in one step (write-up 26): the certificate it presents is
#    read first, then the site is added with that fingerprint pinned and
#    the administrator's credentials, which set it up to the end:
#    service account and key, SAML trust both ways, placement defaults,
#    live check. The credentials are used for this run and not stored.
jq -n --arg u "$PC_URL" '{url:$u}' > "$tmp/inspect.json"
code=$(api POST /api/v1/sites/inspect "$tmp/inspect.json")
pin=$(field .fingerprint)
line "POST sites/inspect" "$code $([ -n "$pin" ] && [ "$pin" != null ] && echo fingerprint)" "200 fingerprint"
jq -n --rawfile pw "$S/prism-pw" --arg c "$SITE_CODE" --arg n "$SITE_NAME" --arg u "$PC_URL" --arg pin "$pin" --arg sa "$SERVICE_ACCOUNT" \
  '{code:$c, name:$n, order:1, url:$u, tls_pin:$pin, service_account:$sa, admin:{username:"admin", password:($pw|rtrimstr("\n"))}}' > "$tmp/site.json"
s=$(t0); code=$(api POST /api/v1/sites "$tmp/site.json"); d=$(( $(t0) - s ))
line "POST site $SITE_CODE in one step (${d}s)" "$code $(field .site.id) complete=$(field .complete) failed=$(field '[.steps[]?|select(.status=="failed")]|length')" "201 $SITE complete=true failed=0"
jq -r '.steps[]? | "     \(.name): \(.status) \(.message)"' "$tmp/out"
api GET "/api/v1/sites/$SITE" >/dev/null
line "GET site: service account, identity provider and defaults stored" "api_key_set=$(field .api_key_set) idp=$(field .saml_idp_name) defaults=$([ "$(field .cluster_ext_id)" != "" ] && [ "$(field .cluster_ext_id)" != null ] && echo set)" "api_key_set=true idp=$IDP_NAME defaults=set"

# 5. Complete, checks, apply.
code=$(api GET /api/v1/settings)
line "GET settings after the bootstraps" "$code complete=$(field .complete) missing=$(field '.missing|length')" "200 complete=true missing=0"
for target in keycloak oidc; do
  jq -n --arg t "$target" '{target:$t}' > "$tmp/check.json"
  code=$(api POST /api/v1/settings/check "$tmp/check.json")
  line "check $target" "$code ok=$(field .ok)" "200 ok=true"
done
echo '{}' > "$tmp/check.json"
code=$(api POST "/api/v1/sites/$SITE/check" "$tmp/check.json")
line "check site $SITE_CODE" "$code ok=$(field .ok)" "200 ok=true"
echo "     $(field .message)"
code=$(api POST /api/v1/settings/apply)
line "apply" "$code" 202
back=timeout
for _ in $(seq 1 60); do
  sleep 1
  if [ "$(curl -sk -o /dev/null -w '%{http_code}' "https://$LAB_VM_IP:8443/healthz" || true)" = 200 ]; then
    code=$(api GET /api/v1/settings 2>/dev/null); [ "$code" = 200 ] && [ "$(field .mode)" = running ] && { back=running; break; }
  fi
done
line "service back after apply" "$back" running

# 6. Branding (write-up 14), so the login page is branded again.
jq -n --rawfile logo lab/portico/portico-msp-logo.svg '{msp_name:"Portico MSP", header_text:"Portico MSP", tagline:"The single, authenticated entry point to your Nutanix services.", background_color:"#a73422", text_color:"#ffffff", accent_color:"#a73422", portico_logo:false, logo_type:"image/svg+xml", logo_data:($logo|@base64), min_display_ms:800}' > "$tmp/brand.json"
code=$(api PUT /api/v1/settings/branding "$tmp/brand.json")
line "PUT branding" "$code" 200

echo "next: lab/tenants/acceptance.sh (tenant tenant-b and its user), the browser check, then lab/tenants/acceptance.sh --delete"
if [ $fail -eq 0 ]; then echo PASS; else echo FAIL; exit 1; fi
