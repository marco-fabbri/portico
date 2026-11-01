#!/usr/bin/env bash
# Boot test of the edge router image on a host with KVM (the CI runner,
# the build host): the image boots with the config drive AHV would attach
# (CONFIG_DRIVE_V2) carrying the user data Portico sends to an edge
# (edge-boottest-user-data.yaml, kept equal to EdgeUserData's output by a
# test of internal/peering), on QEMU's user-mode network. The edge has no
# login, so the test reads the serial console: the state line the
# cloud-init prints last, and that nothing answers on port 22.
#   image/scripts/edge-boottest.sh output-edge/portico-edge-<version>.qcow2
set -euo pipefail
IMG=$(cd "$(dirname "${1:?qcow2}")" && pwd)/$(basename "$1")
cd "$(dirname "$0")/.."
LOG=boottest-edge-serial.log
QPID=""
W=$(mktemp -d); trap 'rm -rf "$W"; [ -n "$QPID" ] && kill "$QPID" 2>/dev/null || true' EXIT
mkdir -p "$W/cd/openstack/latest"
cp scripts/edge-boottest-user-data.yaml "$W/cd/openstack/latest/user_data"
printf '{"uuid": "11111111-2222-3333-4444-555555555555", "hostname": "portico-s1-edge", "name": "portico-s1-edge"}\n' > "$W/cd/openstack/latest/meta_data.json"
genisoimage -quiet -output "$W/seed.iso" -volid config-2 -joliet -rock "$W/cd"
rm -f "$LOG"
qemu-system-x86_64 -enable-kvm -cpu host -m 1024 -smp 1 -display none -daemonize -pidfile "$W/qemu.pid" \
  -snapshot -drive "file=$IMG,if=virtio,discard=unmap" -cdrom "$W/seed.iso" \
  -netdev user,id=n0,restrict=on,hostfwd=tcp:127.0.0.1:2223-:22 -device virtio-net,netdev=n0 \
  -serial "file:$LOG"
QPID=$(cat "$W/qemu.pid")
echo "edge-boottest: waiting for the ready line on the serial console"
for i in $(seq 1 60); do
  grep -q '^portico-edge ready' "$LOG" 2>/dev/null && break
  sleep 5
  [ "$i" -eq 60 ] && { echo "edge-boottest: no ready line after 300 s"; tail -n 80 "$LOG"; exit 1; }
done
echo "edge-boottest: ready after $((i * 5)) s"
# (the login prompt may share the line with the state)
state=$(grep 'portico-edge portico-s1-edge:' "$LOG" | tail -n 1 | tr -d '\r' | sed 's/.*portico-edge portico-s1-edge:/portico-edge portico-s1-edge:/')
echo "edge-boottest: $state"
sed -n '/portico-edge portico-s1-edge:/,/^portico-edge ready/p' "$LOG" | grep -v 'portico-edge portico-s1-edge:\|^portico-edge ready' | head -n 8 | sed 's/^/edge-boottest:   /' || true
fail=0; ok() { echo "ok   $1"; }; ko() { echo "FAIL $1"; fail=1; }
check() { local label=$1 pattern=$2; if grep -qE "$pattern" <<<"$state"; then ok "$label"; else ko "$label"; fi; }
check "FRR runs the BGP configuration of the user data" 'router bgp 65020;neighbor 10\.0\.2\.2 remote-as 65010'
check "forwarding on" 'forward=1'
check "nftables active" 'nftables=active'
check "frr active" 'frr=active'
check "no sshd unit active" 'sshd=(inactive|)($|;)'
# The image has no login (goal 3): the build user is gone and no account
# holds a password hash.
check "no build user" 'users=none'
check "no password hash" 'hashes=0'
# An SSH server answers with its banner at once; a closed port gives
# nothing (QEMU accepts on the host side and closes when the guest refuses).
banner=$(timeout 5 bash -c 'exec 3<>/dev/tcp/127.0.0.1/2223 && head -c 4 <&3' 2>/dev/null || true)
if [ "$banner" = "SSH-" ]; then ko "port 22 does not answer"; else ok "port 22 does not answer"; fi
if grep -qE 'cloud-init.*(Traceback|failed to run)' "$LOG"; then ko "cloud-init ran without a failure"; else ok "cloud-init ran without a failure"; fi
kill "$QPID" 2>/dev/null || true; QPID=""
[ $fail -eq 0 ] && { echo "edge-boottest: PASS"; exit 0; }
echo "edge-boottest: FAIL"; tail -n 60 "$LOG"; exit 1
