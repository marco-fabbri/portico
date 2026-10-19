# Build host

An x86_64 VM on the lab's Proxmox with KVM inside, for `image/build.sh`
and `image/scripts/boottest.sh`: the same build as the GitHub runner,
with the logs at hand and no cloud minutes. `deploy.sh` creates it from
the Rocky 9 GenericCloud image (CPU type host, 4 vCPU, 8 GiB, 80 GiB,
VLAN 220, `10.23.220.90`), taking the Proxmox endpoint, node, datastores
and network from the NKP toolkit's `tofu/proxmox/terraform.tfvars` and
the API token from there into the environment only; then `provision.sh`
installs QEMU/KVM, Packer, OpenTofu, Go, gh and clones the repository
under `~build/portico`.

```
lab/build-host/deploy.sh
ssh build@10.23.220.90
cd portico && git pull && CGO_ENABLED=0 go build -o /tmp/portico ./cmd/portico && image/build.sh 0.0.0-dev /tmp/portico
SEED_FORMAT=configdrive image/scripts/boottest.sh image/output/portico-0.0.0-dev.qcow2
```

`deploy.sh --destroy` removes the VM. Lab only; nothing here is part of
the product.
