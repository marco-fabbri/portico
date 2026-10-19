# Build host for the appliance image: an x86_64 VM on Proxmox with KVM
# inside (CPU type host), where image/build.sh and the boot test run in
# the same time as on the GitHub runner, with the logs at hand. Provider
# and shape after the Proxmox module of marco-fabbri/nkp-preprovisioned-toolkit.
terraform {
  required_version = ">= 1.6.0"
  required_providers {
    proxmox = {
      source  = "bpg/proxmox"
      version = ">= 0.100.0, < 1.0.0"
    }
  }
}

provider "proxmox" {
  endpoint  = var.proxmox_endpoint
  insecure  = var.proxmox_insecure
  api_token = var.proxmox_api_token
}

resource "proxmox_download_file" "rocky9" {
  content_type = "iso"
  datastore_id = var.image_datastore
  node_name    = var.proxmox_node
  file_name    = "portico-build-rocky9.img"
  url          = var.rocky9_image_url
  overwrite    = false
  # Proxmox downloads the 600 MB image itself; the provider's default wait
  # of ten minutes was too short on the lab's link.
  upload_timeout = 3600
}

resource "proxmox_virtual_environment_vm" "build" {
  name        = var.vm_name
  description = "Portico image build host (KVM inside)"
  node_name   = var.proxmox_node
  vm_id       = var.vm_id
  on_boot     = true

  agent {
    enabled = false
  }
  cpu {
    cores = var.cpus
    type  = "host"
  }
  memory {
    dedicated = var.memory_mib
  }
  disk {
    datastore_id = var.datastore_id
    file_id      = proxmox_download_file.rocky9.id
    interface    = "scsi0"
    size         = var.disk_gib
    discard      = "on"
  }
  network_device {
    bridge  = var.network_bridge
    vlan_id = var.network_vlan_id
  }
  initialization {
    datastore_id = var.datastore_id
    ip_config {
      ipv4 {
        address = "${var.address}/${var.network_prefix_length}"
        gateway = var.network_gateway
      }
    }
    dns {
      servers = var.dns_servers
    }
    user_account {
      username = "build"
      keys     = [var.ssh_public_key]
    }
  }
  operating_system {
    type = "l26"
  }
  serial_device {}
  lifecycle {
    ignore_changes = [initialization]
  }
}

output "address" {
  value = var.address
}
