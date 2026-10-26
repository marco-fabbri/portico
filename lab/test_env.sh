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
echo PASS
