#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
export NKE_BUILD_IN_PLACE=1 NFPM_BIN=/run/current-system/sw/bin/nfpm
for package in kubeadm-distro cni-calico cni-ovn spire-agent; do
  cache=/root/.cache/nke-packages/$package
  for directory in bin dist; do
    mkdir -p "$cache/$directory"
    ln -s "$cache/$directory" "$REPO_ROOT/os-image/packages/$package/$directory"
  done
  [[ -L "$cache/bin/nfpm" ]] || ln -s "$NFPM_BIN" "$cache/bin/nfpm"
done
bash "$REPO_ROOT/os-image/packer/scripts/fetch-artifacts.sh"
check-ubuntu-elf --arch "$ARCH" \
  "$REPO_ROOT/agent/bin/linux_$ARCH/nke-agent"
