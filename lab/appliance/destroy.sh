#!/usr/bin/env bash
# Lab: remove the appliance VM and its image from NTNX-01, stop the HTTP
# server on the lab VM. Requires `source lab/env.sh`. Lab only.
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${LAB_VM_IP:?source lab/env.sh first}" "${PC_PASS:?}"
A=lab/appliance
export TF_VAR_nutanix_password="$PC_PASS"
if [ -s "$A/terraform.tfstate" ]; then
  tofu -chdir=deploy/tofu/nutanix destroy -input=false -auto-approve -var-file="$PWD/$A/terraform.tfvars" -state="$PWD/$A/terraform.tfstate"
fi
if [ -s "$A/.stage" ]; then
  stage=$(cat "$A/.stage")
  ssh -o BatchMode=yes "lab@$LAB_VM_IP" "[ -s '$stage/http.pid' ] && kill \$(cat '$stage/http.pid') 2>/dev/null; sudo firewall-cmd -q --remove-port=8000/tcp; rm -rf '$stage'" || true
  rm -f "$A/.stage"
fi
echo "appliance removed"
