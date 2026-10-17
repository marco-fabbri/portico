#!/usr/bin/env bash
# Stop Portico on the lab VM and bring the nginx stand-in back. Files stay.
set -euo pipefail
: "${LAB_VM_IP:?source lab/env.sh first}"
ssh -o BatchMode=yes -o ConnectTimeout=5 "lab@$LAB_VM_IP" sudo bash -c "'systemctl disable --now portico; systemctl enable --now nginx'"
echo "nginx stand-in restored"
