#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Fills the placeholders in plan.json from the same SITE_E2E_* and QUALIFY_*
# repository variables forge-site-controller-e2e.yml itself falls back to,
# then -- per stream, same as the qualify job in qualify.yml does -- decodes
# just that stream into the shape forge-site-controller-e2e.yml's
# stream-json input takes and dispatches it.
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
# settings is picked up without touching this script. A variable that
# genuinely does not exist resolves to empty, same as it would inside the
# scheduled workflow; a failed gh call (auth, wrong repo) warns instead of
# silently resolving the same way.
var_names=(QUALIFY_VERSION QUALIFY_KIND_VERSION QUALIFY_CLUSTERCTL_VERSION QUALIFY_NERDCTL_VERSION
           SITE_E2E_CONTROLLER_VERSION SITE_E2E_CLI_VERSION SITE_E2E_OS_IMAGE_VERSION
           SITE_E2E_CAPI_PROVISIONED_VERSION SITE_E2E_CNI_OVN_VERSION SITE_E2E_KUBERNETES_VERSION
           SITE_E2E_OS_IMAGE_ARM64 SITE_E2E_OS_IMAGE_BOOTLOADER_ARM64
           SITE_E2E_OS_IMAGE_AMD64 SITE_E2E_OS_IMAGE_BOOTLOADER_AMD64)
envsubst_names=""
for name in "${var_names[@]}"; do
  value="$(gh variable get "${name}" --repo "${REPO}" 2>&1)" || {
    echo "warning: gh variable get ${name} failed: ${value}" >&2
    value=""
  }
  export "${name}=${value}"
  envsubst_names="${envsubst_names}\${${name}} "
done

# Restricted to the names above: a bare envsubst replaces every $VAR-shaped
# piece of text in the file, which would corrupt any literal $ a URL or
# value happens to contain.
plan="$(envsubst "${envsubst_names}" < "${DIR}/plan.json" | jq -c .)"
names="$(jq -r '[.combinations[] as $c | $c.streams[] | .name] | .[]' <<<"${plan}")"

while IFS= read -r name; do
  # One flat object: this stream's own capabilities, the component versions
  # from the combination it belongs to, and -- from its own os-image
  # dependency's artifacts -- just the one image and bootloader URL matching
  # its own arch.
  stream="$(jq -c --arg name "${name}" '
    .combinations[] as $c |
    ($c.streams[] | select(.name==$name)) as $s |
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
  ' <<<"${plan}")"

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
done <<<"${names}"
