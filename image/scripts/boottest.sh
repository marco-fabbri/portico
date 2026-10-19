#!/usr/bin/env bash
# shellcheck disable=SC2016  # the single-quoted commands run on the appliance over SSH
# Boot test of the built image on a host with KVM (the CI runner): a
# NoCloud seed gives the appliance an SSH user and the static network the
# OpenTofu module would write; the test then checks what the first boot
# must leave behind. Serial console on boottest-serial.log.
#   image/scripts/boottest.sh output/portico-<version>.qcow2
set -euo pipefail
IMG=$(cd "$(dirname "${1:?qcow2}")" && pwd)/$(basename "$1")
cd "$(dirname "$0")/.."
QPID=""
W=$(mktemp -d); trap 'rm -rf "$W"; [ -n "$QPID" ] && kill "$QPID" 2>/dev/null || true' EXIT
ssh-keygen -q -t ed25519 -N '' -f "$W/key"
# The user data is the one deploy/tofu/nutanix would send (rendered from its
# template with tofu console, so a template defect fails here): an admin
# user with the key, the static network of QEMU's user-mode subnet.
KEY=$(cat "$W/key.pub")
if command -v tofu >/dev/null; then
  # scripts/render-user-data renders the module's template with no provider
  # (tofu console inside the module waited on the Nutanix provider on the
  # runner); a timeout guards it where the command exists.
  TO=""; command -v timeout >/dev/null && TO="timeout 120"
  KEYFILE=$'[connection]\nid=eth0\ntype=ethernet\ninterface-name=eth0\nautoconnect=true\n\n[ipv4]\nmethod=manual\naddress1=10.0.2.15/24,10.0.2.2\ndns=10.0.2.3;\n\n[ipv6]\nmethod=disabled\n'
  (cd scripts/render-user-data && $TO tofu init -input=false -backend=false >/dev/null 2>&1 && printf '%s\n' 'local.user_data' | $TO tofu console -input=false -var hostname=boottest -var nic=eth0 -var "ssh_public_key=$KEY" -var "keyfile=$KEYFILE" 2>/dev/null | sed 's/^<<EOT$//; s/^EOT$//' > "$W/user-data")
  grep -q '^#cloud-config' "$W/user-data" || { echo "boottest: the module's template did not render"; cat "$W/user-data"; exit 1; }
  echo "boottest: user data rendered from deploy/tofu/nutanix/cloud-init.tftpl"
else
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
      - $KEY
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
  echo "boottest: user data written by hand (no tofu on this host)"
fi
# SEED_FORMAT=nocloud (default) is the cidata disk; configdrive is what AHV
# attaches (CONFIG_DRIVE_V2): label config-2, openstack/latest/user_data
# and meta_data.json.
case "${SEED_FORMAT:-nocloud}" in
  configdrive)
    mkdir -p "$W/cd/openstack/latest"
    cp "$W/user-data" "$W/cd/openstack/latest/user_data"
    printf '{"uuid": "11111111-2222-3333-4444-555555555555", "hostname": "boottest", "name": "boottest"}\n' > "$W/cd/openstack/latest/meta_data.json"
    genisoimage -quiet -output "$W/seed.iso" -volid config-2 -joliet -rock "$W/cd" ;;
  *)
    printf 'instance-id: boottest-1\nlocal-hostname: boottest\n' > "$W/meta-data"
    genisoimage -quiet -output "$W/seed.iso" -volid cidata -joliet -rock "$W/user-data" "$W/meta-data" ;;
esac
echo "boottest: seed format ${SEED_FORMAT:-nocloud}"
qemu-system-x86_64 -enable-kvm -cpu host -m 3072 -smp 2 -display none -daemonize -pidfile "$W/qemu.pid" \
  -snapshot -drive "file=$IMG,if=virtio,discard=unmap" -cdrom "$W/seed.iso" \
  -netdev user,id=n0,restrict=on,hostfwd=tcp:127.0.0.1:2222-:22 -device virtio-net,netdev=n0 \
  -serial "file:boottest-serial-${SEED_FORMAT:-nocloud}.log"
QPID=$(cat "$W/qemu.pid")
SSH=(ssh -i "$W/key" -p 2222 -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 admin@127.0.0.1)
echo "boottest: waiting for SSH"
for i in $(seq 1 60); do "${SSH[@]}" true 2>/dev/null && break; sleep 5; [ "$i" -eq 60 ] && { echo "boottest: no SSH after 300 s"; tail -n 80 "boottest-serial-${SEED_FORMAT:-nocloud}.log"; exit 1; }; done
echo "boottest: SSH up after $((i * 5)) s"
"${SSH[@]}" 'sudo cloud-init status --wait >/dev/null 2>&1; echo "cloud-init: $(cloud-init status 2>/dev/null | head -1)"; for _ in $(seq 1 60); do systemctl is-active -q portico-firstboot && break; sleep 2; done' || true
fail=0; ok() { echo "ok   $1"; }; ko() { echo "FAIL $1"; fail=1; }
check() { local label=$1 cmd=$2; if "${SSH[@]}" "$cmd" >/dev/null 2>&1; then ok "$label"; else ko "$label"; fi; }
check "cloud-init target enabled and ran" 'systemctl is-enabled -q cloud-init.target && cloud-init status 2>/dev/null | grep -q done'
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
check "the build user is gone (no account, no sudoers file, no home)" '! id build >/dev/null 2>&1 && ! sudo test -e /etc/sudoers.d/build && ! sudo test -d /home/build'
check "postgres and keycloak units present (containers pull at first start)" 'systemctl list-unit-files postgres.service keycloak.service | grep -c service | grep -q 2'
"${SSH[@]}" 'echo "== units:"; for u in portico-firstboot portico keycloak postgres firewalld getty@tty1 cloud-init; do printf "%s=%s " "$u" "$(systemctl is-active $u)"; done; echo; echo "== journal errors:"; sudo journalctl -p err -b --no-pager | tail -n 12' 2>/dev/null | cut -c1-160 || true
"${SSH[@]}" 'sudo shutdown -P now' >/dev/null 2>&1 || true
sleep 5
[ $fail -eq 0 ] && { echo "boottest: PASS (${SEED_FORMAT:-nocloud})"; exit 0; }
echo "boottest: FAIL (${SEED_FORMAT:-nocloud})"; tail -n 60 "boottest-serial-${SEED_FORMAT:-nocloud}.log"; "${SSH[@]}" 'sudo cloud-init status --long; sudo tail -n 40 /var/log/cloud-init.log' 2>/dev/null | cut -c1-200 || true; exit 1
