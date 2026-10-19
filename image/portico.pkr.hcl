# Portico appliance image, in two stages (design 4.9):
#
#   packer build -only=qemu.install   portico.pkr.hcl   # anaconda + kickstart -> output-install/portico-base.qcow2
#   packer build -only=qemu.provision portico.pkr.hcl   # boot that disk, install.sh, OpenSCAP, cleanup -> output/portico-<version>.qcow2
#
# The install stage boots anaconda's kernel and initrd directly (no ISO
# menu to drive over VNC) with the kickstart served by Packer's HTTP
# server on a fixed port, and writes the serial console to a file so the
# CI log shows what anaconda did. The provision stage boots the installed
# disk normally and talks SSH to the build user the kickstart created.
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
  description = "Path of the portico binary built for linux/amd64 (provision stage)"
  default     = ""
}

variable "mirror" {
  type        = string
  description = "Rocky Linux 9 BaseOS repository: stage2, packages, and images/pxeboot for the kernel"
  default     = "https://dl.rockylinux.org/pub/rocky/9/BaseOS/x86_64/os"
}

variable "accelerator" {
  type    = string
  default = "kvm"
}

variable "headless" {
  type    = bool
  default = true
}

# Stage 1: anaconda installs Rocky 9 with image/portico.ks and powers off.
source "qemu" "install" {
  # Packer needs a medium; the kernel and initrd below do the booting and
  # anaconda fetches stage2 and the packages from the mirror. The initrd
  # itself is that medium: small, and already downloaded by build.sh.
  iso_url          = "boot/initrd.img"
  iso_checksum     = "none"
  output_directory = "output-install"
  vm_name          = "portico-base.qcow2"
  format           = "qcow2"
  disk_size        = "40G"
  disk_interface   = "virtio"
  # fstrim in the guest reaches the image: blocks freed by the cleanup
  # are not stored (without this the qcow2 kept every deleted cache).
  disk_discard     = "unmap"
  disk_detect_zeroes = "unmap"
  net_device       = "virtio-net"
  accelerator      = var.accelerator
  cpus             = 2
  memory           = 4096
  headless         = var.headless
  http_directory   = "."
  http_port_min    = 8181
  http_port_max    = 8181
  communicator     = "none"
  # With no communicator Packer waits for the VM to power itself off, up
  # to this long; anaconda needs more than the 5-minute default.
  shutdown_timeout = "60m"
  boot_wait        = "2s"
  # Rocky 9 needs x86-64-v2; QEMU's default qemu64 model lacks it (glibc
  # refuses to start and the kernel panics, measured on the runner).
  qemuargs = [
    ["-cpu", "host"],
    ["-kernel", "boot/vmlinuz"],
    ["-initrd", "boot/initrd.img"],
    ["-append", "inst.ks=http://10.0.2.2:8181/portico.ks inst.stage2=${var.mirror} inst.repo=${var.mirror} inst.text ip=dhcp console=ttyS0,115200n8"],
    ["-serial", "file:serial-install.log"],
  ]
}

# Stage 2: boot the installed disk, provision over SSH, close the image.
source "qemu" "provision" {
  iso_url          = "output-install/portico-base.qcow2"
  iso_checksum     = "none"
  disk_image       = true
  output_directory = "output"
  vm_name          = "portico-${var.version}.qcow2"
  format           = "qcow2"
  disk_size        = "40G"
  disk_interface   = "virtio"
  disk_discard     = "unmap"
  disk_detect_zeroes = "unmap"
  # Prism Central's image service refuses a qcow2 with compressed clusters
  # (measured on pc.7.6: "dependent backend service has encountered an
  # error"); the same image uncompressed imports. Discard keeps it small.
  disk_compression = false
  net_device       = "virtio-net"
  accelerator      = var.accelerator
  cpus             = 2
  memory           = 4096
  headless         = var.headless
  boot_wait        = "20s"
  ssh_username     = "build"
  ssh_password     = "portico-build"
  ssh_timeout      = "15m"
  shutdown_command = "sudo bash /tmp/portico-build/scripts/poweroff.sh"
  shutdown_timeout = "5m"
  qemuargs = [
    ["-cpu", "host"],
    ["-serial", "file:serial-provision.log"],
  ]
}

build {
  sources = ["source.qemu.install"]
}

build {
  sources = ["source.qemu.provision"]

  # /tmp is noexec on the appliance (CIS): every script runs through bash.
  provisioner "shell" {
    execute_command = "bash '{{ .Path }}'"
    inline          = ["mkdir -p /tmp/portico-build"]
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
