# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Provision stage of the FDE variant: boots the disk nke-node-fde-install.pkr.hcl
# produced (fresh, or restored from the installed-disk build cache) as a thin
# qcow2 overlay, then runs the nke provisioners unchanged. The disk already
# auto-unlocks via the build-time `luks.key=` GRUB parameter
# autoinstall-lvm.pkrtpl's late-commands set, and still has the plain
# /home/ubuntu/.ssh/authorized_keys file curtin wrote at install time (until
# provision.sh removes it at the very end), so this SSHes in with the exact
# keypair nke-node-fde-install.pkr.hcl used -- Make passes a matching
# ssh_private_key_file/ssh_public_key_file pair, cached alongside the disk.
# Shared input, architecture, and QEMU configuration is declared once in
# variables.pkr.hcl and locals.pkr.hcl.

source "qemu" "nke-fde-provision" {
  iso_url = local.installed_disk_path
  # Recheck the staged installed disk against the digest in the manifest,
  # the same way the install stage rechecks the installer ISO. This proves
  # only that the bytes are unchanged between staging and boot -- it is not
  # provenance; nothing here verifies the disk actually came from a trusted
  # install stage rather than an arbitrary file placed at this path.
  iso_checksum = local.installed_disk_checksum
  disk_image   = true
  # A thin overlay, not a copy: the cached (or just-built) installed disk is
  # read-only input here. compress-qcow2.sh's `qemu-img convert -O qcow2`
  # flattens this and refuses to publish a result that still carries a
  # backing file (see its header comment), so the overlay never leaks out.
  use_backing_file = true
  # Match the install stage's disk_size. Packer creates the overlay at
  # disk_size, 40 GiB by default, and the image could not deploy to a
  # smaller disk.
  disk_size = var.disk_size

  qemu_binary    = local.qemu_binary
  machine_type   = local.machine_type
  cpu_model      = var.cpu_model
  disk_interface = "virtio"
  format         = "qcow2"
  accelerator    = var.accelerator
  disk_discard   = "unmap"
  disk_cache     = "unsafe"

  # var.efi_firmware_vars here is the install stage's OWN efivars.fd (Make
  # passes a different -var per stage), not the pristine OVMF template: this
  # build must boot with the exact NVRAM state the installed disk's
  # grub-install left behind, not a blank one.
  efi_firmware_code = var.efi_firmware_code
  efi_firmware_vars = var.efi_firmware_vars
  efi_drop_efivars  = true
  efi_boot          = true

  skip_compaction  = true
  disk_compression = false

  qemuargs = concat(local.architecture_qemuargs, [
    ["-serial", "file:${local.build_log_directory}/fde-provision-console.log"],
  ])

  ssh_username           = "ubuntu"
  ssh_private_key_file   = var.ssh_private_key_file
  ssh_timeout            = var.ssh_timeout
  ssh_handshake_attempts = 10000

  shutdown_command = "sudo shutdown -P now"
  vm_name          = "nke-node.qcow2"
  output_directory = "${var.output_directory}/fde"
  headless         = true
  memory           = 8192
  cpus             = var.cpus
  net_device       = "virtio-net"
}

build {
  name    = "nke-node-fde-provision"
  sources = ["source.qemu.nke-fde-provision"]

  provisioner "shell-local" {
    environment_vars = [
      "MODE=stage",
      "MATERIAL_PATH=${local.image_inputs}",
      "EXPECTED_DIGEST=${local.image_inputs_digest}",
      "STAGING_DIRECTORY=${local.staged_image_inputs.fde}",
    ]
    script = "${local.project_root}/scripts/prepare-image-inputs.sh"
  }

  error-cleanup-provisioner "shell-local" {
    environment_vars = [
      "MODE=clean",
      "STAGING_DIRECTORY=${local.staged_image_inputs.fde}",
    ]
    script = "${local.project_root}/scripts/prepare-image-inputs.sh"
  }

  provisioner "shell" {
    inline = ["mkdir -p /var/tmp/nke-image/luks"]
  }

  provisioner "file" {
    source      = "${local.staged_image_inputs.fde}/"
    destination = "/var/tmp/nke-image/"
    generated   = true
  }

  provisioner "shell-local" {
    environment_vars = [
      "MODE=clean",
      "STAGING_DIRECTORY=${local.staged_image_inputs.fde}",
    ]
    script = "${local.project_root}/scripts/prepare-image-inputs.sh"
  }

  provisioner "file" {
    source      = "${local.project_root}/../files/luks/"
    destination = "/var/tmp/nke-image/luks/"
  }

  provisioner "shell" {
    # Run before provision.sh removes Packer's authorized SSH key.
    environment_vars = ["LUKS_PASSPHRASE=${var.luks_passphrase}"]
    script           = "${local.project_root}/scripts/configure-fde.sh"
  }

  provisioner "shell" {
    environment_vars = ["ROOT_FS_LABEL=${var.root_fs_label}"]
    script           = "${local.project_root}/scripts/provision.sh"
  }

  post-processor "shell-local" {
    # Keep one source-owned final conversion for Make and direct callers.
    inline = ["${local.project_root}/scripts/compress-qcow2.sh ${var.output_directory}/fde/nke-node.qcow2"]
  }
}
