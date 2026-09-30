# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Install stage of the FDE variant: boots the Ubuntu Server installer, runs
# autoinstall to lay down the LVM-on-LUKS layout, and shuts down as soon as
# SSH into the installed system succeeds -- no provisioners here. Its output
# is the reusable "installed disk" the Makefile's installed-disk build cache
# keys on (see INSTALLED_DIR / print-INSTALLED_DISK_CACHE_KEY_FDE in
# Makefile) and nke-node-fde-provision.pkr.hcl boots as a thin overlay to run
# the actual nke provisioners. Shared input, architecture, and QEMU
# configuration is declared once in variables.pkr.hcl and locals.pkr.hcl.

source "qemu" "nke-fde-install" {
  iso_url = local.installer_iso
  # Recheck the staged ISO against the digest in the authenticated manifest.
  iso_checksum = local.installer_checksum

  qemu_binary    = local.qemu_binary
  machine_type   = local.machine_type
  cpu_model      = var.cpu_model
  disk_size      = var.disk_size
  disk_interface = "virtio"
  format         = "qcow2"
  accelerator    = var.accelerator
  # Propagate guest TRIM so provision.sh's fstrim can release unused QCOW2
  # clusters; without unmap the guest reports success but the image stays large.
  disk_discard = "unmap"
  # This is a throwaway build VM that Packer shuts down cleanly. Avoid paying
  # the runner-storage cost of every package-install fsync.
  disk_cache = "unsafe"

  efi_firmware_code = var.efi_firmware_code
  efi_firmware_vars = var.efi_firmware_vars
  # Keep this build's own NVRAM state (unlike the provision stage below):
  # the provision build boots this same disk as its own QEMU guest and needs
  # whatever boot entry the installer's grub-install actually registered.
  # Dropped efivars is execution state Packer discards; keeping it here is
  # what makes it a published, cacheable half of the installed-disk pair (see
  # the Makefile's INSTALLED_DIR layout and prepare-installer-disk).
  efi_drop_efivars = false
  efi_boot         = true

  # No final compression here -- the install stage's qcow2 is an internal,
  # cached intermediate, never a published artifact. compress-qcow2.sh runs
  # once, on the provision stage's flattened result.
  skip_compaction  = true
  disk_compression = false

  http_content = {
    "/user-data" = templatefile("${local.project_root}/cidata/autoinstall-lvm.pkrtpl", {
      ssh_public_key        = local.ssh_public_key
      luks_enabled          = true
      luks_passphrase       = var.luks_passphrase
      nvidia_kernel_package = local.nvidia_kernel_package
      serial_console        = local.serial_console
      ubuntu_snapshot       = var.ubuntu_snapshot
      ubuntu_archive_path   = local.ubuntu_archive_path
    })
    "/meta-data" = "instance-id: nke-packer-fde\nlocal-hostname: nke-packer-fde\n"
  }

  boot_wait    = var.boot_wait
  boot_command = local.boot_command
  qemuargs = concat(local.architecture_qemuargs, [
    ["-serial", "file:${local.build_log_directory}/fde-install-console.log"],
  ])

  ssh_username         = "ubuntu"
  ssh_private_key_file = var.ssh_private_key_file
  ssh_timeout          = var.ssh_timeout
  # The live installer may accept and drop many probes before its final reboot;
  # do not let those expected attempts exhaust Packer's handshake counter.
  ssh_handshake_attempts = 10000

  shutdown_command = "sudo shutdown -P now"
  vm_name          = "nke-node-installed.qcow2"
  output_directory = "${var.output_directory}/fde"
  headless         = true
  memory           = 8192
  cpus             = var.cpus
  net_device       = "virtio-net"
}

build {
  name    = "nke-node-fde-install"
  sources = ["source.qemu.nke-fde-install"]
  # No provisioners: this stage's only job is autoinstall. shutdown_command
  # above fires as soon as SSH connects, since there is nothing else to run.
}
