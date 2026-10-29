#!/usr/bin/env bash
# Checks lab/env.sh: it must refuse to run without the secrets file, and
# must export the lab variables with a clean password when the file exists.
set -u
cd "$(dirname "$0")/.." || exit 1
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
out=$(HOME="$tmp" bash -c 'source lab/env.sh' 2>&1); rc=$?
[ "$rc" -eq 1 ] || { echo "FAIL: expected exit 1 without secrets, got $rc"; exit 1; }
grep -q 'secrets file not found' <<<"$out" || { echo "FAIL: missing message, got: $out"; exit 1; }
# shellcheck source=lab/env.sh
source lab/env.sh
[ -n "$PC_PASS" ] || { echo "FAIL: PC_PASS empty"; exit 1; }
[ "$PC_URL" = "https://10.23.210.175:9440" ] || { echo "FAIL: PC_URL=$PC_URL"; exit 1; }
[ "$LAB_VM_IP" = "10.23.210.191" ] || { echo "FAIL: LAB_VM_IP=$LAB_VM_IP"; exit 1; }
[[ "$PC_PASS" != *$'\n'* ]] || { echo "FAIL: newline in password"; exit 1; }
# The second site's Prism Central has its own password file: with PC_URL on
# NYC1 the password comes from nyc1-pw, never from prism-pw.
mkdir -p "$tmp/s"; printf 'site-one\n' > "$tmp/s/prism-pw"; printf 'site-two\n' > "$tmp/s/nyc1-pw"
two=$(PORTICO_SECRETS="$tmp/s" PC_URL=https://10.23.210.185:9440 bash -c 'source lab/env.sh && echo "$PC_PASS"')
[ "$two" = site-two ] || { echo "FAIL: NYC1 must read nyc1-pw"; exit 1; }
one=$(PORTICO_SECRETS="$tmp/s" bash -c 'unset PC_URL; source lab/env.sh && echo "$PC_PASS"')
[ "$one" = site-one ] || { echo "FAIL: NTNX-01 must read prism-pw"; exit 1; }
rm "$tmp/s/nyc1-pw"
out=$(PORTICO_SECRETS="$tmp/s" PC_URL=https://10.23.210.185:9440 bash -c 'source lab/env.sh' 2>&1); rc=$?
[ "$rc" -eq 1 ] || { echo "FAIL: NYC1 without nyc1-pw must exit 1, got $rc"; exit 1; }
grep -q 'nyc1-pw' <<<"$out" || { echo "FAIL: missing nyc1-pw in message, got: $out"; exit 1; }
echo PASS
