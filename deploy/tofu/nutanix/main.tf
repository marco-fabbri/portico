# Portico appliance on Nutanix Prism Central: the image from the GitHub
# release and one VM (design 4.9). Thin on purpose: the console wizard and
# the setup page do the rest. Provider blocks after the Nutanix module of
# marco-fabbri/nkp-preprovisioned-toolkit (tofu/nutanix), validated there
# on pc.7.6 with provider 2.5.
terraform {
  required_version = ">= 1.6.0"
  required_providers {
    nutanix = {
      source  = "nutanix/nutanix"
      version = ">= 2.5.0, < 3.0.0"
    }
  }
}

provider "nutanix" {
  endpoint = var.nutanix_endpoint
  port     = var.nutanix_port
  username = var.nutanix_username
  password = var.nutanix_password
  insecure = var.nutanix_insecure
}

data "nutanix_clusters_v2" "target" {
  filter = "name eq '${var.nutanix_cluster_name}'"
}

data "nutanix_subnets_v2" "target" {
  filter = "name eq '${var.nutanix_subnet_name}'"
}

data "nutanix_storage_containers_v2" "target" {
  count  = var.nutanix_storage_container_name == "" ? 0 : 1
  filter = "name eq '${var.nutanix_storage_container_name}'"
}

locals {
  gib                  = 1024 * 1024 * 1024
  cluster_ext_id       = one(data.nutanix_clusters_v2.target.cluster_entities).ext_id
  subnet_ext_id        = one(data.nutanix_subnets_v2.target.subnets).ext_id
  storage_container_id = var.nutanix_storage_container_name == "" ? null : one(data.nutanix_storage_containers_v2.target[0].storage_containers).container_ext_id
  image_url            = var.portico_image_url != "" ? var.portico_image_url : "https://github.com/marco-fabbri/portico/releases/download/v${var.portico_version}/portico-${var.portico_version}.qcow2"
  # The keyfile the console wizard would write (internal/console), so a
  # later run of the wizard offers these values as defaults.
  keyfile = var.network == null ? "" : join("\n", concat(
    ["[connection]", "id=${var.guest_nic_name}", "type=ethernet", "interface-name=${var.guest_nic_name}", "autoconnect=true", "", "[ipv4]", "method=manual",
    "address1=${var.network.address}/${var.network.prefix}${var.network.gateway != "" ? ",${var.network.gateway}" : ""}"],
    length(var.network.dns) > 0 ? ["dns=${join(";", var.network.dns)};"] : [],
    ["", "[ipv6]", "method=disabled", ""]
  ))
  user_data = templatefile("${path.module}/cloud-init.tftpl", {
    hostname       = var.vm_name
    nic            = var.guest_nic_name
    keyfile        = local.keyfile
    ssh_public_key = var.ssh_public_key
  })
}

resource "nutanix_images_v2" "portico" {
  name                     = "portico-${var.portico_version}"
  description              = "Portico appliance ${var.portico_version}"
  type                     = "DISK_IMAGE"
  cluster_location_ext_ids = [local.cluster_ext_id]
  source {
    url_source {
      url = local.image_url
    }
  }
}
