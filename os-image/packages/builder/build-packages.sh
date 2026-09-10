#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Build NKE package debs from a writable source tree. Both the native Docker
# builder and the Packer guest fallback use this entry point.
set -euo pipefail
umask 022

source_root="${1:?SOURCE_ROOT is required}"
cache_dir="${2:?CACHE_DIR is required}"
out_dir="${3:?OUT_DIR is required}"
bin_dir="${4:?BIN_DIR is required}"
operation="${5:?BUILD_OPERATION is required}"

case "${operation}" in debs | lock) ;; *) echo 'build-packages: expected debs or lock' >&2; exit 1 ;; esac
case "${ARCH:?ARCH is required}" in amd64 | arm64) ;; *) echo 'build-packages: ARCH must be amd64 or arm64' >&2; exit 1 ;; esac
case "${BUILD_OVN:-0}" in 0 | 1) ;; *) echo 'build-packages: BUILD_OVN must be 0 or 1' >&2; exit 1 ;; esac
case "${DISCARD_PACKAGE_CACHE:-0}" in 0 | 1) ;; *) echo 'build-packages: DISCARD_PACKAGE_CACHE must be 0 or 1' >&2; exit 1 ;; esac

packages_root="${source_root}/os-image/packages"
if [[ ! -d "${packages_root}" || ! -d "${source_root}/make" ]]; then
  echo "build-packages: SOURCE_ROOT must contain os-image/packages and make" >&2
  exit 1
fi
mkdir -p "${cache_dir}" "${out_dir}" "${bin_dir}"

packages=(kubeadm-distro cni-calico)
if [[ "${BUILD_OVN:-0}" == 1 ]]; then
  packages+=(cni-ovn)
fi

for package in "${packages[@]}"; do
  args=("ARCH=${ARCH}" "BIN_DIR=${bin_dir}" "DIST_DIR=${cache_dir}/${package}" "OUT_DIR=${out_dir}" "DISCARD_PACKAGE_CACHE=${DISCARD_PACKAGE_CACHE:-0}")
  case "${package}" in
    kubeadm-distro) [[ -z "${K8S_VERSIONS:-}" ]] || args+=("K8S_VERSIONS=${K8S_VERSIONS}") ;;
    cni-calico) [[ -z "${CALICO_VERSIONS:-}" ]] || args+=("CALICO_VERSIONS=${CALICO_VERSIONS}") ;;
    cni-ovn) [[ -z "${OVN_STREAMS:-}" ]] || args+=("OVN_STREAMS=${OVN_STREAMS}") ;;
  esac
  make -C "${packages_root}/${package}" "${operation}" "${args[@]}"
  if [[ "${operation}" == lock ]]; then
    mkdir -p "${out_dir}/${package}"
    cp -R "${packages_root}/${package}/lock/${ARCH}" "${out_dir}/${package}/"
  elif [[ "${package}" == kubeadm-distro && "${DISCARD_PACKAGE_CACHE:-0}" == 1 ]]; then
    # The guest fallback has a small root disk. kubeadm's cache is no longer
    # needed once its debs exist, while the Docker builder retains its volume.
    rm -rf "${cache_dir}/${package}"
  fi
done

if [[ "${operation}" == debs ]]; then
  shopt -s nullglob
  debs=("${out_dir}"/*.deb)
  if (( ${#debs[@]} == 0 )); then
    echo 'build-packages: package build produced no debs' >&2
    exit 1
  fi
  cd "${out_dir}"
  printf '%s\n' "${ARCH}" > ARCH
  sha256sum ./*.deb Packages ARCH > SHA256SUMS
fi
