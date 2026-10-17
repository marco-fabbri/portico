#!/usr/bin/env bash
# Build the lab realm in Keycloak. Idempotent. Lab only.
#   lab/saml/keycloak-realm.sh realm                         create realm portico-lab
#   lab/saml/keycloak-realm.sh group NAME                    create group
#   lab/saml/keycloak-realm.sh user EMAIL GROUP              create user in group (password in ~/portico-secrets/<local>-pw)
#   lab/saml/keycloak-realm.sh saml-client ENTITY_ID ACS_URL create/update the SAML client for Prism Central
# Requires `source lab/env.sh`.
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_ID_HOST:?source lab/env.sh first}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
R="portico-lab"
kc() { lab/saml/kc.sh "$@"; }
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

case "${1:?subcommand}" in
  realm)
    if kc GET "/$R" 2>/dev/null | jq -e .realm >/dev/null; then echo "realm $R exists"; exit 0; fi
    jq -n --arg r "$R" '{realm:$r, enabled:true, displayName:"Portico lab", loginWithEmailAllowed:true, registrationEmailAsUsername:true}' > "$tmp/realm.json"
    kc POST "" "$tmp/realm.json" 2>&1 >/dev/null | tr -d '\n'; echo " realm $R created"
    ;;
  group)
    g="${2:?group name}"
    if kc GET "/$R/groups?search=$g&exact=true" 2>/dev/null | jq -e '.[0].id' >/dev/null; then echo "group $g exists"; exit 0; fi
    jq -n --arg n "$g" '{name:$n}' > "$tmp/g.json"
    kc POST "/$R/groups" "$tmp/g.json" 2>&1 >/dev/null | tr -d '\n'; echo " group $g created"
    ;;
  user)
    email="${2:?email}"; g="${3:?group}"; local="${email%%@*}"
    umask 077; [ -s "$S/$local-pw" ] || openssl rand -base64 18 | tr -d '/+=' > "$S/$local-pw"
    gid=$(kc GET "/$R/groups?search=$g&exact=true" 2>/dev/null | jq -r '.[0].id'); [ "$gid" != null ]
    uid=$(kc GET "/$R/users?email=$email&exact=true" 2>/dev/null | jq -r '.[0].id // empty')
    if [ -z "$uid" ]; then
      jq -n --arg e "$email" --arg l "$local" --arg p "$(cat "$S/$local-pw")" \
        '{username:$e, email:$e, firstName:($l|ascii_upcase[0:1]+.[1:]), lastName:"Lab", enabled:true, emailVerified:true, credentials:[{type:"password", value:$p, temporary:false}]}' > "$tmp/u.json"
      kc POST "/$R/users" "$tmp/u.json" 2>&1 >/dev/null | tr -d '\n'
      uid=$(kc GET "/$R/users?email=$email&exact=true" 2>/dev/null | jq -r '.[0].id'); echo " user $email created"
    else echo "user $email exists"; fi
    kc PUT "/$R/users/$uid/groups/$gid" 2>&1 >/dev/null | tr -d '\n'; echo " in group $g"
    ;;
  saml-client)
    entity="${2:?entityID}"; acs="${3:?ACS URL}"
    jq -n --arg e "$entity" --arg a "$acs" '{
      clientId:$e, protocol:"saml", enabled:true, name:"Prism Central (lab)",
      redirectUris:[$a], baseUrl:$a, adminUrl:$a,
      frontchannelLogout:true,
      attributes:{
        "saml.assertion.signature":"true", "saml.server.signature":"true",
        "saml.client.signature":"false", "saml.force.post.binding":"true",
        "saml.authnstatement":"true", "saml_name_id_format":"email",
        "saml_force_name_id_format":"true", "saml.signature.algorithm":"RSA_SHA256",
        "saml_assertion_consumer_url_post":$a, "saml_single_logout_service_url_post":$a
      }}' > "$tmp/c.json"
    q=$(jq -rn --arg e "$entity" '$e|@uri')
    cid=$(kc GET "/$R/clients?clientId=$q" 2>/dev/null | jq -r '.[0].id // empty')
    if [ -z "$cid" ]; then kc POST "/$R/clients" "$tmp/c.json" 2>&1 >/dev/null | tr -d '\n'; echo " saml client created"
    else kc PUT "/$R/clients/$cid" "$tmp/c.json" 2>&1 >/dev/null | tr -d '\n'; echo " saml client updated"; fi
    cid=$(kc GET "/$R/clients?clientId=$q" 2>/dev/null | jq -r '.[0].id')
    # Attribute mappers Prism Central can use: email and groups.
    for m in email groups; do
      if ! kc GET "/$R/clients/$cid/protocol-mappers/models" 2>/dev/null | jq -e --arg n "$m" '.[]|select(.name==$n)' >/dev/null; then
        if [ "$m" = email ]; then
          jq -n '{name:"email", protocol:"saml", protocolMapper:"saml-user-property-mapper", config:{"user.attribute":"email","attribute.name":"email","attribute.nameformat":"Basic"}}' > "$tmp/m.json"
        else
          jq -n '{name:"groups", protocol:"saml", protocolMapper:"saml-group-membership-mapper", config:{"attribute.name":"groups","attribute.nameformat":"Basic","single":"true","full.path":"false"}}' > "$tmp/m.json"
        fi
        kc POST "/$R/clients/$cid/protocol-mappers/models" "$tmp/m.json" 2>&1 >/dev/null | tr -d '\n'; echo " mapper $m added"
      fi
    done
    ;;
  *) echo "unknown subcommand $1" >&2; exit 2 ;;
esac
