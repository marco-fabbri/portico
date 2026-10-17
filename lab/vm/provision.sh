#!/usr/bin/env bash
# Provision Keycloak + PostgreSQL (podman quadlets) and nginx on portico-lab.
# Generates passwords into ~/portico-secrets/ and a self-signed cert with
# SANs for both lab host names. Idempotent. Requires `source lab/env.sh`.
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_VM_IP:?source lab/env.sh first}" "${LAB_ID_HOST:?}" "${LAB_PRISM_HOST:?}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=5 "lab@$LAB_VM_IP")
umask 077

# 1. Secrets (generated once)
for f in keycloak-admin-pw keycloak-db-pw; do
  [ -s "$S/$f" ] || openssl rand -base64 24 | tr -d '/+=' | cut -c1-24 > "$S/$f"
done
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
{
  echo "KC_BOOTSTRAP_ADMIN_USERNAME=kcadmin"
  echo "KC_BOOTSTRAP_ADMIN_PASSWORD=$(cat "$S/keycloak-admin-pw")"
  echo "KC_DB_PASSWORD=$(cat "$S/keycloak-db-pw")"
  echo "POSTGRES_PASSWORD=$(cat "$S/keycloak-db-pw")"
} > "$tmp/keycloak.env"

# 2. Self-signed certificate with both SANs (generated once)
if [ ! -s "$S/lab-ca.crt" ]; then
  openssl req -x509 -newkey rsa:2048 -nodes -days 365 -keyout "$S/lab-ca.key" -out "$S/lab-ca.crt" \
    -subj "/CN=portico-lab" -addext "subjectAltName=DNS:$LAB_ID_HOST,DNS:$LAB_PRISM_HOST" >/dev/null 2>&1
fi

# 3. Copy files
scp -q -o BatchMode=yes lab/vm/postgres.container lab/vm/keycloak.container lab/vm/portico-lab.network \
  lab/vm/nginx.conf "$tmp/keycloak.env" "$S/lab-ca.crt" "$S/lab-ca.key" "lab@$LAB_VM_IP:/tmp/"

# 4. Install on the VM
"${SSH[@]}" sudo bash -s <<'REMOTE'
set -euo pipefail
install -d -m 0750 /etc/portico-lab /etc/portico-lab/tls /etc/containers/systemd
install -m 0600 /tmp/keycloak.env /etc/portico-lab/keycloak.env
install -m 0644 /tmp/lab-ca.crt /etc/portico-lab/tls/lab.crt
install -m 0600 /tmp/lab-ca.key /etc/portico-lab/tls/lab.key
install -m 0644 /tmp/postgres.container /tmp/keycloak.container /tmp/portico-lab.network /etc/containers/systemd/
install -m 0644 /tmp/nginx.conf /etc/nginx/nginx.conf
rm -f /tmp/keycloak.env /tmp/lab-ca.key
restorecon -R /etc/portico-lab /etc/nginx/nginx.conf
setsebool -P httpd_can_network_connect 1
rpm -q firewalld >/dev/null || dnf -y -q install firewalld
systemctl enable --now firewalld >/dev/null
firewall-cmd -q --add-service=https --permanent && firewall-cmd -q --reload
systemctl daemon-reload
# Quadlet-generated units cannot be enabled; the [Install] section handles that.
systemctl start postgres.service keycloak.service
nginx -t && systemctl enable --now nginx && systemctl reload nginx
REMOTE

# 5. Wait for Keycloak
echo "waiting for keycloak"
for _ in $(seq 1 60); do
  # One quoted string: ssh re-parses the remote command line.
  code=$("${SSH[@]}" "curl -s -o /dev/null -w '%{http_code}' -H 'Host: $LAB_ID_HOST' http://127.0.0.1:8080/realms/master" || true)
  [ "$code" = "200" ] && { echo "keycloak ready"; exit 0; }
  sleep 5
done
echo "keycloak did not become ready" >&2; exit 1
