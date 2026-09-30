#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Prints the installed-disk build cache key: a sha256 over everything that
# shapes the install stage's output -- the ISO's basename
# (prepare-installer-iso.sh verifies its bytes, so this does not need to),
# UBUNTU_SNAPSHOT, ARCH, UBUNTU_RELEASE, VARIANT, a hash of LUKS_PASSPHRASE
# (never the passphrase itself), SERIAL_CONSOLE, DISK_SIZE, and the exact
# bytes of the files that render or select autoinstall config. cd into the
# packer root first, so the key does not depend on where this checkout lives.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

: "${ARCH:?}" "${UBUNTU_RELEASE:?}" "${UBUNTU_SNAPSHOT:?}" "${VARIANT:?}" "${ISO_URL:?}" "${LUKS_PASSPHRASE:?}" "${SERIAL_CONSOLE?}" "${DISK_SIZE:?}"

passphrase_sha="$(printf '%s' "${LUKS_PASSPHRASE}" | sha256sum | cut -d' ' -f1)"
{
  printf 'iso:%s\nsnapshot:%s\narch:%s\nrelease:%s\nvariant:%s\nluks:%s\nserial:%s\ndisk_size:%s\n' \
    "${ISO_URL##*/}" "${UBUNTU_SNAPSHOT}" "${ARCH}" "${UBUNTU_RELEASE}" "${VARIANT}" \
    "${passphrase_sha}" "${SERIAL_CONSOLE}" "${DISK_SIZE}"
  sha256sum cidata/autoinstall-lvm.pkrtpl "nke-node-${VARIANT}-install.pkr.hcl" \
    locals.pkr.hcl variables.pkr.hcl versions.pkr.hcl Makefile \
    scripts/print-installed-disk-cache-key.sh scripts/prepare-installer-iso.sh
} | sha256sum | cut -d' ' -f1
