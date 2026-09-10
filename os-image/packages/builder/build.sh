#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Standalone host entrypoint for package builds in a native Linux container.
set -euo pipefail

operation="${1:-debs}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
engine="${CONTAINER_ENGINE:-docker}"
case "${ARCH:-$(uname -m)}" in
  arm64 | aarch64) ARCH=arm64 ;;
  amd64 | x86_64) ARCH=amd64 ;;
  *) echo 'builder: ARCH must be amd64 or arm64' >&2; exit 1 ;;
esac
case "${operation}" in
  image | debs | lock) ;;
  *) echo 'builder: expected image, debs, or lock' >&2; exit 1 ;;
esac
engine_platform="$("${engine}" info --format '{{.OSType}}/{{.Architecture}}')"
case "${engine_platform}" in
  linux/aarch64 | linux/arm64) engine_arch=arm64 ;;
  linux/x86_64 | linux/amd64) engine_arch=amd64 ;;
  *) echo "builder: unsupported container engine platform ${engine_platform}" >&2; exit 1 ;;
esac
if [[ "${engine_arch}" != "${ARCH}" ]]; then
  echo "builder: native ${ARCH} Linux engine required; engine is ${engine_platform}" >&2
  exit 1
fi
builder_image="${BUILDER_IMAGE:-nke-package-builder:${ARCH}}"
if [[ "${operation}" == image ]]; then
  exec "${engine}" build --platform "linux/${ARCH}" \
    --build-arg "CRANE_VERSION=${CRANE_VERSION:?set CRANE_VERSION via make}" \
    --build-arg "NFPM_VERSION=${NFPM_VERSION:?set NFPM_VERSION via make}" \
    --tag "${builder_image}" "${repo_root}/os-image/packages/builder"
fi
artifacts="${ARTIFACTS_DIR:-${repo_root}/os-image/packer/artifacts/${ARCH}}"
cache_volume="${BUILDER_CACHE_VOLUME:-nke-package-builder-${ARCH}}"
cache="${BUILDER_CACHE:-${repo_root}/os-image/.cache/package-builder/${ARCH}}"
mkdir -p "${artifacts}" "${cache}"
artifacts="$(cd "${artifacts}" && pwd)"
cache="$(cd "${cache}" && pwd)"
output="$(mktemp -d "${artifacts}/.${operation}.XXXXXX")"
trap 'rm -rf "${output}"' EXIT
version_args=()
for name in K8S_VERSIONS CALICO_VERSIONS OVN_STREAMS; do
  if [[ -n "${!name:-}" ]]; then
    version_args+=(-e "${name}=${!name}")
  fi
done
secret_args=()
if [[ "${BUILD_OVN:-0}" == 1 ]]; then
  secret_args=(-e NGC_API_KEY)
fi
"${engine}" run --rm --platform "linux/${ARCH}" \
  --user "$(id -u):$(id -g)" --cap-drop ALL --security-opt no-new-privileges \
  --mount "type=bind,src=${repo_root},dst=/src,readonly" \
  --mount "type=bind,src=${output},dst=/out" \
  --mount "type=volume,src=${cache_volume},dst=/cache" \
  -e HOME=/tmp -e "ARCH=${ARCH}" -e "BUILD_OPERATION=${operation}" \
  -e "BUILD_OVN=${BUILD_OVN:-0}" "${version_args[@]}" "${secret_args[@]}" \
  "${builder_image}" bash /src/os-image/packages/builder/inside.sh
# Publish only complete results; retain the previous build for review/recovery.
if [[ -e "${artifacts}/${operation}" ]]; then
  previous="$(mktemp -d "${cache}/${operation}-previous.XXXXXX")"
  mv "${artifacts}/${operation}" "${previous}/${operation}"
fi
mv "${output}" "${artifacts}/${operation}"
trap - EXIT
echo "builder: exported ${artifacts}/${operation}"
