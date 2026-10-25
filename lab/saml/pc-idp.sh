#!/usr/bin/env bash
# Read or create the SAML identity provider in Prism Central (design 6.8).
#   lab/saml/pc-idp.sh list
#   lab/saml/pc-idp.sh create [REDIRECT_URL]   refuses if an IdP not named LAB_SAML_IDP (default portico-keycloak) exists
#   lab/saml/pc-idp.sh delete              deletes the SAML users it created first
# The IdP metadata XML is fetched from Keycloak by the Mac and passed inline
# (base64), because the PCVM cannot resolve the lab host names.
# Requires `source lab/env.sh`.
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}" "${LAB_ID_HOST:?}" "${LAB_VM_IP:?}" "${LAB_PRISM_HOST:?}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
NAME="${LAB_SAML_IDP:-portico-keycloak}"
# v4.1.b3: the served v4.0 schema rejects redirectURL ("property unsupported").
EP="/api/iam/v4.1.b3/authn/saml-identity-providers"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

list() { lab/pc.sh GET "$EP" 2>/dev/null | jq -r '.data[]? | "\(.extId)\t\(.name)\t\(.redirectURL // "-")\t\(.idpMetadata.entityId // "-")"'; }

case "${1:?subcommand}" in
  list) list ;;
  create)
    redirect="${2:-}"
    existing=$(lab/pc.sh GET "$EP" 2>/dev/null | jq -r '.data[]?.name')
    if [ -n "$existing" ] && [ "$existing" != "$NAME" ]; then
      echo "refusing: an identity provider already exists: $existing" >&2; exit 2
    fi
    if [ "$existing" = "$NAME" ]; then echo "idp $NAME exists"; list; exit 0; fi
    curl -sf --cacert "$S/lab-ca.crt" --resolve "$LAB_ID_HOST:443:$LAB_VM_IP" \
      "https://$LAB_ID_HOST/realms/${LAB_REALM:-portico}/protocol/saml/descriptor" > "$tmp/idp.xml"
    jq -n --arg n "$NAME" --arg x "$(base64 < "$tmp/idp.xml" | tr -d '\n')" --arg r "$redirect" '
      {name:$n, idpMetadataXml:$x, usernameAttribute:"email", emailAttribute:"email",
       groupsAttribute:"groups", isSignedAuthnReqEnabled:false}
      + (if $r != "" then {redirectURL:$r} else {} end)' > "$tmp/create.json"
    jq 'del(.idpMetadataXml) + {idpMetadataXml:"<base64 of the Keycloak realm SAML descriptor>"}' "$tmp/create.json" > docs/lab/payloads/saml-idp-create.json
    lab/pc.sh POST "$EP" "$tmp/create.json" 2>"$tmp/code" > "$tmp/resp.json" || true
    code=$(tr -d '\n' < "$tmp/code")
    if jq -e '.data.extId' "$tmp/resp.json" >/dev/null 2>&1; then
      echo "$code"; jq -c '.data | {extId, name, redirectURL, entityId: .idpMetadata.entityId}' "$tmp/resp.json"
    else
      echo "$code" >&2; jq -c '.data.error.validationErrorMessages // .data // .' "$tmp/resp.json" >&2; exit 1
    fi
    ;;
  delete)
    id=$(lab/pc.sh GET "$EP" 2>/dev/null | jq -r --arg n "$NAME" '.data[]? | select(.name==$n) | .extId')
    [ -n "$id" ] || { echo "no idp named $NAME"; exit 0; }
    # IAM-21016: the IdP cannot go while users created by its logins exist.
    for u in $(lab/pc.sh GET "/api/iam/v4.1.b3/authn/users?\$filter=userType%20eq%20Schema.Enums.UserType'SAML'" 2>/dev/null | jq -r --arg i "$id" '.data[]? | select(.idpId==$i) | .extId'); do
      uh=$(mktemp); PC_HEADERS_FILE="$uh" lab/pc.sh GET "/api/iam/v4.1.b3/authn/users/$u" >/dev/null 2>&1
      uetag=$(grep -i '^etag:' "$uh" | awk '{print $2}' | tr -d '\r'); rm -f "$uh"
      IF_MATCH="$uetag" lab/pc.sh DELETE "/api/iam/v4.1.b3/authn/users/$u" 2>&1 | tail -1 | sed "s/^/   saml user $u: /"
    done
    h=$(mktemp); PC_HEADERS_FILE="$h" lab/pc.sh GET "$EP/$id" >/dev/null 2>&1
    etag=$(grep -i '^etag:' "$h" | awk '{print $2}' | tr -d '\r'); rm -f "$h"
    IF_MATCH="$etag" lab/pc.sh DELETE "$EP/$id" 2>&1 | tail -1
    ;;
  *) echo "unknown subcommand $1" >&2; exit 2 ;;
esac
