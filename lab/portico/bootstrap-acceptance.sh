#!/usr/bin/env bash
# Milestone 2e acceptance (write-up 15): drive the bootstrap of a fresh
# appliance through Portico's admin API on the lab VM. Prints one line per
# check: got (expected). Exit 1 on any mismatch. The one-time credentials
# (Prism Central admin, Keycloak admin) are read from ~/portico-secrets into
# JSON bodies in a private temp dir with jq --rawfile, never on a command
# line and never written under the repository.
#   source lab/env.sh && lab/portico/bootstrap-acceptance.sh            full run on a fresh appliance
#   source lab/env.sh && lab/portico/bootstrap-acceptance.sh --refusal-only   while the old IdP exists: the 409
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}" "${LAB_VM_IP:?}" "${LAB_PRISM_HOST:?}" "${LAB_ID_HOST:?}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
# The same run against the appliance VM (Milestone 3): other secrets
# directory, another service account and realm, since the lab VM's exist.
REALM="${LAB_REALM:-portico-lab}"
SERVICE_ACCOUNT="${LAB_SERVICE_ACCOUNT:-portico}"
IDP_NAME="${LAB_SAML_IDP:-portico-lab-keycloak}"
PROJECT_PREFIX="${LAB_PROJECT_PREFIX:-portico-lab}"
for f in prism-pw keycloak-admin-pw portico-admin-pw; do [ -s "$S/$f" ] || { echo "missing $S/$f" >&2; exit 1; }; done
umask 077; tmp=$(mktemp -d); trap 'rm -rf "${tmp:?}"' EXIT
fail=0
line() { local ok=ok; [ "$2" = "$3" ] || { ok=FAIL; fail=1; }; printf '%-4s %-70s -> %s (%s)\n' "$ok" "$1" "$2" "$3"; }
api() { local code; code=$(lab/tenants/api.sh "$@" 2>&1 >"$tmp/out" | tail -1); echo "${code#HTTP }"; }
field() { jq -r "$1" "$tmp/out" 2>/dev/null; }
t0() { date +%s; }

if [ "${1:-}" = "--refusal-only" ]; then
  # Review focus 3 on the real Prism Central: a foreign IdP is refused, not adopted.
  jq -n '{name:"portico-e2e"}' > "$tmp/saml.json"
  code=$(api POST /api/v1/settings/bootstrap/saml "$tmp/saml.json")
  line "bootstrap/saml with another name while portico-lab-keycloak exists" "$code $(field '.error' | grep -o 'portico-lab-keycloak' | head -1)" "409 portico-lab-keycloak"
  if [ $fail -eq 0 ]; then echo PASS; else echo FAIL; exit 1; fi
  exit
fi

# 1. Setup mode on an empty store.
code=$(api GET /api/v1/settings)
line "GET settings on a fresh store" "$code mode=$(field .mode) stored=$(field .stored)" "200 mode=setup stored=false"
jq -n --arg p "$LAB_PRISM_HOST" --arg i "$LAB_ID_HOST" '{prism_host:$p, id_host:$i}' > "$tmp/hosts.json"
code=$(api PUT /api/v1/settings "$tmp/hosts.json")
line "PUT hosts (draft)" "$code stored=$(field .stored) complete=$(field .complete)" "200 stored=true complete=false"
code=$(api POST /api/v1/settings/apply)
line "apply on a draft" "$code" 409

# 2. Keycloak: realm, admin client, OIDC client from the one-time admin credentials.
jq -n --rawfile pw "$S/keycloak-admin-pw" --arg r "$REALM" '{admin_username:"kcadmin", admin_password:($pw|rtrimstr("\n")), realm:$r, display_name:"Portico MSP"}' > "$tmp/kc.json"
s=$(t0); code=$(api POST /api/v1/settings/bootstrap/keycloak "$tmp/kc.json"); d=$(( $(t0) - s ))
line "bootstrap keycloak (${d}s)" "$code ok=$(field .ok) created=$(field '.created|length') stored=$(field .stored)" "200 ok=true created=3 stored=true"
echo "     $(field .message)"

# 3. Prism Central: service account, key, policy, verified with the new key.
jq -n --rawfile pw "$S/prism-pw" --arg u "$PC_URL" --arg sa "$SERVICE_ACCOUNT" '{username:"admin", password:($pw|rtrimstr("\n")), url:$u, tls_verify:false, service_account:$sa}' > "$tmp/pc.json"
s=$(t0); code=$(api POST /api/v1/settings/bootstrap/prism-central "$tmp/pc.json"); d=$(( $(t0) - s ))
line "bootstrap prism-central (${d}s)" "$code ok=$(field .ok) clusters=$(field '.clusters|length') stored=$(field .stored)" "200 ok=true clusters=1 stored=true"
echo "     $(field .message)"
cluster=$(field '.clusters[0].ext_id'); container=$(field '.containers[0].ext_id'); sa_ext=$(field .service_account_ext_id); key_name=$(field .key_name)
echo "     service account $sa_ext, key $key_name, expires $(field .key_expires)"

# 4. Tenants card.
jq -n --arg c "$cluster" --arg sc "$container" --arg pp "$PROJECT_PREFIX" '{project_prefix:$pp, group_prefix:"tenant-", role:"Project Admin", cluster_ext_id:$c, storage_container_ext_id:$sc, operation_timeout:"15m"}' > "$tmp/tenants.json"
api GET /api/v1/settings >/dev/null
jq --slurpfile t "$tmp/tenants.json" '{prism_host, id_host, prism_central:{url:.prism_central.url, tls_verify:.prism_central.tls_verify, saml_idp_name:.prism_central.saml_idp_name}, keycloak:{realm:.keycloak.realm, admin_client_id:.keycloak.admin_client_id}, oidc:{issuer:.oidc.issuer, client_id:.oidc.client_id}, tenants:$t[0]}' "$tmp/out" > "$tmp/put.json"
code=$(api PUT /api/v1/settings "$tmp/put.json")
line "PUT tenants card" "$code missing=$(field '.missing|join(",")')" "200 missing=prism_central.saml_idp_name"

# 5. SAML trust: the service account first, the admin credentials on a 403.
jq -n --arg n "$IDP_NAME" '{name:$n}' > "$tmp/saml.json"
s=$(t0); code=$(api POST /api/v1/settings/bootstrap/saml "$tmp/saml.json"); d=$(( $(t0) - s ))
used="service account"
if [ "$code" = 200 ] && [ "$(field .ok)" = false ] && grep -q "refused the service account" <<<"$(field .message)"; then
  echo "     service account refused: $(field .message)"
  jq -n --rawfile pw "$S/prism-pw" --arg n "$IDP_NAME" '{name:$n, username:"admin", password:($pw|rtrimstr("\n"))}' > "$tmp/saml.json"
  s=$(t0); code=$(api POST /api/v1/settings/bootstrap/saml "$tmp/saml.json"); d=$(( $(t0) - s ))
  used="admin credentials"
fi
line "bootstrap saml with the $used (${d}s)" "$code ok=$(field .ok) used_credentials=$(field .used_credentials) stored=$(field .stored)" "200 ok=true used_credentials=$([ "$used" = "service account" ] && echo false || echo true) stored=true"
echo "     $(field .message)"
echo "     idp $(field .idp_ext_id), entityID $(field .entity_id), ACS $(field .acs_url)"

# 6. Complete, checks, apply.
code=$(api GET /api/v1/settings)
line "GET settings after the bootstraps" "$code complete=$(field .complete) missing=$(field '.missing|length')" "200 complete=true missing=0"
for target in keycloak oidc prism_central; do
  jq -n --arg t "$target" '{target:$t}' > "$tmp/check.json"
  code=$(api POST /api/v1/settings/check "$tmp/check.json")
  line "check $target" "$code ok=$(field .ok)" "200 ok=true"
done
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

# 7. Branding (write-up 14), so the login page is branded again.
jq -n --rawfile logo lab/portico/portico-msp-logo.svg '{msp_name:"Portico MSP", tagline:"The single, authenticated entry point to your Nutanix services.", accent_color:"#a73422", logo_type:"image/svg+xml", logo_data:($logo|@base64), min_display_ms:800}' > "$tmp/brand.json"
code=$(api PUT /api/v1/settings/branding "$tmp/brand.json")
line "PUT branding" "$code" 200

echo "next: lab/tenants/acceptance.sh (tenant B, carol), the browser check, then lab/tenants/acceptance.sh --delete"
if [ $fail -eq 0 ]; then echo PASS; else echo FAIL; exit 1; fi
