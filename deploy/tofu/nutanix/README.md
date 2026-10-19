# Portico appliance on Prism Central with OpenTofu

Uploads the appliance image from the GitHub release and creates the VM.
Thin on purpose: everything after the first boot is the console wizard
(password, exposure) and the setup page.

Prerequisites: OpenTofu 1.6 or later, a Prism Central user that can
create images and VMs, a subnet the appliance will live in, and the
firewall rule of the design: port 9440 of Prism Central reachable from
the appliance's address, never from the Internet.

```
cp terraform.tfvars.example terraform.tfvars   # fill in
export TF_VAR_nutanix_password='...'           # or put it in the file
tofu init
tofu apply
```

`tofu output admin_url` prints where to go next. With `network` set,
cloud-init writes the same keyfile the console wizard would and the
appliance answers on `https://<address>:8443/` within a couple of
minutes; without it, open the VM console in Prism Central and the wizard
asks for the network. `ssh_public_key` adds a user `admin` with sudo;
without it nobody can SSH in and the console is the only way in.

`portico_image_url` points at a mirror instead of the release (a lab web
server, an internal artifact store). Changing `portico_version` uploads
the new image and rebuilds the VM: back up `/var/lib/portico` first, the
store and the master key live there (docs/operating.md).
