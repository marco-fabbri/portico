# Portico edge router image (prefix-routing design 4.4), one stage:
#
#   packer build -var version=<version> edge.pkr.hcl   # -> output-edge/portico-edge-<version>.qcow2
#
# The Rocky Linux 9 GenericCloud image is booted as it is with a NoCloud
# seed that gives Packer a build user; scripts/edge-provision.sh installs
# FRR and nftables and turns the default user, the password login and the
# SSH server off; scripts/harden.sh applies the CIS Level 1 Server
# profile with oscap/edge-tailoring.xml; the cleanup and the power-off
# leave an image with no user, no password, no SSH host key, no SSH
# server, cloud-init enabled. The edge gets everything else from the
# cloud-init Portico sends (internal/peering/edge_userdata.tmpl).
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

# The base image is pinned by name and checksum: a build is repeatable,
# a new base is a change to this file. Rocky keeps the current images at
# this path and the earlier ones under pub/rocky-vault.
variable "base_url" {
  type    = string
  default = "https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud-Base-9.8-20260525.0.x86_64.qcow2"
}

variable "base_checksum" {
  type    = string
  default = "sha256:92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8"
}

variable "accelerator" {
  type    = string
  default = "kvm"
}

variable "headless" {
  type    = bool
  default = true
}

source "qemu" "edge" {
  iso_url          = var.base_url
  iso_checksum     = var.base_checksum
  disk_image       = true
  output_directory = "output-edge"
  vm_name          = "portico-edge-${var.version}.qcow2"
  format           = "qcow2"
  # The GenericCloud disk is 10 GiB: the VM Portico creates asks the same.
  disk_size        = "10G"
  disk_interface   = "virtio"
  disk_discard     = "unmap"
  disk_detect_zeroes = "unmap"
  # Prism Central's image service refuses compressed clusters (portico.pkr.hcl).
  disk_compression = false
  net_device       = "virtio-net"
  accelerator      = var.accelerator
  cpus             = 2
  memory           = 2048
  headless         = var.headless
  # The build's seed: a user for Packer, gone with the power-off.
  cd_label = "cidata"
  cd_content = {
    "meta-data" = "instance-id: portico-edge-build\nlocal-hostname: portico-edge\n"
    "user-data" = <<-UD
      #cloud-config
      users:
        - name: build
          groups: [wheel]
          sudo: ["ALL=(ALL) NOPASSWD:ALL"]
          shell: /bin/bash
          lock_passwd: false
          plain_text_passwd: portico-build
      ssh_pwauth: true
      UD
  }
  boot_wait        = "10s"
  ssh_username     = "build"
  ssh_password     = "portico-build"
  ssh_timeout      = "15m"
  shutdown_command = "sudo bash /tmp/portico-build/scripts/edge-provision.sh poweroff"
  shutdown_timeout = "5m"
  qemuargs = [
    ["-cpu", "host"],
    ["-serial", "file:serial-edge.log"],
  ]
}

build {
  sources = ["source.qemu.edge"]

  # /tmp is noexec once hardened: every script runs through bash.
  provisioner "shell" {
    execute_command = "bash '{{ .Path }}'"
    inline          = ["mkdir -p /tmp/portico-build"]
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
      "bash /tmp/portico-build/scripts/edge-provision.sh install",
      "TAILORING=/tmp/portico-build/oscap/edge-tailoring.xml EXPECTED=/tmp/portico-build/oscap/edge-expected-failures.txt bash /tmp/portico-build/scripts/harden.sh",
    ]
  }
  provisioner "file" {
    direction   = "download"
    source      = "/tmp/portico-build/oscap-report.html"
    destination = "output-edge/portico-edge-${var.version}-oscap.html"
  }
  provisioner "shell" {
    execute_command = "sudo -E bash '{{ .Path }}'"
    inline          = ["bash /tmp/portico-build/scripts/edge-provision.sh cleanup"]
  }

  post-processor "checksum" {
    checksum_types = ["sha256"]
    output         = "output-edge/portico-edge-${var.version}.qcow2.sha256"
  }
}
