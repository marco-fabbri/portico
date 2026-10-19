#!/usr/bin/env bash
# Lab: the image build host on Proxmox. Reads the Proxmox endpoint, node,
# datastores and network from the NKP toolkit's tofu/proxmox/terraform.tfvars
# (Marco's lab, same Proxmox) and the API token from there into the
# environment only; writes lab/build-host/terraform.tfvars without it.
#   lab/build-host/deploy.sh            create or update the VM, then provision it
#   lab/build-host/deploy.sh --destroy
set -euo pipefail
cd "$(dirname "$0")/../.." || exit 1
NKP="${NKP_TOOLKIT:-$HOME/nkp-preprovisioned-toolkit}/tofu/proxmox/terraform.tfvars"
[ -r "$NKP" ] || { echo "no $NKP: give NKP_TOOLKIT or write lab/build-host/terraform.tfvars by hand" >&2; exit 1; }
B=lab/build-host
ADDRESS="${BUILD_HOST_IP:-10.23.220.90}"
val() { sed -n "s/^$1 *= *\"\\{0,1\\}\\([^\"]*\\)\"\\{0,1\\}.*/\\1/p" "$NKP" | head -1; }
export TF_VAR_proxmox_api_token
TF_VAR_proxmox_api_token=$(val proxmox_api_token)
[ -n "$TF_VAR_proxmox_api_token" ] || { echo "no proxmox_api_token in $NKP" >&2; exit 1; }
if [ "${1:-}" = "--destroy" ]; then
  tofu -chdir="$B" destroy -input=false -auto-approve
  exit
fi
umask 077
sshkey=""
for k in "$HOME/.ssh/id_ed25519.pub" "$HOME/.ssh/id_rsa.pub"; do [ -r "$k" ] && { sshkey=$(cat "$k"); break; }; done
[ -n "$sshkey" ] || { echo "no SSH public key in ~/.ssh" >&2; exit 1; }
cat > "$B/terraform.tfvars" <<VARS
proxmox_endpoint = "$(val proxmox_endpoint)"
proxmox_node     = "$(val proxmox_node)"
datastore_id     = "$(val datastore_id)"
image_datastore  = "$(val image_datastore)"
network_bridge   = "$(val network_bridge)"
network_vlan_id  = $(val network_vlan_id)
network_gateway  = "$(val network_gateway)"
dns_servers      = $(sed -n 's/^dns_servers *= *//p' "$NKP" | head -1)
address          = "$ADDRESS"
ssh_public_key   = "$sshkey"
VARS
if ping -c 2 -W 1 "$ADDRESS" >/dev/null 2>&1 && [ ! -s "$B/terraform.tfstate" ]; then echo "refusing: $ADDRESS answers ping and no state exists" >&2; exit 2; fi
tofu -chdir="$B" init -input=false >/dev/null
tofu -chdir="$B" apply -input=false -auto-approve
echo "waiting for SSH on $ADDRESS"
for _ in $(seq 1 60); do ssh -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "build@$ADDRESS" true 2>/dev/null && break; sleep 5; done
scp -q -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "$B/provision.sh" "build@$ADDRESS:provision.sh"
ssh -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "build@$ADDRESS" 'sudo bash ~/provision.sh && rm -f ~/provision.sh'
echo "build host ready: ssh build@$ADDRESS; the repository is in ~/portico"
