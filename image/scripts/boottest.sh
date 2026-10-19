#!/usr/bin/env bash
# Boot test of the built image on a host with KVM (the CI runner): a
# NoCloud seed gives the appliance an SSH user and the static network the
# OpenTofu module would write; the test then checks what the first boot
# must leave behind. Serial console on boottest-serial.log.
#   image/scripts/boottest.sh output/portico-<version>.qcow2
set -euo pipefail
IMG=${1:?qcow2}
cd "$(dirname "$0")/.."
W=$(mktemp -d); trap 'rm -rf "$W"; kill "$QPID" 2>/dev/null || true' EXIT
ssh-keygen -q -t ed25519 -N '' -f "$W/key"
cat > "$W/user-data" <<UD
#cloud-config
hostname: boottest
users:
  - name: admin
    groups: [wheel]
    sudo: ["ALL=(ALL) NOPASSWD:ALL"]
    shell: /bin/bash
    lock_passwd: true
    ssh_authorized_keys:
      - $(cat "$W/key.pub")
write_files:
  - path: /etc/NetworkManager/system-connections/eth0.nmconnection
    permissions: "0600"
    owner: root:root
    content: |
      [connection]
      id=eth0
      type=ethernet
      interface-name=eth0
      autoconnect=true

      [ipv4]
      method=manual
      address1=10.0.2.15/24,10.0.2.2
      dns=10.0.2.3;

      [ipv6]
      method=disabled
runcmd:
  - [nmcli, connection, reload]
  - [nmcli, connection, up, eth0]
UD
printf 'instance-id: boottest-1\nlocal-hostname: boottest\n' > "$W/meta-data"
genisoimage -quiet -output "$W/seed.iso" -volid cidata -joliet -rock "$W/user-data" "$W/meta-data"
qemu-system-x86_64 -enable-kvm -cpu host -m 3072 -smp 2 -display none -daemonize -pidfile "$W/qemu.pid" \
  -snapshot -drive "file=$IMG,if=virtio,discard=unmap" -cdrom "$W/seed.iso" \
  -netdev user,id=n0,hostfwd=tcp:127.0.0.1:2222-:22 -device virtio-net,netdev=n0 \
  -serial file:boottest-serial.log
QPID=$(cat "$W/qemu.pid")
SSH=(ssh -i "$W/key" -p 2222 -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 admin@127.0.0.1)
echo "boottest: waiting for SSH"
for i in $(seq 1 60); do "${SSH[@]}" true 2>/dev/null && break; sleep 5; [ "$i" -eq 60 ] && { echo "boottest: no SSH after 300 s"; tail -n 80 boottest-serial.log; exit 1; }; done
echo "boottest: SSH up after $((i * 5)) s"
"${SSH[@]}" 'sudo cloud-init status --wait >/dev/null 2>&1; echo "cloud-init: $(cloud-init status 2>/dev/null | head -1)"; for _ in $(seq 1 60); do systemctl is-active -q portico-firstboot && break; sleep 2; done' || true
fail=0; ok() { echo "ok   $1"; }; ko() { echo "FAIL $1"; fail=1; }
check() { local label=$1 cmd=$2; if "${SSH[@]}" "$cmd" >/dev/null 2>&1; then ok "$label"; else ko "$label"; fi; }
check "first boot ran once (unit active, condition now false)" 'systemctl is-active -q portico-firstboot && sudo test -s /etc/portico/keycloak.env'
check "TLS pairs generated at first boot" 'sudo test -s /etc/portico/tls/internal.key && sudo test -s /etc/portico/tls/public.key'
check "keycloak.env is 0600 root" '[ "$(sudo stat -c %a:%U /etc/portico/keycloak.env)" = "600:root" ]'
check "portico.service active" 'systemctl is-active -q portico'
check "admin listener answers on 8443 (setup mode)" 'curl -sk https://127.0.0.1:8443/healthz | grep -q .'
check "firewalld active with the portico service, no cockpit" 'systemctl is-active -q firewalld && sudo firewall-cmd --list-services | grep -q portico && ! sudo firewall-cmd --list-services | grep -q cockpit'
check "tty1 override runs the console" 'grep -q "portico console" /etc/systemd/system/getty@tty1.service.d/override.conf && systemctl is-active -q getty@tty1'
check "interface is eth0 with the static address" 'ip -4 addr show eth0 | grep -q 10.0.2.15'
check "every LVM volume mounted" 'for m in /var /var/log /var/log/audit /var/tmp /home /var/lib/containers /var/lib/portico; do mountpoint -q "$m" || exit 1; done'
check "/tmp noexec tmpfs, /dev/shm noexec" 'findmnt -no OPTIONS /tmp | grep -q noexec && findmnt -no OPTIONS /dev/shm | grep -q noexec'
check "SELinux enforcing" '[ "$(getenforce)" = Enforcing ]'
check "no root password, no password SSH" 'sudo grep -q "^root:[!*]" /etc/shadow && sudo sshd -T 2>/dev/null | grep -qi "^passwordauthentication no"'
check "postgres and keycloak units present (containers pull at first start)" 'systemctl list-unit-files postgres.service keycloak.service | grep -c service | grep -q 2'
"${SSH[@]}" 'echo "== units:"; for u in portico-firstboot portico keycloak postgres firewalld getty@tty1 cloud-init; do printf "%s=%s " "$u" "$(systemctl is-active $u)"; done; echo; echo "== journal errors:"; sudo journalctl -p err -b --no-pager | tail -n 12' 2>/dev/null | cut -c1-160 || true
"${SSH[@]}" 'sudo shutdown -P now' >/dev/null 2>&1 || true
sleep 5
[ $fail -eq 0 ] && { echo "boottest: PASS"; exit 0; }
echo "boottest: FAIL"; tail -n 60 boottest-serial.log; exit 1
