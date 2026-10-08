#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Fills the placeholders in plan.json from the same SITE_E2E_* and QUALIFY_*
# repository variables forge-site-controller-e2e.yml itself falls back to,
# flattens each combination/stream pair into that workflow's stream-json
# shape, then dispatches it once per stream.
#
# Usage: qualify/run-matrix.sh [ref]
# Set DRY_RUN=1 to print the resolved gh workflow run command instead of
# executing it.
set -Eeuo pipefail

REPO="dsx-ai-factory/nke"
REF="${1:-main}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# The exact set plan.json's placeholders reference. Pulled from the
# repository variables rather than hardcoded, so a value changed in GitHub
# settings is picked up without touching this script. An unset variable
# resolves to empty, same as it would inside the scheduled workflow.
for name in QUALIFY_VERSION QUALIFY_KIND_VERSION QUALIFY_CLUSTERCTL_VERSION QUALIFY_NERDCTL_VERSION \
            SITE_E2E_CONTROLLER_VERSION SITE_E2E_CLI_VERSION SITE_E2E_OS_IMAGE_VERSION \
            SITE_E2E_CAPI_PROVISIONED_VERSION SITE_E2E_CNI_OVN_VERSION SITE_E2E_KUBERNETES_VERSION \
            SITE_E2E_OS_IMAGE_ARM64 SITE_E2E_OS_IMAGE_BOOTLOADER_ARM64 \
            SITE_E2E_OS_IMAGE_AMD64 SITE_E2E_OS_IMAGE_BOOTLOADER_AMD64; do
  export "${name}=$(gh variable get "${name}" --repo "${REPO}" 2>/dev/null || true)"
done

resolved="$(envsubst < "${DIR}/plan.json")"

# Each stream becomes one flat object: its own capabilities, the component
# versions from the combination it belongs to, and -- from its own os-image
# dependency's artifacts -- just the one image and bootloader URL matching
# its own arch. This is the exact shape forge-site-controller-e2e.yml's
# stream-json input takes.
matrix="$(jq -c '
  [
    .combinations[] as $c |
    $c.streams[] as $s |
    ($s.capabilities.arch) as $arch |
    ($s.dependencies[] | select(.name=="os-image") | .artifacts) as $artifacts |
    (reduce ($artifacts[] | select(.arch==$arch)) as $a
       ({}; . + {($a.name + "-" + $a.arch): $a.url})
    ) as $images |
    ({
      name: $s.name,
      arch: $arch,
      gpu: ($s.capabilities.gpu // ""),
      "kubernetes-version": (($s.dependencies[] | select(.name=="kube") | .version) // ""),
      "controller-version": (($c.components[] | select(.name=="controller") | .version) // ""),
      "cli-version": (($c.components[] | select(.name=="cli") | .version) // ""),
      "capi-provisioned-version": (($c.components[] | select(.name=="capi-provisioned") | .version) // ""),
      "cni-ovn-version": (($c.dependencies[] | select(.name=="nke-cni-ovn") | .version) // "")
    } + $images)
  ]
' <<<"${resolved}")"

stream_count="$(jq 'length' <<<"${matrix}")"

for ((i=0; i<stream_count; i++)); do
  stream="$(jq -c ".[$i]" <<<"${matrix}")"
  name="$(jq -r '.name' <<<"${stream}")"

  echo "=== ${name} ==="
  jq . <<<"${stream}"

  cmd=(gh workflow run forge-site-controller-e2e.yml
    --repo "${REPO}"
    --ref "${REF}"
    -f stage=full
    -f "stream-json=${stream}")

  if [[ "${DRY_RUN:-0}" == "1" ]]; then
    printf -v quoted '%q ' "${cmd[@]}"
    echo "  [dry-run] ${quoted}"
  else
    "${cmd[@]}"
  fi
  echo
done
