# A change to the cloud-init payload rebuilds the VM: Prism Central does
# not return it after creation (ignore_changes below).
resource "terraform_data" "cloud_init" {
  input = sha256(local.user_data)
}

resource "nutanix_virtual_machine_v2" "portico" {
  name                 = var.vm_name
  description          = "Portico appliance ${var.portico_version}"
  num_sockets          = var.vm_cpus
  num_cores_per_socket = 1
  memory_size_bytes    = var.vm_memory_mib * 1024 * 1024
  power_state          = "ON"

  cluster {
    ext_id = local.cluster_ext_id
  }

  disks {
    disk_address {
      bus_type = "SCSI"
      index    = 0
    }
    backing_info {
      vm_disk {
        disk_size_bytes = var.vm_disk_gib * local.gib
        data_source {
          reference {
            image_reference {
              image_ext_id = nutanix_images_v2.portico.id
            }
          }
        }
        dynamic "storage_container" {
          for_each = local.storage_container_id == null ? [] : [local.storage_container_id]
          content {
            ext_id = storage_container.value
          }
        }
      }
    }
  }

  nics {
    nic_network_info {
      virtual_ethernet_nic_network_info {
        nic_type  = "NORMAL_NIC"
        vlan_mode = "ACCESS"
        subnet {
          ext_id = local.subnet_ext_id
        }
        dynamic "ipv4_config" {
          for_each = var.nutanix_subnet_ipam && var.network != null ? [var.network.address] : []
          content {
            should_assign_ip = true
            ip_address {
              value = ipv4_config.value
            }
          }
        }
      }
    }
  }

  boot_config {
    legacy_boot {
      boot_order = ["DISK", "CDROM", "NETWORK"]
    }
  }

  guest_customization {
    config {
      cloud_init {
        datasource_type = "CONFIG_DRIVE_V2"
        cloud_init_script {
          user_data {
            value = base64encode(local.user_data)
          }
        }
      }
    }
  }

  lifecycle {
    replace_triggered_by = [terraform_data.cloud_init]
    ignore_changes = [
      guest_customization,
      cd_roms,
      nics[0].nic_network_info[0].virtual_ethernet_nic_network_info[0].ipv4_config[0].should_assign_ip,
    ]
  }
}
