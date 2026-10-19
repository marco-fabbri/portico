# Portico appliance image: Rocky Linux 9 from the boot ISO with
# image/portico.ks, deploy/install.sh, OpenSCAP hardening and a cleanup,
# built by .github/workflows/image.yml (or image/build.sh on an x86_64
# host with KVM). Output: output/portico-<version>.qcow2 and its sha256,
# output/portico-<version>-oscap.html.
packer {
  required_version = ">= 1.11.0"
  required_plugins {
    qemu = {
      source  = "github.com/hashicorp/qemu"
      version = ">= 1.1.0, < 2.0.0"
    }
  }
}

variable "version" {
  type        = string
  description = "Portico version, part of the image name"
}

variable "binary" {
  type        = string
  description = "Path of the portico binary built for linux/amd64"
}

variable "iso_url" {
  type    = string
  default = "https://dl.rockylinux.org/pub/rocky/9/isos/x86_64/Rocky-9.8-x86_64-boot.iso"
}

variable "iso_checksum" {
  type    = string
  default = "sha256:d6eeefdc8437c593d41a3150fcca4a734c55642ed472eecdda99720bb1370881"
}

variable "accelerator" {
  type    = string
  default = "kvm"
}

variable "headless" {
  type    = bool
  default = true
}

source "qemu" "portico" {
  iso_url          = var.iso_url
  iso_checksum     = var.iso_checksum
  output_directory = "output"
  vm_name          = "portico-${var.version}.qcow2"
  format           = "qcow2"
  disk_size        = "40G"
  disk_interface   = "virtio"
  disk_compression = true
  net_device       = "virtio-net"
  accelerator      = var.accelerator
  cpus             = 2
  memory           = 4096
  headless         = var.headless
  http_directory   = "."
  boot_wait        = "10s"
  # The boot ISO's isolinux menu: Tab opens the kernel line.
  boot_command = [
    "<tab><wait>",
    " inst.text inst.ks=http://{{ .HTTPIP }}:{{ .HTTPPort }}/portico.ks",
    "<enter>"
  ]
  ssh_username     = "build"
  ssh_password     = "portico-build"
  ssh_timeout      = "45m"
  shutdown_command = "sudo bash /tmp/portico-build/scripts/poweroff.sh"
  shutdown_timeout = "5m"
}

build {
  sources = ["source.qemu.portico"]

  provisioner "shell" {
    inline = ["mkdir -p /tmp/portico-build"]
  }
  provisioner "file" {
    source      = var.binary
    destination = "/tmp/portico-build/portico"
  }
  provisioner "file" {
    source      = "../deploy"
    destination = "/tmp/portico-build/deploy"
  }
  provisioner "file" {
    source      = "oscap"
    destination = "/tmp/portico-build/oscap"
  }
  provisioner "file" {
    source      = "scripts"
    destination = "/tmp/portico-build/scripts"
  }
  provisioner "shell" {
    execute_command = "sudo -E bash '{{ .Path }}'"
    inline = [
      "bash /tmp/portico-build/deploy/install.sh --binary /tmp/portico-build/portico --no-start",
      "bash /tmp/portico-build/scripts/harden.sh",
    ]
  }
  provisioner "file" {
    direction   = "download"
    source      = "/tmp/portico-build/oscap-report.html"
    destination = "output/portico-${var.version}-oscap.html"
  }
  provisioner "shell" {
    execute_command = "sudo -E bash '{{ .Path }}'"
    script          = "scripts/cleanup.sh"
  }

  post-processor "checksum" {
    checksum_types = ["sha256"]
    output         = "output/portico-${var.version}.qcow2.sha256"
  }
}
