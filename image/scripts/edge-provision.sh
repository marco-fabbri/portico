#!/usr/bin/env bash
# The edge router image (prefix-routing design 4.4), run as root inside
# the build VM by edge.pkr.hcl in three steps:
#   install   FRR and nftables; cloud-init with no default user and no
#             network rendering; firewalld out (nftables is the firewall)
#   cleanup   close the image: no cache, log, host key, machine id, build
#             network, scanner
#   poweroff  Packer's shutdown command: the build user, its sudoers file
#             and the SSH server go, then power off (detached, so the SSH
#             session may close)
set -euo pipefail
B=/tmp/portico-build

install_edge() {
  dnf -y -q install frr nftables openscap-scanner scap-security-guide
  # firewalld would manage nftables' tables beside the edge's own; the
  # edge's firewall is the one table its cloud-init writes.
  dnf -y -q remove firewalld >/dev/null 2>&1 || true
  sed -i 's/^bgpd=no/bgpd=yes/' /etc/frr/daemons
  systemctl enable frr nftables >/dev/null 2>&1
  # FRR's package requires net-snmp (its SNMP module links the agent
  # library); the daemon itself never runs on the edge.
  systemctl mask snmpd >/dev/null 2>&1 || true
  # As the appliance's kickstart: journald alone, persistent (CIS 4.2:
  # not both journald and rsyslog); the remote journal socket masked;
  # /tmp a tmpfs through the systemd unit.
  dnf -y -q remove rsyslog >/dev/null 2>&1 || true
  mkdir -p /etc/systemd/journald.conf.d /var/log/journal
  cat > /etc/systemd/journald.conf.d/portico.conf <<'JD'
[Journal]
Storage=persistent
Compress=yes
SystemMaxUse=256M
JD
  systemctl mask systemd-journal-remote.socket >/dev/null 2>&1 || true
  systemctl unmask tmp.mount >/dev/null 2>&1 || true
  mkdir -p /etc/systemd/system/tmp.mount.d
  cat > /etc/systemd/system/tmp.mount.d/portico.conf <<'TM'
[Mount]
Options=mode=1777,strictatime,nosuid,nodev,noexec
TM
  systemctl daemon-reload
  systemctl enable tmp.mount >/dev/null 2>&1
  # The rule wants /tmp mounted now, not at the next boot: the build's
  # files move aside while the tmpfs goes on, then back onto it.
  if ! mountpoint -q /tmp; then
    mv "$B" /root/portico-build
    systemctl start tmp.mount
    mv /root/portico-build "$B"
  fi
  # cloud-init: the config drive Portico attaches carries the user data;
  # no default user, no password login, no network rendering (the user
  # data's bootcmd writes eth0's profile), no SSH host keys to generate.
  cat > /etc/cloud/cloud.cfg.d/90-portico-edge.cfg <<'CI'
datasource_list: [ ConfigDrive, NoCloud, None ]
network:
  config: disabled
users: []
disable_root: true
ssh_pwauth: false
ssh_genkeytypes: []
CI
  # NetworkManager makes no DHCP profile of its own for eth0: the profile
  # from the user data is the only one (write-up 30 for the appliance).
  cat > /etc/NetworkManager/conf.d/90-portico-no-auto-default.conf <<'NM'
[main]
no-auto-default=*
NM
  echo portico-edge > /etc/hostname
}

cleanup_edge() {
  # The hardening must leave the router's packages alone (a remediation
  # once removed FRR: package_net-snmp_removed, which FRR requires).
  rpm -q frr nftables >/dev/null || { echo "edge: FRR or nftables is missing after the hardening" >&2; exit 1; }
  # The scanner and its content are build tools: the edge has no login to
  # run them from (the report ships with the release instead). The CIS
  # remediation installs firewalld back (package_firewalld_installed, set
  # aside in the tailoring): out again, nftables is the firewall.
  dnf -y -q --setopt=clean_requirements_on_remove=False remove openscap-scanner scap-security-guide firewalld >/dev/null 2>&1 || true
  dnf -y -q clean all
  rm -rf /var/cache/dnf /var/lib/dnf/history*
  rm -f /etc/ssh/ssh_host_*
  truncate -s 0 /etc/machine-id
  mkdir -p /var/lib/dbus && rm -f /var/lib/dbus/machine-id && ln -s /etc/machine-id /var/lib/dbus/machine-id
  cloud-init clean --logs --seed
  rm -f /etc/NetworkManager/system-connections/*.nmconnection
  rm -f /root/.bash_history /home/build/.bash_history
  journalctl --rotate >/dev/null 2>&1 || true
  journalctl --vacuum-time=1s >/dev/null 2>&1 || true
  find /var/log -type f -name '*.log' -exec truncate -s 0 {} + 2>/dev/null || true
  find /var/log -type f \( -name '*.gz' -o -name '*-????????' \) -delete 2>/dev/null || true
  rm -rf "$B/oscap" "$B"/oscap-*
  restorecon -R /etc >/dev/null 2>&1 || true
  sync
  fstrim -av >/dev/null 2>&1 || true
}

poweroff_edge() {
  nohup bash -c 'sleep 2; userdel -rf build 2>/dev/null; rm -f /etc/sudoers.d/build /etc/sudoers.d/90-cloud-init-users; dnf -y -q --setopt=clean_requirements_on_remove=False remove openssh-server >/dev/null 2>&1; rm -rf /tmp/portico-build; sync; shutdown -P now' >/dev/null 2>&1 &
}

case "${1:-}" in
  install) install_edge ;;
  cleanup) cleanup_edge ;;
  poweroff) poweroff_edge ;;
  *) echo "usage: edge-provision.sh install|cleanup|poweroff" >&2; exit 2 ;;
esac
