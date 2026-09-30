#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Prints the installed-disk build cache key: a sha256 over the ISO's own
# digest (resolved from Ubuntu's SHA256SUMS, not a local download -- the
# install stage may not have run yet), UBUNTU_SNAPSHOT, ARCH, UBUNTU_RELEASE,
# VARIANT, and the exact bytes of every file that shapes the install stage
# (autoinstall templates, the install-stage Packer source, shared locals and
# variables, and this Makefile). Over-inclusive is fine here -- a change that
# does not actually affect the installed disk just costs a cache miss. A
# stale hit is not fine, so nothing here is guessed or left out because it
# "shouldn't matter".
set -euo pipefail

ARCH="${ARCH:?ARCH is required}"
UBUNTU_RELEASE="${UBUNTU_RELEASE:?UBUNTU_RELEASE is required}"
UBUNTU_SNAPSHOT="${UBUNTU_SNAPSHOT:?UBUNTU_SNAPSHOT is required}"
VARIANT="${VARIANT:?VARIANT is required}" # fde or non-fde
ISO_URL="${ISO_URL:?ISO_URL is required}"
ISO_CHECKSUM="${ISO_CHECKSUM:?ISO_CHECKSUM is required}"
PACKER_ROOT="${PACKER_ROOT:?PACKER_ROOT is required}"

# shellcheck source=./iso-checksum.sh
. "$(dirname "${BASH_SOURCE[0]}")/iso-checksum.sh"

# ISO_SHA256 lets a caller that already resolved the digest (or a test) skip
# a second network round-trip; otherwise resolve it the same way
# prepare-installer-iso.sh does.
if [[ -n "${ISO_SHA256:-}" ]]; then
  iso_sha256="${ISO_SHA256}"
else
  iso_sha256="$(nke_resolve_iso_sha256 "${ISO_URL}" "${ISO_CHECKSUM}")"
fi

install_stage_inputs=(
  "${PACKER_ROOT}/cidata/autoinstall-lvm.pkrtpl"
  "${PACKER_ROOT}/cidata/user-data.pkrtpl"
  "${PACKER_ROOT}/nke-node-${VARIANT}-install.pkr.hcl"
  "${PACKER_ROOT}/locals.pkr.hcl"
  "${PACKER_ROOT}/variables.pkr.hcl"
  "${PACKER_ROOT}/versions.pkr.hcl"
  "${PACKER_ROOT}/Makefile"
)

for f in "${install_stage_inputs[@]}"; do
  if [[ ! -f "${f}" ]]; then
    echo "print-installed-disk-cache-key: missing install-stage input: ${f}" >&2
    exit 1
  fi
done

{
  printf 'iso-sha256:%s\n' "${iso_sha256}"
  printf 'ubuntu-snapshot:%s\n' "${UBUNTU_SNAPSHOT}"
  printf 'arch:%s\n' "${ARCH}"
  printf 'ubuntu-release:%s\n' "${UBUNTU_RELEASE}"
  printf 'variant:%s\n' "${VARIANT}"
  sha256sum "${install_stage_inputs[@]}"
} | sha256sum | awk '{print $1}'
