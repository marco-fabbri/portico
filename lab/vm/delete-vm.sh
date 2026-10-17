#!/usr/bin/env bash
# Power off and delete the lab VM portico-lab. Keeps the image.
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_URL:?source lab/env.sh first}"
# shellcheck source=lab/vm/lib.sh
source lab/vm/lib.sh
vm=$(vm_extid portico-lab)
[ -n "$vm" ] || { echo "no VM named portico-lab"; exit 0; }
state=$(lab/pc.sh GET "/api/vmm/v4.0/ahv/config/vms/$vm" 2>/dev/null | jq -r .data.powerState)
if [ "$state" = "ON" ]; then t=$(vm_action "$vm" power-off); wait_task "$t" >/dev/null; fi
etag=$(etag_of "/api/vmm/v4.0/ahv/config/vms/$vm")
IF_MATCH="$etag" lab/pc.sh DELETE "/api/vmm/v4.0/ahv/config/vms/$vm" 2>/dev/null | jq -r '.data.extId' | { read -r t; wait_task "$t"; }
echo "deleted portico-lab"
