#!/usr/bin/env bash
# Portico installer for Rocky Linux 9 (design 4.9). One script for both
# delivery forms: the appliance image build runs it with --binary and
# --no-start, an MSP runs it on a Rocky 9 host it already has with
# --release. It installs the packages, the portico user and directories,
# the binary, the PostgreSQL and Keycloak quadlets, the units (portico,
# the console on tty1, cloudflared, the Keycloak hostname watcher), the
# firewalld service, the sshd and sysctl drop-ins; it generates the
# Keycloak secrets and the internal TLS pair once. Re-running it upgrades
# the binary and the files and keeps every secret and the store.
#
#   install.sh --binary PATH | --release VERSION [--no-start] [--render-only DIR]
#
# --render-only DIR writes every file under DIR with its absolute path and
# runs no command: the repository's tests use it without root.
set -euo pipefail

usage() { echo "usage: install.sh --binary PATH | --release VERSION [--no-start] [--render-only DIR]" >&2; exit 2; }

BINARY=""; RELEASE=""; START=1; ROOT=""; RENDER=0
while [ $# -gt 0 ]; do
  case "$1" in
    --binary) BINARY=${2:?}; shift 2 ;;
    --release) RELEASE=${2:?}; shift 2 ;;
    --no-start) START=0; shift ;;
    --render-only) ROOT=${2:?}; RENDER=1; START=0; shift 2 ;;
    -h|--help) usage ;;
    *) echo "install.sh: unknown argument $1" >&2; usage ;;
  esac
done
HERE=$(cd "$(dirname "$0")" && pwd)
REPO="https://github.com/marco-fabbri/portico"
if [ $RENDER -eq 0 ] && [ "$(id -u)" -ne 0 ]; then echo "install.sh: run as root" >&2; exit 1; fi
if [ $RENDER -eq 0 ] && [ ! -r /etc/os-release ]; then echo "install.sh: not a Linux host" >&2; exit 1; fi
if [ $RENDER -eq 0 ] && ! grep -qE '^ID="?rocky"?' /etc/os-release; then echo "install.sh: Rocky Linux 9 is the supported host" >&2; exit 1; fi

# put MODE OWNER GROUP SRC DST: install one file (owner and group skipped in render mode).
put() {
  local mode=$1 owner=$2 group=$3 src=$4 dst="$ROOT$5"
  mkdir -p "$(dirname "$dst")"
  if [ $RENDER -eq 1 ]; then install -m "$mode" "$src" "$dst"; else install -m "$mode" -o "$owner" -g "$group" "$src" "$dst"; fi
}
# putdir MODE OWNER GROUP PATH
putdir() {
  local mode=$1 owner=$2 group=$3 dst="$ROOT$4"
  mkdir -p "$dst"; chmod "$mode" "$dst"
  [ $RENDER -eq 1 ] || chown "$owner:$group" "$dst"
}
log() { echo "install.sh: $*"; }

# 1. Packages.
if [ $RENDER -eq 0 ]; then
  log "packages"
  dnf -y -q install podman firewalld NetworkManager policycoreutils-python-utils openssl curl tar
  if [ ! -r /etc/yum.repos.d/cloudflared.repo ]; then
    curl -fsSL https://pkg.cloudflare.com/cloudflared.repo -o /etc/yum.repos.d/cloudflared.repo || log "warning: the cloudflared repository is not reachable; tunnel mode needs it later"
  fi
  dnf -y -q install cloudflared || log "warning: cloudflared not installed; tunnel mode needs it later"
  id -u portico >/dev/null 2>&1 || useradd -r -s /sbin/nologin -d /var/lib/portico portico
fi

# 2. Directories.
putdir 0750 root portico /etc/portico
putdir 0750 root portico /etc/portico/tls
putdir 0700 root root /etc/cloudflared
putdir 0755 portico portico /var/lib/portico
putdir 0755 portico portico /var/lib/portico/keycloak-theme

# 3. The binary.
if [ -n "$BINARY" ]; then
  put 0755 root root "$BINARY" /usr/local/bin/portico
elif [ -n "$RELEASE" ]; then
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  base="$REPO/releases/download/v$RELEASE/portico-$RELEASE-linux-amd64.tar.gz"
  log "downloading $base"
  curl -fsSL -o "$tmp/portico.tar.gz" "$base"
  curl -fsSL -o "$tmp/portico.tar.gz.sha256" "$base.sha256"
  (cd "$tmp" && sed 's#  .*#  portico.tar.gz#' portico.tar.gz.sha256 | sha256sum -c --quiet -)
  tar -xzf "$tmp/portico.tar.gz" -C "$tmp" portico
  put 0755 root root "$tmp/portico" /usr/local/bin/portico
elif [ ! -x "$ROOT/usr/local/bin/portico" ]; then
  echo "install.sh: no binary: give --binary PATH or --release VERSION" >&2; exit 1
fi

# 4. Process settings (kept when present: the wizard edits them).
[ -e "$ROOT/etc/portico/portico.yaml" ] || put 0640 root portico "$HERE/portico.appliance.yaml" /etc/portico/portico.yaml

# 5. Keycloak secrets, generated once; shown by the console wizard only.
if [ ! -e "$ROOT/etc/portico/keycloak.env" ]; then
  umask 077
  kcpw=$(openssl rand -base64 24 | tr -d '/+=')
  dbpw=$(openssl rand -base64 24 | tr -d '/+=')
  env=$(mktemp)
  printf 'KC_BOOTSTRAP_ADMIN_USERNAME=kcadmin\nKC_BOOTSTRAP_ADMIN_PASSWORD=%s\nKC_DB_PASSWORD=%s\nPOSTGRES_PASSWORD=%s\n' "$kcpw" "$dbpw" "$dbpw" > "$env"
  put 0600 root root "$env" /etc/portico/keycloak.env
  rm -f "$env"; unset kcpw dbpw
  umask 022
fi

# 6. Internal TLS pair for the admin listener, generated once (self-signed,
#    ten years; the public pair is the wizard's business).
if [ ! -e "$ROOT/etc/portico/tls/internal.key" ]; then
  umask 077
  tlsd=$(mktemp -d)
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
    -keyout "$tlsd/internal.key" -out "$tlsd/internal.crt" -subj "/CN=portico" -addext "subjectAltName=DNS:portico" >/dev/null 2>&1
  put 0644 root portico "$tlsd/internal.crt" /etc/portico/tls/internal.crt
  put 0640 root portico "$tlsd/internal.key" /etc/portico/tls/internal.key
  rm -rf "$tlsd"
  umask 022
fi

# 7. Quadlets, units, drop-ins.
put 0644 root root "$HERE/quadlets/portico.network" /etc/containers/systemd/portico.network
put 0644 root root "$HERE/quadlets/postgres.container" /etc/containers/systemd/postgres.container
put 0644 root root "$HERE/quadlets/keycloak.container" /etc/containers/systemd/keycloak.container
put 0644 root root "$HERE/portico.service" /etc/systemd/system/portico.service
put 0644 root root "$HERE/portico-console.service" /etc/systemd/system/getty@tty1.service.d/override.conf
put 0644 root root "$HERE/cloudflared.service" /etc/systemd/system/cloudflared.service
put 0644 root root "$HERE/keycloak-hostname.path" /etc/systemd/system/keycloak-hostname.path
put 0644 root root "$HERE/keycloak-hostname.service" /etc/systemd/system/keycloak-hostname.service
put 0644 root root "$HERE/firewalld/portico.xml" /etc/firewalld/services/portico.xml
put 0600 root root "$HERE/sshd/00-portico.conf" /etc/ssh/sshd_config.d/00-portico.conf
put 0644 root root "$HERE/sysctl/60-portico.conf" /etc/sysctl.d/60-portico.conf

if [ $RENDER -eq 1 ]; then log "rendered under $ROOT"; exit 0; fi

# 8. Apply: SELinux labels, kernel parameters, units, firewall, sshd.
restorecon -R /etc/portico /usr/local/bin/portico /etc/containers/systemd /etc/systemd/system /var/lib/portico
sysctl -q --system >/dev/null
systemctl daemon-reload
systemctl enable firewalld >/dev/null 2>&1 || true
if [ $START -eq 1 ]; then
  systemctl start firewalld
  firewall-cmd -q --permanent --add-service=portico
  firewall-cmd -q --permanent --remove-service=cockpit 2>/dev/null || true
  firewall-cmd -q --permanent --remove-service=dhcpv6-client 2>/dev/null || true
  firewall-cmd -q --reload
fi
if sshd -t 2>/dev/null; then systemctl reload sshd 2>/dev/null || true; fi
systemctl enable portico keycloak-hostname.path >/dev/null 2>&1 || true
if [ $START -eq 1 ]; then
  log "starting the services"
  systemctl start postgres.service keycloak.service keycloak-hostname.path
  systemctl restart portico.service
  systemctl restart getty@tty1.service 2>/dev/null || true
  for _ in $(seq 1 30); do
    if curl -sk -o /dev/null -w '%{http_code}' https://127.0.0.1:8443/healthz 2>/dev/null | grep -q 200; then log "portico ready: open the console (tty1) or https://<address>:8443/"; exit 0; fi
    sleep 2
  done
  log "portico did not answer within 60 s: journalctl -u portico" >&2; exit 1
fi
log "installed without starting the services (--no-start)"
