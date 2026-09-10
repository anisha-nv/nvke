#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Run as root in the disposable smoke-test guest, whose network has no egress.
set -euo pipefail
cd /usr/lib/nke/debs
[[ "$(cat ARCH)" == "$(dpkg --print-architecture)" ]]
sha256sum -c SHA256SUMS
apt_args=(-o Dir::Etc::sourcelist=/etc/apt/sources.list.d/nke-local.list -o Dir::Etc::sourceparts=-)
apt-get "${apt_args[@]}" update
export DEBIAN_FRONTEND=noninteractive
# Leaving globs literal when absent deliberately makes missing packages fail.
for deb in ./nke-kubeadm-distro-*.deb; do
  package="$(dpkg-deb -f "${deb}" Package)"
  version="${package#nke-kubeadm-distro-}"
  apt-get "${apt_args[@]}" install -y "${package}"
  [[ "$(kubeadm version -o short)" == "v${version}" ]]
  systemctl is-active containerd
  kubeadm config images list --kubernetes-version "v${version}" | while read -r ref; do
    ctr -n k8s.io images ls -q | grep -Fx "${ref}"
  done
done
for deb in ./nke-cni-calico-*.deb; do
  apt-get "${apt_args[@]}" install -y "$(dpkg-deb -f "${deb}" Package)"
done
echo 'boot-smoke: every staged distro version and Calico installed without network access'
