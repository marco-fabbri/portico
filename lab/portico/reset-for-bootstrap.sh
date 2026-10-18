#!/usr/bin/env bash
# Put the lab back to "fresh appliance" so the Milestone 2e bootstrap can
# be measured: Portico stopped and its store moved aside (master key kept),
# tenant A's SAML group and role membership removed and the IdP unshared
# (project and VM stay), the SAML IdP and the service account deleted in
# Prism Central, the Keycloak realm portico-lab deleted (alice, carol,
# groups, clients, theme setting), the three stale secret files removed on
# the Mac. Lab only; destructive; refuses to run without --yes.
#   source lab/env.sh && lab/portico/reset-for-bootstrap.sh --yes
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}" "${LAB_VM_IP:?}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
cat <<PLAN
reset-for-bootstrap.sh will, in this order:
  1. stop portico.service on $LAB_VM_IP and move /var/lib/portico/portico.db to portico.db.pre-2e-<timestamp> (master key kept)
  2. remove tenant A's role memberships and SAML group tenant-a, unshare the IdP from portico-lab-tenant-a (project and VM stay)
  3. delete the SAML identity provider portico-lab-keycloak in Prism Central
  4. delete the service account portico (and its keys) in Prism Central
  5. delete the Keycloak realm portico-lab (alice, carol, groups, clients, login theme setting)
  6. remove $S/portico-sa-key, portico-oidc-secret, portico-kc-admin-secret
PLAN
[ "${1:-}" = "--yes" ] || { echo "refusing: run again with --yes" >&2; exit 2; }
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=5 "lab@$LAB_VM_IP")
ts=$(date -u +%Y%m%d-%H%M%S)
tmp=$(mktemp -d); trap 'rm -rf "${tmp:?}"' EXIT

echo "1. store"
"${SSH[@]}" sudo bash -s -- "$ts" <<'REMOTE'
set -euo pipefail
systemctl stop portico || true
if [ -f /var/lib/portico/portico.db ]; then
  mv /var/lib/portico/portico.db "/var/lib/portico/portico.db.pre-2e-$1"
  rm -f /var/lib/portico/portico.db-wal /var/lib/portico/portico.db-shm
  echo "   store moved to /var/lib/portico/portico.db.pre-2e-$1"
else echo "   no store"; fi
ls -l /var/lib/portico/master.key
REMOTE

echo "2. tenant A memberships, SAML group, IdP sharing"
pj=$(project_extid portico-lab-tenant-a)
if [ -n "$pj" ]; then
  for rm in $(lab/pc.sh GET "$IAM1/authz/role-memberships" 2>/dev/null | jq -r --arg p "$pj" '.data[]? | select(.projectExtId==$p) | .extId'); do
    IF_MATCH="$(etag_of "$IAM1/authz/role-memberships/$rm")" lab/pc.sh DELETE "$IAM1/authz/role-memberships/$rm" 2>&1 >/dev/null | tr -d '\n'; echo " role membership $rm deleted"
  done
  idp=$(saml_idp_extid)
  if [ -n "$idp" ] && lab/pc.sh GET "$IAM1/authn/saml-identity-providers/$idp" 2>/dev/null | jq -e --arg p "$pj" '.data.sharedWithProjects // [] | index($p)' >/dev/null; then
    jq -n --arg p "$pj" '{projectExtId:$p}' > "$tmp/u.json"
    IF_MATCH="$(etag_of "$IAM1/authn/saml-identity-providers/$idp")" lab/pc.sh POST "$IAM1/authn/config/saml-identity-provider/$idp/\$actions/unshare" "$tmp/u.json" 2>/dev/null | jq -r '"   unshare: " + (.data.message // "done")'
  fi
else echo "   no project portico-lab-tenant-a"; fi
g=$(user_group_extid tenant-a)
if [ -n "$g" ]; then IF_MATCH="$(etag_of "$IAM0/authn/user-groups/$g")" lab/pc.sh DELETE "$IAM0/authn/user-groups/$g" 2>&1 >/dev/null | tr -d '\n'; echo " saml group tenant-a deleted"; fi

echo "3. SAML identity provider"
lab/saml/pc-idp.sh delete

echo "4. service account"
lab/auth/create-service-account.sh delete

echo "5. Keycloak realm portico-lab"
lab/saml/kc.sh DELETE /portico-lab 2>&1 | tail -1

echo "6. stale secret files on the Mac"
for f in portico-sa-key portico-oidc-secret portico-kc-admin-secret; do
  [ -e "$S/$f" ] && { rm -f "${S:?}/${f:?}"; echo "   removed $S/$f"; }
done
echo "inventory rows changed: SAML IdP, SAML group tenant-a, service account, Keycloak realm (alice, carol, clients), VM files (store backup portico.db.pre-2e-$ts), the three Keycloak/service-account secret files"
echo "next: lab/portico/deploy.sh --fresh && lab/portico/bootstrap-acceptance.sh"
