#!/usr/bin/env bash
# Keycloak login-theme probe (write-up 13). Lab only.
# Writes a minimal theme into the directory mounted in the Keycloak
# container, sets it as the realm's login theme through the admin API,
# fetches the login page and reports whether the theme applied without a
# restart. Prints the header-related lines of the page so the CSS
# selectors can be read off the real markup.
#   source lab/env.sh && lab/saml/theme-probe.sh [reset]
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_VM_IP:?source lab/env.sh first}" "${LAB_ID_HOST:?}" "${LAB_PRISM_HOST:?}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
R=portico-lab
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=5 "lab@$LAB_VM_IP")
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
set_theme() {  # set_theme NAME
  lab/saml/kc.sh GET "/$R" 2>/dev/null | jq --arg t "$1" '.loginTheme = $t' > "$tmp/realm.json"
  lab/saml/kc.sh PUT "/$R" "$tmp/realm.json" 2>&1 >/dev/null | tr -d '\n'; echo " loginTheme=$1"
}
if [ "${1:-}" = reset ]; then set_theme keycloak; exit 0; fi

# 1. Minimal theme, written as the portico user (the directory is its).
"${SSH[@]}" "cd / && sudo -u portico bash -s" <<'REMOTE'
set -e
d=/var/lib/portico/keycloak-theme
mkdir -p "$d/login/resources/css" "$d/login/messages"
cat > "$d/login/theme.properties" <<'EOT'
parent=keycloak.v2
import=common/keycloak
styles=css/login.css css/portico.css
EOT
cat > "$d/login/resources/css/portico.css" <<'EOT'
/* probe */ .pf-v5-c-button.pf-m-primary { background-color: #8b1f5f; }
EOT
cat > "$d/login/messages/messages_en.properties" <<'EOT'
loginTitleHtml=Probe MSP
loginAccountTitle=Probe sign in
EOT
find "$d" -type f | sort
REMOTE

# 2. Realm login theme.
set_theme portico

# 3. Fetch the login page through Portico's public name.
# The portico client requires PKCE: any S256 challenge renders the page.
url="https://$LAB_ID_HOST/realms/$R/protocol/openid-connect/auth?client_id=portico&response_type=code&redirect_uri=https%3A%2F%2F$LAB_PRISM_HOST%2F.portico%2Fcallback&scope=openid&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256"
curl -s --cacert "$S/lab-ca.crt" --resolve "$LAB_ID_HOST:443:$LAB_VM_IP" "$url" > "$tmp/page.html"
for needle in portico.css "Probe MSP" "Probe sign in"; do
  if grep -q "$needle" "$tmp/page.html"; then echo "ok   page contains: $needle"; else echo "MISS page lacks: $needle"; fi
done
echo "--- header markup (for the selectors):"
grep -oE '<(div|header|h1|h2|img)[^>]*(kc-header|login__header|login__main-header|kc-page-title|login__main-title)[^>]*>' "$tmp/page.html" | head -8
