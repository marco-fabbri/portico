output "vm_ext_id" {
  description = "extId of the appliance VM"
  value       = nutanix_virtual_machine_v2.portico.id
}

output "image_ext_id" {
  description = "extId of the uploaded image"
  value       = nutanix_images_v2.portico.id
}

output "admin_url" {
  description = "The admin UI, once the network is up"
  value       = var.network == null ? "set the network on the VM console (Prism Central > VM > Launch console), then https://<address>:8443/" : "https://${var.network.address}:8443/"
}
