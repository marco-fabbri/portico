# Renders deploy/tofu/nutanix/cloud-init.tftpl for the boot test without
# the Nutanix provider (tofu console in the module itself waited on the
# provider's configuration on the runner). Same template, same inputs.
variable "hostname" { type = string }
variable "nic" { type = string }
variable "ssh_public_key" { type = string }
variable "keyfile" { type = string }

locals {
  user_data = templatefile("${path.module}/../../../deploy/tofu/nutanix/cloud-init.tftpl", {
    hostname       = var.hostname
    nic            = var.nic
    ssh_public_key = var.ssh_public_key
    keyfile        = var.keyfile
  })
}

output "user_data" {
  value = local.user_data
}
