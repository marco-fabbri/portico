#!/usr/bin/env bash
# Sites measurements (write-up 20, D3): can Portico's SAML identity
# provider in Prism Central carry its own entityIssuer (one per site on
# one public name), and does a login work with it? Lab only, Prism Central admin from lab/env.sh, the
# appliance's Keycloak through lab/saml/kc.sh. Changes the live trust and
# always puts it back (trap), also with --revert alone.
#   source lab/env.sh && lab/sites/idp-entity-issuer.sh [--revert]
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}" "${LAB_PRISM_HOST:?}" "${LAB_VM_IP:?}"
# shellcheck source=lab/projects/lib.sh
source lab/projects/lib.sh
REALM="${LAB_REALM:-portico}"; export LAB_SAML_IDP="${LAB_SAML_IDP:-portico-keycloak}"
umask 077; tmp=$(mktemp -d)
state="${TMPDIR:-/tmp}/portico-idp-issuer.state"   # original values, for --revert after a crash
id=$(saml_idp_extid); [ -n "$id" ] || { echo "no IdP $LAB_SAML_IDP" >&2; exit 1; }
path="$IAM1/authn/saml-identity-providers/$id"
spmeta() { lab/pc.sh GET "$path/sp-metadata" 2>/dev/null | grep -oE 'entityID="[^"]*"' | head -1 | sed -E 's/entityID="([^"]*)"/\1/'; }
kc_client_uuid() { lab/saml/kc.sh GET "/$REALM/clients?clientId=$(jq -rn --arg c "$1" '$c|@uri')" 2>/dev/null | jq -r '.[0].id // empty'; }
# The PUT must carry the IdP metadata again (the GET does not return it)
# and no empty groupsDelim (measured: 400 IAM-20011 otherwise): the realm
# descriptor is read from Keycloak's loopback on the appliance.
read -r -a sshopts <<<"${LAB_SSH_OPTS:-}"
ssh -o BatchMode=yes -o LogLevel=ERROR "${sshopts[@]}" "${LAB_SSH_USER:-admin}@$LAB_VM_IP" "curl -s http://127.0.0.1:8080/realms/$REALM/protocol/saml/descriptor" > "$tmp/desc.xml" 2>/dev/null
grep -q EntityDescriptor "$tmp/desc.xml" || { echo "no SAML descriptor from Keycloak" >&2; exit 1; }
set_issuer() {  # set_issuer VALUE -> prints the HTTP code of the PUT
  lab/pc.sh GET "$path" 2>/dev/null | jq --arg e "$1" --rawfile x "$tmp/desc.xml" '.data | {name, idpMetadataXml: ($x|@base64), usernameAttribute, emailAttribute, groupsAttribute, isSignedAuthnReqEnabled, redirectURL, entityIssuer: $e}' > "$tmp/put.json"
  local etag; etag=$(etag_of "$path")
  IF_MATCH="$etag" lab/pc.sh PUT "$path" "$tmp/put.json" 2>"$tmp/code" >"$tmp/put.out"; tail -1 "$tmp/code" | sed 's/HTTP //'
}
set_kc_client() {  # set_kc_client FROM TO
  local u; u=$(kc_client_uuid "$1"); [ -n "$u" ] || { echo "no Keycloak client $1" >&2; return 1; }
  # Keycloak updates a client from its whole representation.
  lab/saml/kc.sh GET "/$REALM/clients/$u" 2>/dev/null | jq --arg c "$2" '.clientId = $c' > "$tmp/kc.json"
  lab/saml/kc.sh PUT "/$REALM/clients/$u" "$tmp/kc.json" >/dev/null 2>"$tmp/kccode"; tail -1 "$tmp/kccode"
}
revert() {
  [ -s "$state" ] || return 0
  local orig cur kc
  orig=$(sed -n 1p "$state"); kc=$(sed -n 2p "$state")
  cur=$(lab/pc.sh GET "$path" 2>/dev/null | jq -r '.data.entityIssuer // empty')
  [ "$cur" != "$orig" ] && echo "revert IdP entityIssuer: HTTP $(set_issuer "$orig")"
  [ -n "$kc" ] && [ "$kc" != "$orig" ] && set_kc_client "$kc" "$orig" >/dev/null && echo "revert Keycloak client id"
  rm -f "$state"; rm -rf "$tmp"
}
if [ "${1:-}" = "--revert" ]; then revert; exit; fi
trap revert EXIT

orig=$(lab/pc.sh GET "$path" 2>/dev/null | jq -r '.data.entityIssuer // empty')
echo "IdP $LAB_SAML_IDP ($id): entityIssuer=$orig, SP metadata entityID=$(spmeta)"
printf '%s\n%s\n' "$orig" "$orig" > "$state"
new="https://$LAB_PRISM_HOST/api/iam/authn/site-1"
code=$(set_issuer "$new")
echo "PUT entityIssuer=$new: HTTP $code"
[ "$code" = 200 ] || [ "$code" = 202 ] || { jq -c '[.data.error.validationErrorMessages[]?.message] // .data.error // .' "$tmp/put.out" 2>/dev/null | head -c 1500; echo; exit 0; }
sleep 3
now=$(lab/pc.sh GET "$path" 2>/dev/null | jq -r '.data.entityIssuer // empty')
echo "read back entityIssuer=$now, SP metadata entityID=$(spmeta)"
[ "$now" = "$new" ] || exit 0
echo "Keycloak client $orig -> $new: $(set_kc_client "$orig" "$new")"; printf '%s\n%s\n' "$orig" "$new" > "$state"
echo "=== login with matching audience"
lab/sites/tenant-login.sh "${LAB_TENANT_SLUG:-acme}" | tail -3; echo "exit $?"
# Whether Prism Central refuses an assertion made for another audience
# needs a second Prism Central (an assertion of one site posted to the
# other's ACS): with one, Keycloak refuses a request whose issuer is not
# a client, before any assertion exists. Measured in the two-site test.
