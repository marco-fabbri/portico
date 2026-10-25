#!/usr/bin/env bash
# Milestone 4 acceptance (write-up 21) on the appliance with one site:
# the single-Prism Central store became site LAB_SITE_ID, the tenant kept
# its project, the site is renamed to LAB_SITE_CODE, a tenant user reaches
# the console without the site page, maintenance stops new entries only,
# "disconnect" ends the routing of the sessions still on the site, and
# the local-login paths stay refused (with LAB_REALM and, on the
# appliance, LAB_PASSTHROUGH=loopback). Lab only.
# Prints one line per check: got (expected). Exit 1 on any mismatch.
#   source lab/env.sh && lab/sites/acceptance.sh [--with-tenant]
# --with-tenant also runs lab/tenants/acceptance.sh on the site (creates,
# checks and deletes the tenant LAB_NEW_TENANT, default "beta").
# The site is renamed with Apply: Portico restarts and every tenant
# session ends, and the disconnect step cuts every session on the site,
# a browser of yours included. Needs the probe user of LAB_TENANT_SLUG
# (lab/sites/tenant-login.sh --ensure-probe SLUG).
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}" "${LAB_VM_IP:?}" "${LAB_PRISM_HOST:?}"
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh
site="${LAB_SITE_ID:-site-1}"; code_want="${LAB_SITE_CODE:-AZ-IT-MI1}"
slug="${LAB_TENANT_SLUG:-acme}"; prefix="${LAB_PROJECT_PREFIX:-portico}"
probe="portico-probe-$slug@portico.test"
with_tenant=0; [ "${1:-}" = "--with-tenant" ] && with_tenant=1
umask 077; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail=0
line() { local ok=ok; [ "$2" = "$3" ] || { ok=FAIL; fail=1; }; printf '%-4s %-62s -> %s (%s)\n' "$ok" "$1" "$2" "$3"; }
api() { local code; code=$(lab/tenants/api.sh "$@" 2>&1 >"$tmp/out" | tail -1); echo "${code#HTTP }"; }
# console JAR: the console page with an existing session; prints the
# status and whether the body is the maintenance page.
console() {
  local c
  c=$(curl -sk -b "$1" -c "$1" --resolve "$LAB_PRISM_HOST:443:$LAB_VM_IP" -H 'Accept: text/html' -o "$tmp/console" -w '%{http_code}' "https://$LAB_PRISM_HOST/console/")
  grep -q 'is in maintenance' "$tmp/console" && c="$c maintenance-page"
  echo "$c"
}
# login N: a fresh login of the probe user; prints the final status, a
# "site-page" hop if the site page was shown, and "maintenance-page".
login() {
  local d="$tmp/login$1" out
  lab/sites/tenant-login.sh "$slug" --keep-jar "$d" > "$d.txt" 2>&1
  out=$(sed -n 's/^final: \([0-9]*\).*/\1/p' "$d.txt")
  grep -q '/\.portico/sites' "$d.txt" && out="$out site-page"
  grep -lq 'is in maintenance' "$d"/hop-*.html 2>/dev/null && out="$out maintenance-page"
  echo "${out:-none}"
}
site_json() { api GET /api/v1/sites >/dev/null; jq --arg s "$site" '.sites[] | select(.id==$s)' "$tmp/out"; }

# 1. The migrated site and the tenant's placement.
site_json > "$tmp/site.json"
line "GET /sites: $site exists, key set" "$(jq -r '"\(.id) \(.api_key_set)"' "$tmp/site.json")" "$site true"
line "GET /sites: $slug placed on $site" "$(jq -r --arg t "$slug" '.tenants | index($t) != null' "$tmp/site.json")" "true"
api GET "/api/v1/tenants/$slug" >/dev/null
jq --arg s "$site" '.placements[] | select(.site==$s)' "$tmp/out" > "$tmp/placement.json"
line "placement on $site: status, project" "$(jq -r '"\(.status) \(.project_id)"' "$tmp/placement.json")" "active $prefix-$slug"
pid=$(jq -r '.project_ext_id' "$tmp/placement.json")
line "the placement's project in Prism Central" "$(project_extid "$prefix-$slug")" "$pid"

# 2. Rename the code, Apply, wait for the restart.
if [ "$(jq -r .code "$tmp/site.json")" != "$code_want" ]; then
  jq --arg c "$code_want" '{code: $c, name, order, url, tls_verify, ca_pem, saml_idp_name, cluster_ext_id, storage_container_ext_id}' "$tmp/site.json" > "$tmp/put.json"
  line "PUT /sites/$site code $code_want" "$(api PUT "/api/v1/sites/$site" "$tmp/put.json") $(jq -r .code "$tmp/out")" "200 $code_want"
  line "POST /settings/apply" "$(api POST /api/v1/settings/apply)" "202"
  sleep 5
  for _ in $(seq 1 40); do [ "$(api GET /api/v1/sites)" = 200 ] && break; sleep 3; done
fi
line "after Apply: code, status" "$(site_json | jq -r '"\(.code) \(.status)"')" "$code_want available"

# 3. One site: the login goes straight to the console.
lab/sites/tenant-login.sh --ensure-probe "$slug" > "$tmp/ensure.txt" 2>&1 || cat "$tmp/ensure.txt"
line "login of $probe" "$(login 1)" "200"
jar1="$tmp/login1/jar"

# 4. Maintenance: new entries refused, sessions on the site listed and kept.
line "PUT /sites/$site/maintenance on" "$(api PUT "/api/v1/sites/$site/maintenance" <(echo '{"maintenance":true}')) $(jq -r .status "$tmp/out")" "200 maintenance"
api GET "/api/v1/sites/$site/sessions" >/dev/null; cp "$tmp/out" "$tmp/sessions.json"
# A probe session of an earlier run may still be listed: at least one.
line "sessions: the probe session is active" "$(jq -r --arg e "$probe" '[.sessions[] | select(.email==$e and .state=="active")] | length >= 1' "$tmp/sessions.json")" "true"
line "a new login during maintenance" "$(login 2)" "503 maintenance-page"
line "the session already on the site keeps working" "$(console "$jar1")" "200"
n=$(jq -r '.sessions | length' "$tmp/sessions.json")
line "POST /sites/$site/sessions/disconnect" "$(api POST "/api/v1/sites/$site/sessions/disconnect") $(jq -r .disconnected "$tmp/out")" "200 $n"
line "the disconnected session's next request" "$(console "$jar1")" "503 maintenance-page"
line "PUT /sites/$site/maintenance off" "$(api PUT "/api/v1/sites/$site/maintenance" <(echo '{"maintenance":false}')) $(jq -r .status "$tmp/out")" "200 available"
line "a login after maintenance" "$(login 3)" "200"

# 5. The local-login paths on the public name.
if lab/auth/probe-local-login.sh > "$tmp/local.txt" 2>&1; then r=pass; else r=fail; fi
line "lab/auth/probe-local-login.sh ($(grep -c . "$tmp/local.txt") requests)" "$r" "pass"
[ "$r" = pass ] || cat "$tmp/local.txt"

# 6. A tenant created, checked and deleted on the site.
if [ $with_tenant -eq 1 ]; then
  export LAB_TENANT_SITES="$site" LAB_TENANT_SLUG="${LAB_NEW_TENANT:-beta}" LAB_PROJECT_PREFIX="$prefix"
  export LAB_TENANT_EMAIL="$LAB_TENANT_SLUG-user@$LAB_TENANT_SLUG.example"
  if lab/tenants/acceptance.sh && lab/tenants/acceptance.sh --delete; then r=pass; else r=fail; fi
  line "lab/tenants/acceptance.sh on $site" "$r" "pass"
fi
exit $fail
