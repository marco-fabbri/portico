#!/usr/bin/env bash
# shellcheck disable=SC2016  # the PATH line is written literally for the login shell
# Provision the build host (Rocky Linux 9): KVM, QEMU, Packer, OpenTofu,
# Go, gh, the image tooling, and a clone of the repository for the build
# user. Idempotent; run as root by lab/build-host/deploy.sh.
set -euo pipefail
GO_VERSION="${GO_VERSION:-1.27.2}"
# An interrupted first run leaves dnf's cache inconsistent ("No such file" on a cached rpm).
dnf -y -q clean packages >/dev/null 2>&1 || true
dnf -y -q install qemu-kvm qemu-img genisoimage git curl tar jq openssl dnf-plugins-core >/dev/null
[ -c /dev/kvm ] || { echo "no /dev/kvm: the VM needs CPU type host and nested virtualization on the Proxmox host" >&2; exit 1; }
usermod -aG kvm build 2>/dev/null || true
# Rocky ships QEMU as /usr/libexec/qemu-kvm; Packer and the boot test call qemu-system-x86_64.
[ -x /usr/local/bin/qemu-system-x86_64 ] || ln -s /usr/libexec/qemu-kvm /usr/local/bin/qemu-system-x86_64
# HashiCorp (Packer) and OpenTofu repositories, GitHub CLI.
[ -r /etc/yum.repos.d/hashicorp.repo ] || dnf config-manager --add-repo https://rpm.releases.hashicorp.com/RHEL/hashicorp.repo >/dev/null
[ -r /etc/yum.repos.d/opentofu.repo ] || cat > /etc/yum.repos.d/opentofu.repo <<'R'
[opentofu]
name=opentofu
baseurl=https://packages.opentofu.org/opentofu/tofu/rpm_any/rpm_any/$basearch
repo_gpgcheck=0
gpgcheck=1
enabled=1
gpgkey=https://get.opentofu.org/opentofu.gpg
       https://packages.opentofu.org/opentofu/tofu/gpgkey
R
[ -r /etc/yum.repos.d/gh-cli.repo ] || dnf config-manager --add-repo https://cli.github.com/packages/rpm/gh-cli.repo >/dev/null
dnf -y -q install packer tofu gh >/dev/null
# Go from go.dev (the distribution's is older than the module needs).
if ! /usr/local/go/bin/go version 2>/dev/null | grep -q "go$GO_VERSION"; then
  tgz=$(mktemp)
  curl -fsSL "https://go.dev/dl/go$GO_VERSION.linux-amd64.tar.gz" -o "$tgz"
  rm -rf /usr/local/go && tar -C /usr/local -xzf "$tgz" && rm -f "$tgz"
fi
grep -q '/usr/local/go/bin' /etc/profile.d/go.sh 2>/dev/null || echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' > /etc/profile.d/go.sh
# The repository, for the build user.
sudo -u build bash -c 'cd ~ && { [ -d portico ] && git -C portico pull -q || git clone -q https://github.com/marco-fabbri/portico.git; }'
echo "provisioned: $(packer version | head -1), $(tofu version | head -1), $(/usr/local/go/bin/go version), kvm $( [ -c /dev/kvm ] && echo yes )"
