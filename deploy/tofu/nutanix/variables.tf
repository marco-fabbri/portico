variable "nutanix_endpoint" {
  type        = string
  description = "Prism Central address (name or IP)"
}

variable "nutanix_port" {
  type    = number
  default = 9440
}

variable "nutanix_username" {
  type        = string
  description = "Prism Central user that can create images and VMs"
}

variable "nutanix_password" {
  type      = string
  sensitive = true
}

variable "nutanix_insecure" {
  type        = bool
  default     = false
  description = "true to skip the Prism Central certificate check (self-signed certificate)"
}

variable "nutanix_cluster_name" {
  type        = string
  description = "AHV cluster that hosts the appliance"
}

variable "nutanix_subnet_name" {
  type        = string
  description = "Subnet of the appliance's interface"
}

variable "nutanix_subnet_ipam" {
  type        = bool
  default     = false
  description = "true when the subnet has AHV IPAM: the static address is then also reserved on AHV"
}

variable "nutanix_storage_container_name" {
  type        = string
  default     = ""
  description = "Storage container of the disk; empty = the cluster default"
}

variable "portico_version" {
  type        = string
  description = "Portico release whose image is uploaded (for example 0.1.0)"
}

variable "portico_image_url" {
  type        = string
  default     = ""
  description = "Overrides the release URL of the qcow2 (a local mirror, a lab web server)"
}

variable "vm_name" {
  type    = string
  default = "portico"
}

variable "vm_cpus" {
  type    = number
  default = 2
}

variable "vm_memory_mib" {
  type    = number
  default = 6144
}

variable "vm_disk_gib" {
  type        = number
  default     = 40
  description = "Disk size; the image's layout needs 40 GiB (the kickstart refuses less)"
  validation {
    condition     = var.vm_disk_gib >= 40
    error_message = "vm_disk_gib must be at least 40: the appliance layout needs it."
  }
}

variable "guest_nic_name" {
  type        = string
  default     = "eth0"
  description = "Interface name inside the guest (Rocky Linux 9 on AHV: eth0)"
}

variable "network" {
  type = object({
    address = string
    prefix  = number
    gateway = string
    dns     = list(string)
  })
  default     = null
  description = "Static network written by cloud-init at the first boot; null = set it on the console wizard"
  validation {
    condition     = var.network == null || can(regex("^((25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])\\.){3}(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])$", var.network.address))
    error_message = "network.address must be an IPv4 address."
  }
  validation {
    condition     = var.network == null || (var.network.prefix >= 1 && var.network.prefix <= 32)
    error_message = "network.prefix must be 1 to 32."
  }
}

variable "ssh_public_key" {
  type        = string
  default     = ""
  description = "When set, cloud-init creates the user admin with sudo and this key; without it the console is the only way in"
}
