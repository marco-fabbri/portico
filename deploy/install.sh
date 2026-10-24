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
#   install.sh --first-boot
#
# --render-only DIR writes every file under DIR with its absolute path and
# runs no command: the repository's tests use it without root.
# --no-start is the image build: nothing that must differ between two
# appliances is written (no Keycloak secret, no TLS pair, no firewall
# runtime state); portico-firstboot.service runs `--first-boot` at the
# first boot of every appliance to generate them and start the services.
set -euo pipefail

usage() { echo "usage: install.sh --binary PATH | --release VERSION [--no-start] [--render-only DIR] | install.sh --first-boot" >&2; exit 2; }

BINARY=""; RELEASE=""; START=1; ROOT=""; RENDER=0; FIRSTBOOT=0; RENDER_SECRETS=0
while [ $# -gt 0 ]; do
  case "$1" in
    --binary) BINARY=${2:?}; shift 2 ;;
    --release) RELEASE=${2:?}; shift 2 ;;
    --no-start) START=0; shift ;;
    --first-boot) FIRSTBOOT=1; shift ;;
    --render-only) ROOT=${2:?}; RENDER=1; START=0; shift 2 ;;
    --render-secrets) RENDER_SECRETS=1; shift ;;  # tests: render the first-boot files too
    -h|--help) usage ;;
    *) echo "install.sh: unknown argument $1" >&2; usage ;;
  esac
done
# The script resolves itself through BASH_SOURCE: the quadlets, units and
# drop-ins it installs live beside it, so it runs from the unpacked
# release (or the repository), never piped into bash.
SELF=${BASH_SOURCE[0]}
HERE=$(cd "$(dirname "$SELF")" && pwd)
REPO="https://github.com/marco-fabbri/portico"
# --first-boot runs from the installed copy alone (/usr/local/sbin) and installs nothing from the tree.
if [ $FIRSTBOOT -eq 0 ] && [ ! -d "$HERE/quadlets" ]; then echo "install.sh: run it from the unpacked release: $HERE/quadlets is missing" >&2; exit 1; fi
if [ $RENDER -eq 0 ] && [ "$(id -u)" -ne 0 ]; then echo "install.sh: run as root" >&2; exit 1; fi
if [ $RENDER -eq 0 ] && [ ! -r /etc/os-release ]; then echo "install.sh: not a Linux host" >&2; exit 1; fi
if [ $RENDER -eq 0 ] && ! grep -qE '^ID="?rocky"?' /etc/os-release; then echo "install.sh: Rocky Linux 9 is the supported host" >&2; exit 1; fi

# put MODE OWNER GROUP SRC DST: install one file (owner and group skipped in render mode).
# A destination that was absent or differs is noted in CHANGED, so a second
# run restarts only the services whose files changed.
CHANGED=""
put() {
  local mode=$1 owner=$2 group=$3 src=$4 dst="$ROOT$5"
  mkdir -p "$(dirname "$dst")"
  if [ ! -e "$dst" ] || ! cmp -s "$src" "$dst"; then CHANGED="$CHANGED $5"; fi
  if [ $RENDER -eq 1 ]; then install -m "$mode" "$src" "$dst"; else install -m "$mode" -o "$owner" -g "$group" "$src" "$dst"; fi
}
# putdir MODE OWNER GROUP PATH
putdir() {
  local mode=$1 owner=$2 group=$3 dst="$ROOT$4"
  mkdir -p "$dst"; chmod "$mode" "$dst"
  [ $RENDER -eq 1 ] || chown "$owner:$group" "$dst"
}
log() { echo "install.sh: $*"; }

# Secrets and TLS pairs: generated once per appliance, never at image build.
generate_secrets() {
  # Keycloak administrator and database passwords; shown by the console wizard only.
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
  # Self-signed pairs: internal (the admin listener, ten years) and public
  # (a placeholder so the service starts; the wizard installs the real one).
  local name
  for name in internal public; do
    [ -e "$ROOT/etc/portico/tls/$name.key" ] && continue
    umask 077
    tlsd=$(mktemp -d)
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
      -keyout "$tlsd/$name.key" -out "$tlsd/$name.crt" -subj "/CN=portico" -addext "subjectAltName=DNS:portico" >/dev/null 2>&1
    put 0644 root portico "$tlsd/$name.crt" "/etc/portico/tls/$name.crt"
    put 0640 root portico "$tlsd/$name.key" "/etc/portico/tls/$name.key"
    rm -rf "$tlsd"
    umask 022
  done
}

# Firewall: the portico service (443, 8443) in the default zone, cockpit
# and dhcpv6-client out; permanent, through the daemon when it runs.
configure_firewall() {
  if systemctl is-active -q firewalld; then
    firewall-cmd -q --permanent --add-service=portico
    firewall-cmd -q --permanent --remove-service=cockpit 2>/dev/null || true
    firewall-cmd -q --permanent --remove-service=dhcpv6-client 2>/dev/null || true
    firewall-cmd -q --reload
  else
    firewall-offline-cmd -q --add-service=portico 2>/dev/null || true
    firewall-offline-cmd -q --remove-service=cockpit 2>/dev/null || true
    firewall-offline-cmd -q --remove-service=dhcpv6-client 2>/dev/null || true
  fi
}


if [ $FIRSTBOOT -eq 1 ]; then
  # Runs from portico-firstboot.service, ordered before the services:
  # they start by themselves once this exits (a blocking systemctl start
  # from here would wait for this very unit).
  # No package, no download: a first boot may have no route to the
  # Internet yet (measured on NTNX-01: dnf failed on the cloudflared repo
  # and the whole first boot with it).
  log "first boot: secrets, TLS pairs, firewall"
  generate_secrets
  restorecon -R /etc/portico >/dev/null 2>&1 || true
  configure_firewall
  log "first boot done"
  exit 0
fi

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
# The Keycloak hostname file Portico writes on Apply: present and empty
# from the start, the quadlet loads it unconditionally.
if [ ! -e "$ROOT/var/lib/portico/keycloak.hostname.env" ]; then e=$(mktemp); put 0644 portico portico "$e" /var/lib/portico/keycloak.hostname.env; rm -f "$e"; fi

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
# SSH: keys only, wheel only. On a host of your own the policy would lock
# out a session that came in with a password or as a user outside wheel:
# it is installed there only when a wheel user already has a key.
ssh_policy_safe() {
  local u home
  for u in $(getent group wheel | cut -d: -f4 | tr ',' ' '); do
    home=$(getent passwd "$u" | cut -d: -f6)
    [ -n "$home" ] && [ -s "$home/.ssh/authorized_keys" ] && return 0
  done
  return 1
}
if [ $START -eq 0 ] || ssh_policy_safe; then
  put 0600 root root "$HERE/sshd/00-portico.conf" /etc/ssh/sshd_config.d/00-portico.conf
else
  log "warning: no user in wheel has an SSH key: the SSH policy (keys only, wheel only) is NOT installed, so this session is not locked out; add a key to a wheel user and run install.sh again"
fi
put 0644 root root "$HERE/sshd/portico-network-online.conf" /etc/systemd/system/sshd.service.d/portico.conf
put 0644 root root "$HERE/sysctl/60-portico.conf" /etc/sysctl.d/60-portico.conf
# This script itself, for the first boot of an appliance.
put 0755 root root "$SELF" /usr/local/sbin/portico-install
put 0644 root root "$HERE/portico-firstboot.service" /etc/systemd/system/portico-firstboot.service
# On a host of your own the secrets are generated now; the image build
# leaves them to the first boot.
if [ $START -eq 1 ] || [ $RENDER_SECRETS -eq 1 ]; then generate_secrets; fi

# PostgreSQL and Keycloak are restarted when a quadlet or the network
# changed (a quadlet edit takes effect at the next start only); started
# otherwise, so an unchanged re-run does not interrupt logins.
containers_changed() { case "$CHANGED" in *" /etc/containers/systemd/"*) return 0 ;; esac; return 1; }

if [ $RENDER -eq 1 ]; then
  if containers_changed; then log "would restart: postgres keycloak"; fi
  log "rendered under $ROOT"; exit 0
fi

# 8. Apply: SELinux labels, kernel parameters, units, firewall, sshd.
restorecon -R /etc/portico /usr/local/bin/portico /usr/local/sbin/portico-install /etc/containers/systemd /etc/systemd/system /var/lib/portico
sysctl -q --system >/dev/null
systemctl daemon-reload
systemctl enable firewalld portico keycloak-hostname.path portico-firstboot >/dev/null 2>&1 || true
if sshd -t 2>/dev/null; then systemctl reload sshd 2>/dev/null || true; fi
if [ $START -eq 1 ]; then
  systemctl start firewalld
  configure_firewall
  if containers_changed; then
    log "a quadlet changed: restarting PostgreSQL and Keycloak"
    systemctl restart postgres.service keycloak.service
  fi
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
# Image build: the firewall's permanent configuration now, the rest at the first boot.
configure_firewall
log "installed without starting the services (--no-start): portico-firstboot.service completes the setup at the first boot"
