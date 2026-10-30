#!/usr/bin/env bash
# Lab: remove the appliance VM and its image from NTNX-01, and stop the
# HTTP server on the image host when deploy.sh started one there
# (LAB_IMAGE_HOST and LAB_IMAGE_USER, as deploy.sh). Requires
# `source lab/env.sh`. LAB_APPLIANCE_STATE names a second appliance's
# state directory, as for deploy.sh. Lab only.
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
: "${PC_PASS:?source lab/env.sh first}"
IMAGE_HOST="${LAB_IMAGE_HOST:-}"
IMAGE_USER="${LAB_IMAGE_USER:-lab}"
A="${LAB_APPLIANCE_STATE:-lab/appliance}"
export TF_VAR_nutanix_password="$PC_PASS"
if [ -s "$A/terraform.tfstate" ]; then
  tofu -chdir=deploy/tofu/nutanix destroy -input=false -auto-approve -var-file="$PWD/$A/terraform.tfvars" -state="$PWD/$A/terraform.tfstate"
fi
if [ -s "$A/.stage" ] && [ -n "$IMAGE_HOST" ]; then
  stage=$(cat "$A/.stage")
  ssh -o BatchMode=yes "$IMAGE_USER@$IMAGE_HOST" "[ -s '$stage/http.pid' ] && kill \$(cat '$stage/http.pid') 2>/dev/null; sudo firewall-cmd -q --remove-port=8000/tcp; rm -rf '$stage'" || true
fi
rm -f "$A/.stage"
echo "appliance removed"
