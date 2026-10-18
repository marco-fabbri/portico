#!/usr/bin/env bash
# Build the Portico binary for the lab VM and run it as a systemd service in
# place of the nginx stand-in. Idempotent. Lab only.
#   source lab/env.sh && lab/portico/deploy.sh
# Since Milestone 2d the VM runs from the store: the first deploy imports
# lab/portico/portico.yaml (runtime sections plus the three secret files)
# with `portico settings import`, then installs lab/portico/portico.store.yaml
# as /etc/portico/portico.yaml and removes the secret files. Later deploys
# find the settings stored and skip the import.
# Needs ~/portico-secrets/portico-oidc-secret (lab/saml/keycloak-realm.sh oidc-client),
# portico-kc-admin-secret (keycloak-realm.sh admin-client) and portico-sa-key
# (lab/auth/create-service-account.sh). Generates portico-admin-pw (the
# break-glass password of the admin API) when missing.
# With --fresh (Milestone 2e) no secret file is needed and nothing is
# imported: the service starts in setup mode on an empty store and the
# setup page (or lab/portico/bootstrap-acceptance.sh) creates everything.
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_VM_IP:?source lab/env.sh first}"
S="${PORTICO_SECRETS:-$HOME/portico-secrets}"
# shellcheck source=lab/lib.sh
source lab/lib.sh
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=5 "lab@$LAB_VM_IP")
fresh=0; [ "${1:-}" = "--fresh" ] && fresh=1
if [ $fresh -eq 0 ]; then
  [ -s "$S/portico-oidc-secret" ] || { echo "deploy.sh: $S/portico-oidc-secret missing; run lab/saml/keycloak-realm.sh oidc-client first (or deploy with --fresh)" >&2; exit 1; }
  [ -s "$S/portico-kc-admin-secret" ] || { echo "deploy.sh: $S/portico-kc-admin-secret missing; run lab/saml/keycloak-realm.sh admin-client first (or deploy with --fresh)" >&2; exit 1; }
  [ -s "$S/portico-sa-key" ] || { echo "deploy.sh: $S/portico-sa-key missing; run lab/auth/create-service-account.sh first (or deploy with --fresh)" >&2; exit 1; }
fi
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
#    file is "NAME=key": only the key travels. A fresh deploy stages no
#    secret at all.
files=("$tmp/portico" lab/portico/portico.store.yaml deploy/portico.service "$S/portico-admin-pw" "$S/lab-ca.crt" "$S/lab-ca.key")
if [ $fresh -eq 0 ]; then
  cut -d= -f2- "$S/portico-sa-key" | tr -d '\r\n' > "$tmp/prism-api-key"
  files+=(lab/portico/portico.yaml "$S/portico-oidc-secret" "$S/portico-kc-admin-secret" "$tmp/prism-api-key")
fi
scp -q -o BatchMode=yes "${files[@]}" "lab@$LAB_VM_IP:$stage/"

# 3. Install on the VM ($1 = staging directory, $2 = 1 for a fresh deploy)
"${SSH[@]}" sudo bash -s -- "$stage" "$fresh" <<'REMOTE'
set -euo pipefail
st="$1"; fresh="$2"
id -u portico >/dev/null 2>&1 || useradd -r -s /sbin/nologin -d /var/lib/portico portico
install -d -m 0750 -o root -g portico /etc/portico /etc/portico/tls
# Keycloak login theme written by Portico, mounted read-only in the container.
install -d -m 0755 -o portico -g portico /var/lib/portico/keycloak-theme
install -m 0640 -o root -g portico "$st/lab-ca.crt" /etc/portico/tls/lab.crt
install -m 0600 -o portico -g portico "$st/lab-ca.key" /etc/portico/tls/lab.key
install -m 0755 -o root -g root "$st/portico" /usr/local/bin/portico
install -m 0644 -o root -g root "$st/portico.service" /etc/systemd/system/portico.service
install -d -m 0750 -o portico -g portico /var/lib/portico
# The YAML in place holds the process settings only; the runtime sections
# and the secrets are in the store (sealed with the master key, created by
# the binary at its first run as the service user).
install -m 0640 -o root -g portico "$st/portico.store.yaml" /etc/portico/portico.yaml
if [ "$fresh" = 1 ]; then
  echo "fresh deploy: import skipped, the service starts in setup mode"
elif sudo -u portico /usr/local/bin/portico settings show -config /etc/portico/portico.yaml >/dev/null 2>&1; then
  echo "settings already in the store; import skipped"
else
  # First deploy: the full YAML and the secret files exist only for the
  # import, mode 0600 (the binary refuses wider modes), then go away.
  install -m 0640 -o root -g portico "$st/portico.yaml" /etc/portico/portico.import.yaml
  install -m 0600 -o portico -g portico "$st/portico-oidc-secret" /etc/portico/oidc-client-secret
  install -m 0600 -o portico -g portico "$st/portico-kc-admin-secret" /etc/portico/keycloak-admin-secret
  install -m 0600 -o portico -g portico "$st/prism-api-key" /etc/portico/prism-api-key
  sudo -u portico /usr/local/bin/portico settings import -config /etc/portico/portico.import.yaml
  rm -f /etc/portico/portico.import.yaml
fi
rm -f /etc/portico/oidc-client-secret /etc/portico/keycloak-admin-secret /etc/portico/prism-api-key
# Break-glass password of the admin API: from the staged file on stdin,
# hashed by the binary into the store (owned by the service user).
sudo -u portico /usr/local/bin/portico admin set-password -config /etc/portico/portico.yaml < "$st/portico-admin-pw"
rm -rf "$st"
restorecon -R /etc/portico /usr/local/bin/portico /etc/systemd/system/portico.service
firewall-cmd -q --permanent --add-port=8443/tcp --add-port=9440/tcp && firewall-cmd -q --reload
systemctl disable --now nginx >/dev/null 2>&1 || true
systemctl daemon-reload
systemctl enable portico >/dev/null 2>&1
systemctl reset-failed portico >/dev/null 2>&1 || true
systemctl restart portico
REMOTE

# 4. Wait for the admin listener
for _ in $(seq 1 30); do
  code=$(curl -sk -o /dev/null -w '%{http_code}' "https://$LAB_VM_IP:8443/healthz" || true)
  if [ "$code" = 200 ]; then
    echo "portico ready"
    [ $fresh -eq 1 ] && "${SSH[@]}" sudo journalctl -u portico -n 20 --no-pager | grep -o 'setup mode.*' | head -1
    echo "admin UI: https://$LAB_PRISM_HOST:8443/ (resolve the name to $LAB_VM_IP; user admin, password in $S/portico-admin-pw)"; exit 0
  fi
  sleep 1
done
echo "portico did not become ready; last lines of the journal:" >&2
"${SSH[@]}" sudo journalctl -u portico -n 50 --no-pager >&2
exit 1
