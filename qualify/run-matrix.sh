#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Resolves each stream in qualification.yaml against versions.yaml and
# dispatches forge-site-controller-e2e.yml once per stream.
#
# Usage: qualify/run-matrix.sh [ref]
# Set DRY_RUN=1 to print the resolved gh workflow run command instead of
# executing it.
set -Eeuo pipefail

REPO="dsx-ai-factory/nke"
REF="${1:-main}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

stream_count="$(yq '.streams | length' "${DIR}/qualification.yaml")"

for ((i=0; i<stream_count; i++)); do
  name="$(yq ".streams[$i].name" "${DIR}/qualification.yaml")"
  arch="$(yq ".streams[$i].arch" "${DIR}/qualification.yaml")"
  gpu="$(yq ".streams[$i].gpu" "${DIR}/qualification.yaml")"
  kube="$(yq ".streams[$i].kube" "${DIR}/qualification.yaml")"
  os="$(yq ".streams[$i].os" "${DIR}/qualification.yaml")"

  kube_version="$(yq ".kubernetes.\"${kube}\"" "${DIR}/versions.yaml")"
  image="$(yq ".os-images.\"${os}\".\"${arch}\".image" "${DIR}/versions.yaml")"
  bootloader="$(yq ".os-images.\"${os}\".\"${arch}\".bootloader" "${DIR}/versions.yaml")"

  echo "=== ${name} ==="
  echo "  arch=${arch} gpu=${gpu} kubernetes-version=${kube_version}"
  echo "  os-image-${arch}=${image}"
  echo "  os-image-bootloader-${arch}=${bootloader}"

  cmd=(gh workflow run forge-site-controller-e2e.yml
    --repo "${REPO}"
    --ref "${REF}"
    -f stage=full
    -f arch="${arch}"
    -f gpu="${gpu}"
    -f kubernetes-version="${kube_version}"
    -f "os-image-${arch}=${image}"
    -f "os-image-bootloader-${arch}=${bootloader}")

  if [[ "${DRY_RUN:-0}" == "1" ]]; then
    printf -v quoted '%q ' "${cmd[@]}"
    echo "  [dry-run] ${quoted}"
  else
    "${cmd[@]}"
  fi
  echo
done
