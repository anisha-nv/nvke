#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Validate the booted kernel before removing any packages; record the image profile.
set -euo pipefail

IMAGE_PROFILE="${IMAGE_PROFILE:?IMAGE_PROFILE is required}"
running_kernel="$(uname -r)"
case "${IMAGE_PROFILE}:${running_kernel}" in
  development:*-generic|production:*-nvidia*) ;;
  *)
    echo "kernel ${running_kernel} does not match IMAGE_PROFILE=${IMAGE_PROFILE}" >&2
    exit 1
    ;;
esac
sudo install -d -m 0755 /etc/nke
printf '%s\n' "$IMAGE_PROFILE" | sudo tee /etc/nke/image-profile >/dev/null

# The generic kernel is intentional in development images. Never purge it.
if [[ "$IMAGE_PROFILE" == development ]]; then
  exit 0
fi

for pkg in linux-generic linux-headers-generic linux-image-generic; do
  if dpkg-query -W -f='${Status}' "${pkg}" 2>/dev/null | grep -q "^install ok installed$"; then
    sudo apt-get -y purge "${pkg}"
  fi
done

# Purging the meta-packages above does not cascade to their versioned
# dependents: `apt-get autoremove` (in provision.sh) deliberately never fully removes
# a kernel family -- /etc/apt/apt.conf.d/01autoremove's
# VersionedKernelPackages groups every "linux-.*" package and keeps at least
# one full kernel per flavor installed as a safety net against an
# unbootable system, regardless of its auto-install mark. Confirmed on a
# real build: autoremove left linux-image-*-generic, linux-modules-*-generic,
# linux-headers-*-generic/*-generic's arch-independent linux-headers-<ver>,
# linux-tools-*-generic and its zfs modules all installed. Purge that whole
# family by exact name instead, the same way an explicit `apt-get purge
# <version>` from the command line bypasses the same protection.
# dpkg-query -W lists a package regardless of its current state, so an
# already-purged package still known to dpkg (e.g. the old
# linux-image-unsigned-7.0.0-14-generic rc-state leftover the "Cleaning up"
# autoremove below sweeps up) can match this glob and win `head -1` over the
# actually-installed kernel -- filter to installed packages explicitly.
# Querying with no pattern (filtering in awk instead) also avoids dpkg-query
# exiting 1 -- which set -euo pipefail would treat as fatal here -- on a
# system where dpkg has no record at all matching the glob.
# Consume all input: exiting awk early can give dpkg-query SIGPIPE under pipefail.
generic_image_pkg="$(dpkg-query -W -f='${Package}\t${Status}\n' 2>/dev/null |
  awk -F'\t' '!found && $1 ~ /^linux-image-.*-generic$/ && $2 == "install ok installed" { print $1; found=1 }')"
if [[ -n "${generic_image_pkg}" ]]; then
  ver="${generic_image_pkg#linux-image-}"      # e.g. 7.0.0-31-generic
  ver_noflavor="${ver%-generic}"                # e.g. 7.0.0-31
  generic_kernel_candidates=(
    "linux-image-${ver}"
    "linux-modules-${ver}"
    "linux-modules-extra-${ver}"
    "linux-headers-${ver}"
    "linux-headers-${ver_noflavor}"
    "linux-tools-${ver}"
    "linux-tools-${ver_noflavor}"
    "linux-main-modules-zfs-${ver}"
  )
  generic_kernel_pkgs=()
  for pkg in "${generic_kernel_candidates[@]}"; do
    if dpkg-query -W -f='${Status}' "${pkg}" 2>/dev/null | grep -q "^install ok installed$"; then
      generic_kernel_pkgs+=("${pkg}")
    fi
  done
  if [[ "${#generic_kernel_pkgs[@]}" -gt 0 ]]; then
    sudo apt-get -y purge "${generic_kernel_pkgs[@]}"
  fi
fi
