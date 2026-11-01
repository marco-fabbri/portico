#!/usr/bin/env bash
# Repository checks run by the CI on every change (no lab needed):
#   - shellcheck on the installer;
#   - the README presents the newest release notes (badge, release box,
#     notes link), so a tag never goes out with the README behind (0.3.0);
#   - every action is pinned on a major that runs on Node 24;
#   - no identifier of the lab in a tracked file: the lab is a borrowed
#     cluster and nothing that describes it is published (CLAUDE.md).
set -u
cd "$(dirname "$0")/.." || exit 1
fail=0; ok() { echo "ok   $1"; }; ko() { echo "FAIL $1"; fail=1; }

if shellcheck -x deploy/install.sh; then ok "shellcheck deploy/install.sh"; else ko "shellcheck deploy/install.sh"; fi

latest=$(find docs/releases -name 'v*.md' | sed 's#.*/v##; s#\.md$##' | sort -V | tail -1)
if grep -q "badge/release-v$latest-" README.md && grep -q "Current release, $latest\." README.md && grep -q "docs/releases/v$latest.md" README.md; then ok "README presents the newest release notes (v$latest)"; else ko "README is behind docs/releases/v$latest.md (badge, release box or notes link)"; fi

old_node=""
while read -r a min; do
  while read -r v; do
    if [[ "$v" != *.*.* ]] || [ "${v%%.*}" -lt "$min" ]; then old_node="$old_node $a@v$v"; fi
  done < <(grep -hoE "uses: $a@[0-9a-f]{40} # v[0-9]+[.0-9]*" .github/workflows/*.yml | sed 's/.*# v//')
done <<'TABLE'
actions/checkout 5
actions/setup-go 6
actions/upload-artifact 6
actions/download-artifact 7
hashicorp/setup-packer 3
opentofu/setup-opentofu 2
softprops/action-gh-release 3
TABLE
if [ -z "$old_node" ]; then ok "every action runs on Node 24, pinned with its exact version"; else ko "actions on Node 20 or without an exact version:$old_node"; fi

# The lab's identifiers: its address plan, its cluster and host names, the
# directories that hold its scripts and write-ups. Tests use 10.0.x.x,
# documentation the RFC 5737 ranges.
if hits=$(git grep -n -I -E '10\.23\.[0-9]+\.[0-9]+|NTNX-0[0-9]|proxmox0[0-9]|portico-acceptance|\blab/[a-z]|docs/lab/|superpowers/plans/' -- . ':!scripts/check-repo.sh') && [ -n "$hits" ]; then ko "lab identifiers in tracked files:"; printf '%s\n' "$hits" | head -20; else ok "no lab identifier in tracked files"; fi

if [ $fail -eq 0 ]; then echo PASS; else echo FAIL; exit 1; fi
