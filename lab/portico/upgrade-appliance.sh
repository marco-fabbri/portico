#!/usr/bin/env bash
# Upgrade the appliance in place with the binary of this checkout (design
# 4.9: re-running the installer upgrades and keeps every secret and the
# store). Lab only; Portico restarts and tenant sessions end, so ask first.
#   source lab/env.sh && lab/portico/upgrade-appliance.sh
# Needs LAB_VM_IP and LAB_SSH_USER (the appliance's admin user, sudo
# without a password) and optionally LAB_SSH_OPTS. Before the installer
# runs, Portico is stopped and the store and the master key are copied to
# /var/lib/portico/backup-<version>-<time>/ on the appliance itself: the
# master key never leaves it.
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_VM_IP:?source lab/env.sh first}" "${LAB_SSH_USER:?set LAB_SSH_USER (admin on the appliance)}"
# shellcheck disable=SC2206 # LAB_SSH_OPTS is a list of options
O=(-o BatchMode=yes -o ConnectTimeout=5 -o LogLevel=ERROR ${LAB_SSH_OPTS:-})
T="$LAB_SSH_USER@$LAB_VM_IP"
version="dev-$(git rev-parse --short HEAD)"
[ -z "$(git status --porcelain --untracked-files=no)" ] || echo "upgrade-appliance.sh: warning: the checkout has uncommitted changes; $version does not name them" >&2

tmp=$(mktemp -d)
stage=$(ssh "${O[@]}" "$T" 'umask 077; mktemp -d "$HOME/portico-upgrade.XXXXXX"')
trap 'rm -rf "$tmp"; ssh "${O[@]}" "$T" rm -rf "$stage" || true' EXIT
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-X main.version=$version" -o "$tmp/portico" ./cmd/portico
scp -q -r "${O[@]}" "$tmp/portico" deploy "$T:$stage/"
echo "upgrade-appliance.sh: $(ssh "${O[@]}" "$T" /usr/local/bin/portico version) -> $version"
# shellcheck disable=SC2087 # $stage and $version expand here, the rest on the appliance
ssh "${O[@]}" "$T" bash -s <<REMOTE
set -euo pipefail
b=/var/lib/portico/backup-\$(/usr/local/bin/portico version | awk '{print \$2}')-\$(date +%Y%m%d-%H%M%S)
sudo -n systemctl stop portico
sudo -n install -d -m 0700 -o root -g root "\$b"
sudo -n sh -c 'cp -a /var/lib/portico/portico.db* /var/lib/portico/master.key "\$1"/' sh "\$b"
echo "upgrade-appliance.sh: backup in \$b: \$(sudo -n ls "\$b" | tr '\n' ' ')"
sudo -n bash "$stage/deploy/install.sh" --binary "$stage/portico" 2>&1 | grep -v '^\s*$' | tail -5
[ "\$(/usr/local/bin/portico version)" = "portico $version" ] || { echo "upgrade-appliance.sh: the installed binary is not $version" >&2; exit 1; }
systemctl is-active -q portico && echo "upgrade-appliance.sh: portico $version active"
REMOTE
