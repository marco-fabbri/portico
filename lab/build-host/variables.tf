variable "proxmox_endpoint" { type = string }
variable "proxmox_insecure" {
  type    = bool
  default = true
}
variable "proxmox_api_token" {
  type      = string
  sensitive = true
}
variable "proxmox_node" { type = string }
variable "proxmox_ssh_username" {
  type    = string
  default = "root"
}
variable "datastore_id" { type = string }
variable "image_datastore" { type = string }
variable "rocky9_image_url" {
  type    = string
  default = "https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud-Base.latest.x86_64.qcow2"
}
variable "vm_name" {
  type    = string
  default = "portico-build"
}
variable "vm_id" {
  type    = number
  default = 300
}
variable "cpus" {
  type    = number
  default = 4
}
variable "memory_mib" {
  type    = number
  default = 8192
}
variable "disk_gib" {
  type    = number
  default = 80
}
variable "network_bridge" { type = string }
variable "network_vlan_id" { type = number }
variable "address" { type = string }
variable "network_prefix_length" {
  type    = number
  default = 24
}
variable "network_gateway" { type = string }
variable "dns_servers" { type = list(string) }
variable "ssh_public_key" { type = string }
