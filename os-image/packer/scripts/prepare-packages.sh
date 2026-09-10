#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Packer host: attempt the container build, always supply locked guest sources.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
artifacts="${ARTIFACTS_DIR:?ARTIFACTS_DIR is required}"
ARCH="${ARCH:?ARCH is required}"
case "${ARCH}" in amd64 | arm64) ;; *) echo 'ARCH must be amd64 or arm64' >&2; exit 1 ;; esac
BUILD_OVN="${BUILD_OVN:-0}"
case "${BUILD_OVN}" in 0 | 1) ;; *) echo 'BUILD_OVN must be 0 or 1' >&2; exit 1 ;; esac
cache="${BUILDER_CACHE:-${repo_root}/os-image/.cache/package-builder/${ARCH}}"
mkdir -p "$(dirname "${artifacts}")" "${cache}"
# Start a fresh upload tree, preserving all previous inputs outside it. This
# also excludes interrupted container outputs and unrelated local files.
if [[ -e "${artifacts}" ]]; then
  previous="$(mktemp -d "${cache}/package-inputs-previous.XXXXXX")"
  mv "${artifacts}" "${previous}/inputs"
fi
mkdir -p "${artifacts}"
artifacts="$(cd "${artifacts}" && pwd)"
source_root="${artifacts}/package-source"
# Explicit build inputs only: never recursively copy a developer's working
# tree, which may contain caches or local credentials.
inputs=(
  make/tool-versions.mk
  os-image/packages/builder/guest.sh
  os-image/packages/builder/build-packages.sh
)
for package in kubeadm-distro cni-calico cni-ovn; do
  inputs+=("os-image/packages/${package}/Makefile" "os-image/packages/${package}/bundle.sh")
done
for file in \
  cni-common/deb.mk cni-common/bundle-lib.sh cni-common/build-deb.sh \
  cni-common/postinst cni-common/nfpm-images.yaml \
  cni-calico/packaging/nfpm-manifests.yaml cni-ovn/packaging/nfpm-charts.yaml \
  kubeadm-distro/packaging/build-deb.sh kubeadm-distro/packaging/nfpm.yaml \
  kubeadm-distro/packaging/postinst kubeadm-distro/packaging/prerm \
  kubeadm-distro/packaging/postrm kubeadm-distro/packaging/kubelet.default \
  kubeadm-distro/packaging/keyrings/runc.keyring \
  kubeadm-distro/packaging/systemd/kubelet.service \
  kubeadm-distro/packaging/systemd/10-kubeadm.conf \
  kubeadm-distro/packaging/systemd/containerd.service; do
  inputs+=("os-image/packages/${file}")
done
shopt -s nullglob
for lock in "${repo_root}"/os-image/packages/*/lock/"${ARCH}"/*.lock; do
  inputs+=("${lock#"${repo_root}/"}")
done
for file in "${inputs[@]}"; do
  mkdir -p "${source_root}/$(dirname "${file}")"
  cp "${repo_root}/${file}" "${source_root}/${file}"
done
# Only build choices, never credentials, cross the host/guest boundary.
for name in ARCH BUILD_OVN K8S_VERSIONS CALICO_VERSIONS OVN_STREAMS; do
  printf '%s=%q\n' "${name}" "${!name:-}"
done > "${artifacts}/package-build.conf"
engine="${CONTAINER_ENGINE:-docker}"
if command -v "${engine}" >/dev/null 2>&1 && \
  make -C "${repo_root}/os-image" packages "ARCH=${ARCH}" "ARTIFACTS_DIR=${artifacts}"; then
  echo 'prepare-packages: container debs ready for the guest'
else
  echo 'prepare-packages: local container build unavailable or failed; the guest will build from the staged locks' >&2
fi
