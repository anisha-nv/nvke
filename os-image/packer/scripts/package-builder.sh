#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Packer adapter: native package targets or the Nix-owned build-packages app.
set -euo pipefail
arch=${ARCH:-amd64}
case "$arch" in amd64|arm64) ;; *) echo 'ARCH must be amd64 or arm64' >&2; exit 1;; esac
native=0
case "$(uname -s)/$(uname -m)/$arch" in
  Linux/x86_64/amd64|Linux/amd64/amd64|Linux/aarch64/arm64|Linux/arm64/arm64) native=1 ;;
esac
mode=${NKE_PACKAGE_BUILDER:-auto}
if [[ "$mode" == auto ]]; then
  if [[ "$native" == 1 ]]; then mode=host; else mode=nix; fi
fi
case "$mode" in
  host)
    [[ "$native" == 1 ]] || { echo 'host package builds require a matching Linux host' >&2; exit 1; }
    command -v go >/dev/null || { echo 'go is required for host package builds' >&2; exit 1; }
    ;;
  nix)
    command -v nix >/dev/null || {
      echo 'nix is required for VM package builds.' >&2
      echo 'Install Nix and enable nix-command and flakes; see os-image/nix/README.md.' >&2
      exit 1
    }
    ;;
  *) echo 'NKE_PACKAGE_BUILDER must be auto, host, or nix' >&2; exit 1 ;;
esac
echo "package builder: $mode ($arch)"
[[ "${1:-}" != --check ]] || exit 0
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
if [[ "$mode" == nix ]]; then
  exec nix run "$repo_root/os-image#build-packages-$arch" -- \
    --source "$repo_root" --output "${ARTIFACTS_DIR:?ARTIFACTS_DIR is required}"
fi
export NKE_BUILD_IN_PLACE=1
exec bash "$repo_root/os-image/packer/scripts/fetch-artifacts.sh"
