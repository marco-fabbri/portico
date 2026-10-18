#!/usr/bin/env bash
# Build the Portico binary for the lab VM and run it as a systemd service in
# place of the nginx stand-in. Idempotent. Lab only.
#   source lab/env.sh && lab/portico/deploy.sh
# Needs ~/portico-secrets/portico-oidc-secret (lab/saml/keycloak-realm.sh oidc-client),
# portico-kc-admin-secret (keycloak-realm.sh admin-client) and portico-sa-key
# (lab/auth/create-service-account.sh). Generates portico-admin-pw (the
# break-glass password of the admin API) when missing.
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_VM_IP:?source lab/env.sh first}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
# shellcheck source=lab/lib.sh
source lab/lib.sh
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=5 "lab@$LAB_VM_IP")
[ -s "$S/portico-oidc-secret" ] || { echo "deploy.sh: $S/portico-oidc-secret missing; run lab/saml/keycloak-realm.sh oidc-client first" >&2; exit 1; }
[ -s "$S/portico-kc-admin-secret" ] || { echo "deploy.sh: $S/portico-kc-admin-secret missing; run lab/saml/keycloak-realm.sh admin-client first" >&2; exit 1; }
[ -s "$S/portico-sa-key" ] || { echo "deploy.sh: $S/portico-sa-key missing; run lab/auth/create-service-account.sh first" >&2; exit 1; }
umask 077
[ -s "$S/portico-admin-pw" ] || { openssl rand -base64 18 | tr -d '/+=' > "$S/portico-admin-pw"; echo "generated $S/portico-admin-pw"; }

arch=$("${SSH[@]}" uname -m)
[ "$arch" = x86_64 ] || { echo "deploy.sh: lab VM is $arch, this script builds for x86_64" >&2; exit 1; }

# 1. Build (static binary, no cgo)
tmp=$(mktemp -d)
stage=$(remote_stage_dir "lab@$LAB_VM_IP")
trap 'rm -rf "$tmp"; "${SSH[@]}" rm -rf "$stage"' EXIT
version="dev-$(git rev-parse --short HEAD)"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-X main.version=$version" -o "$tmp/portico" ./cmd/portico
echo "built portico $version"

# 2. Stage files in a private directory on the VM. The service-account
#    file is "NAME=key": only the key travels.
cut -d= -f2- "$S/portico-sa-key" | tr -d '\r\n' > "$tmp/prism-api-key"
scp -q -o BatchMode=yes "$tmp/portico" lab/portico/portico.yaml deploy/portico.service \
  "$S/portico-oidc-secret" "$S/portico-kc-admin-secret" "$tmp/prism-api-key" "$S/portico-admin-pw" \
  "$S/lab-ca.crt" "$S/lab-ca.key" "lab@$LAB_VM_IP:$stage/"

# 3. Install on the VM ($1 = staging directory)
"${SSH[@]}" sudo bash -s -- "$stage" <<'REMOTE'
set -euo pipefail
st="$1"
id -u portico >/dev/null 2>&1 || useradd -r -s /sbin/nologin -d /var/lib/portico portico
install -d -m 0750 -o root -g portico /etc/portico /etc/portico/tls
install -m 0640 -o root -g portico "$st/portico.yaml" /etc/portico/portico.yaml
# Secrets and keys: owned by the service user, mode 0600 (the binary refuses wider modes).
install -m 0600 -o portico -g portico "$st/portico-oidc-secret" /etc/portico/oidc-client-secret
install -m 0600 -o portico -g portico "$st/portico-kc-admin-secret" /etc/portico/keycloak-admin-secret
install -m 0600 -o portico -g portico "$st/prism-api-key" /etc/portico/prism-api-key
install -m 0640 -o root -g portico "$st/lab-ca.crt" /etc/portico/tls/lab.crt
install -m 0600 -o portico -g portico "$st/lab-ca.key" /etc/portico/tls/lab.key
install -m 0755 -o root -g root "$st/portico" /usr/local/bin/portico
install -m 0644 -o root -g root "$st/portico.service" /etc/systemd/system/portico.service
# Break-glass password of the admin API: from the staged file on stdin,
# hashed by the binary into the store (owned by the service user).
install -d -m 0750 -o portico -g portico /var/lib/portico
sudo -u portico /usr/local/bin/portico admin set-password -config /etc/portico/portico.yaml < "$st/portico-admin-pw"
rm -rf "$st"
restorecon -R /etc/portico /usr/local/bin/portico /etc/systemd/system/portico.service
firewall-cmd -q --permanent --add-port=8443/tcp --add-port=9440/tcp && firewall-cmd -q --reload
systemctl disable --now nginx >/dev/null 2>&1 || true
systemctl daemon-reload
systemctl enable portico >/dev/null 2>&1
systemctl restart portico
REMOTE

# 4. Wait for the admin listener
for _ in $(seq 1 30); do
  code=$(curl -sk -o /dev/null -w '%{http_code}' "https://$LAB_VM_IP:8443/healthz" || true)
  [ "$code" = 200 ] && { echo "portico ready"; echo "admin UI: https://$LAB_PRISM_HOST:8443/ (resolve the name to $LAB_VM_IP; user admin, password in $S/portico-admin-pw)"; exit 0; }
  sleep 1
done
echo "portico did not become ready; last lines of the journal:" >&2
"${SSH[@]}" sudo journalctl -u portico -n 50 --no-pager >&2
exit 1
