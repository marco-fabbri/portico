#!/usr/bin/env bash
# Milestone 2a acceptance (write-up 11): create a tenant through Portico's
# admin API on NTNX-01, check it from Prism Central as the service account,
# create its first user, disable and enable it; with --delete, remove it
# with its project and verify nothing is left (and _internal unchanged).
# Prints one line per check: got (expected). Exit 1 on any mismatch.
#   source lab/env.sh && lab/tenants/acceptance.sh [--delete]
# Tenant slug "tenant-b" → project portico-lab-tenant-b (lab prefix) and
# Keycloak/SAML group tenant-tenant-b (derived name, not pretty).
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}" "${LAB_VM_IP:?}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh
# Prism Central reads as the service account when its key file exists on
# the Mac; after the 2e bootstrap the key lives in the store only and the
# reads fall back to the admin credentials of lab/env.sh.
if [ -s "$S/portico-sa-key" ]; then export PC_API_KEY; PC_API_KEY=$(cut -d= -f2- "$S/portico-sa-key" | tr -d '\r\n'); fi
# Against the appliance (Milestone 3) the names are the definitive ones: LAB_PROJECT_PREFIX=portico, LAB_REALM=portico, LAB_TENANT_SLUG=acme.
PREFIX="${LAB_PROJECT_PREFIX:-portico-lab}"; REALM="${LAB_REALM:-portico-lab}"
slug="${LAB_TENANT_SLUG:-tenant-b}"; project="$PREFIX-$slug"; group="tenant-$slug"; email="${LAB_TENANT_EMAIL:-carol@lab.portico.test}"
umask 077; tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail=0
line() { local ok=ok; [ "$2" = "$3" ] || { ok=FAIL; fail=1; }; printf '%-4s %-66s -> %s (%s)\n' "$ok" "$1" "$2" "$3"; }
api() { local code; code=$(lab/tenants/api.sh "$@" 2>&1 >"$tmp/out" | tail -1); echo "${code#HTTP }"; }
wait_status() {  # wait_status WANT MAX_SECONDS → prints the final status
  local waited=0 st
  while :; do
    api GET "/api/v1/tenants/$slug" >/dev/null; st=$(jq -r '.status // "gone"' "$tmp/out" 2>/dev/null)
    case "$st" in "$1"|failed|gone) echo "$st"; return ;; esac
    [ "$waited" -ge "$2" ] && { echo "timeout($st)"; return; }
    sleep 10; waited=$((waited + 10))
  done
}
snap_internal() { lab/pc.sh GET "$MD/config/projects" 2>/dev/null | jq -S 'del(.data[] | select(.name!="_internal")) | .data[0] | del(.modifiedTimestamp)' > "$1"; }

if [ "${1:-}" != "--delete" ]; then
  # 1. create
  # LAB_TENANT_SITES (comma-separated site ids) places the tenant on those
  # sites; empty leaves the choice to Portico (the only site).
  jq -n --arg s "$slug" --arg d "${LAB_TENANT_NAME:-Tenant B (lab)}" --arg c "${LAB_TENANT_CONTACT:-ops@lab.portico.test}" --arg sites "${LAB_TENANT_SITES:-}" \
    '{slug:$s, display_name:$d, contacts:$c} + (if $sites == "" then {} else {sites: ($sites | split(",") | map({site: .}))} end)' > "$tmp/create.json"
  code=$(api POST /api/v1/tenants "$tmp/create.json")
  [ "$code" = 409 ] && { echo "     tenant exists already, continuing"; code=202; }
  line "POST /api/v1/tenants ($slug)" "$code" 202
  # 2. wait
  start=$(date +%s)
  line "status after provisioning" "$(wait_status active 900)" active
  echo "     provisioning took $(( $(date +%s) - start )) s"
  api GET "/api/v1/tenants/$slug/steps" >/dev/null; jq -r '.[] | "     step \(.site)/\(.name): \(if .ok then "ok" else "FAILED" end) \(.detail)"' "$tmp/out"
  api GET "/api/v1/tenants/$slug" >/dev/null; cp "$tmp/out" "$tmp/tenant.json"
  # 3. visibility
  code=$(api GET "/api/v1/tenants/$slug/visibility")
  line "GET /visibility (every site)" "$code $(jq -r '[.sites[].ok] | all' "$tmp/out")" "200 true"
  # 4. Prism Central read-back as the service account
  pj=$(project_extid "$project")
  line "project $project exists in Prism Central" "$([ -n "$pj" ] && echo yes || echo no)" yes
  line "project extId matches the tenant's placement" "$([ "$pj" = "$(jq -r '.placements[0].project_ext_id' "$tmp/tenant.json")" ] && echo yes || echo no)" yes
  grp=$(user_group_extid "$group")
  n=$(lab/pc.sh GET "$IAM1/authz/role-memberships" 2>/dev/null | jq -r --arg g "$grp" --arg p "$pj" '[.data[]? | select(.identityExtId==$g and .projectExtId==$p)] | length')
  line "role memberships of $group in the project" "$n" 1
  shared=$(lab/pc.sh GET "$IAM1/authn/saml-identity-providers/$(saml_idp_extid)" 2>/dev/null | jq -r --arg p "$pj" '.data.sharedWithProjects // [] | index($p) != null')
  line "SAML IdP shared with the project" "$shared" true
  # 5. first user
  jq -n --arg e "$email" '{email:$e, first_name:"Carol", last_name:"Lab"}' > "$tmp/user.json"
  code=$(api POST "/api/v1/tenants/$slug/users" "$tmp/user.json")
  if [ "$code" = 201 ]; then jq -r .temporary_password "$tmp/out" > "$S/carol-tmp-pw"; echo "     temporary password saved to $S/carol-tmp-pw"; fi
  # A pre-existing user is not attached to the new group (Portico refuses
  # with 409): run --delete first so the user is recreated.
  line "POST /users ($email)" "$code" 201
  # 6. disable / enable
  code=$(api POST "/api/v1/tenants/$slug/disable"); line "POST /disable" "$code $(jq -r .status "$tmp/out")" "200 disabled"
  en=$(lab/saml/kc.sh GET "/$REALM/users?email=$email&exact=true" 2>/dev/null | jq -r '.[0].enabled')
  line "carol enabled in Keycloak while disabled" "$en" false
  code=$(api POST "/api/v1/tenants/$slug/enable"); line "POST /enable" "$code $(jq -r .status "$tmp/out")" "200 active"
  en=$(lab/saml/kc.sh GET "/$REALM/users?email=$email&exact=true" 2>/dev/null | jq -r '.[0].enabled')
  line "carol enabled in Keycloak after enable" "$en" true
  echo "browser check pending: log in as $email on https://$LAB_PRISM_HOST/ (then run with --delete)"
else
  snap_internal "$tmp/internal-before.json"
  code=$(api DELETE "/api/v1/tenants/$slug?delete_project=true")
  [ "$code" = 404 ] && { echo "     tenant already absent, cleaning the rest"; code=202; }
  line "DELETE ?delete_project=true" "$code" 202
  line "tenant row after deletion" "$(wait_status gone 900)" gone
  line "project $project in Prism Central after deletion" "$([ -z "$(project_extid "$project")" ] && echo absent || echo present)" absent
  line "SAML group $group after deletion" "$([ -z "$(user_group_extid "$group")" ] && echo absent || echo present)" absent
  kg=$(lab/saml/kc.sh GET "/$REALM/groups?search=$group&exact=true" 2>/dev/null | jq -r 'length')
  line "Keycloak group $group after deletion" "$kg" 0
  snap_internal "$tmp/internal-after.json"
  line "_internal project unchanged" "$(diff -q "$tmp/internal-before.json" "$tmp/internal-after.json" >/dev/null && echo yes || echo no)" yes
  # The test user is removed too, so the next run creates her again with a
  # fresh temporary password (Portico itself leaves tenant users in place).
  uid=$(lab/saml/kc.sh GET "/$REALM/users?email=$email&exact=true" 2>/dev/null | jq -r '.[0].id // empty')
  [ -n "$uid" ] && lab/saml/kc.sh DELETE "/$REALM/users/$uid" >/dev/null 2>&1
  line "Keycloak test user $email removed" "$(lab/saml/kc.sh GET "/$REALM/users?email=$email&exact=true" 2>/dev/null | jq -r 'length')" 0
  rm -f "$S/carol-tmp-pw" "$S/carol-pw"
fi
if [ $fail -eq 0 ]; then echo "PASS: every line matched"; else echo "FAIL: see lines marked FAIL" >&2; exit 1; fi
