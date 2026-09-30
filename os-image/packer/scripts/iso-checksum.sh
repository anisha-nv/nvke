#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Shared helper for resolving an Ubuntu installer ISO's published sha256,
# without downloading the (multi-GiB) ISO itself. Sourced by
# prepare-installer-iso.sh, which downloads and verifies the ISO, and by
# print-installed-disk-cache-key.sh, which only needs the digest to compute a
# cache key.

# nke_resolve_iso_sha256 <iso-url> <iso-checksum> prints the sha256 for
# <iso-url>'s basename found in the SHA256SUMS-style manifest named by
# <iso-checksum> (a "file:<url>" value, matching Packer's iso_checksum
# convention).
nke_resolve_iso_sha256() {
  local iso_url="$1" iso_checksum="$2" iso_name checksum_url sha256
  iso_name="$(basename "${iso_url}")"
  checksum_url="${iso_checksum#file:}"
  sha256="$(curl -fsSL "${checksum_url}" | grep -E " \*?${iso_name}\$" | awk '{print $1}' | head -1)"
  if [[ -z "${sha256}" ]]; then
    echo "iso-checksum: no checksum for ${iso_name} found in ${checksum_url}" >&2
    return 1
  fi
  printf '%s\n' "${sha256}"
}
