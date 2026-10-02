#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Report whether the runner, the repository checkout and the site cluster are
# ready for the install. Changes nothing. `run` and `check` are the same.
#
# Usage: ./00-check-readiness.sh [run|check]
#
# The guide docs/site-bootstrap/install-site-controller.md is the authority.
# Commands between `# doc-begin` and `# doc-end` are extracted verbatim into
# the guide. Keep them copy-pasteable: use the lib variables, no functions.
#
# Output is one line per finding: PASS, WARN, FAIL or SKIP. The exit code is
# the number of FAIL lines. Checks that need the cluster or the network print
# SKIP under DRY_RUN.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

# Free space the runner needs for rendered values, diagnostics and image tarballs.
MIN_DISK_GB="${MIN_DISK_GB:-10}"

# doc-begin
# Hosts the install pulls from, sorted by host. Stage 4 fetches Cluster API
# and cert-manager from github.com. Stage 5 pulls charts and images from the
# rest: Postgres and the Bitnami OCI chart from registry-1.docker.io (where
# docker.io image pulls land), SPIRE from spiffe.github.io, Temporal from
# go.temporal.io, CAPNICo from ghcr.io, the nktp chart dependencies from the
# three repositories named in nktp/deploy/charts/nktp/Chart.yaml, and the
# nktp images from docker.io, ghcr.io, quay.io and registry.k8s.io.
EGRESS_URLS=(
  https://ghcr.io/v2/
  https://github.com/
  https://go.temporal.io/helm-charts/index.yaml
  https://kubernetes-sigs.github.io/metrics-server/index.yaml
  https://open-telemetry.github.io/opentelemetry-helm-charts/index.yaml
  https://prometheus-community.github.io/helm-charts/index.yaml
  https://quay.io/v2/
  https://registry-1.docker.io/v2/
  https://registry.k8s.io/v2/
  https://spiffe.github.io/helm-charts-hardened/index.yaml
)
# doc-end

check_tools() {
  local tool
  for tool in kubectl helm clusterctl python3 curl; do
    if command -v "${tool}" >/dev/null 2>&1; then
      check_pass "tool ${tool}: $(command -v "${tool}")"
    else
      check_fail "tool ${tool}: not found in PATH"
    fi
  done
}

# check_helm_version compares helm against the repository pin. The pinned
# binary under controller/.local/bin wins when it exists.
check_helm_version() {
  local pin bin path_bin version
  pin="$(mk_var HELM_VERSION)"
  path_bin="$(command -v helm 2>/dev/null || true)"
  bin="${SBOM_DIR}/../.local/bin/helm"
  [[ -x "${bin}" ]] || bin="${path_bin}"
  if [[ -z "${bin}" ]]; then
    check_fail "helm: no binary found to compare with the pin ${pin:-unknown}"
    return 0
  fi
  version="$("${bin}" version --template '{{.Version}}' 2>/dev/null || true)"
  if [[ -n "${pin}" && "${version}" == "${pin}" ]]; then
    check_pass "helm ${version} at ${bin} matches the pin in make/tool-versions.mk"
  else
    check_warn "helm ${version:-unknown} at ${bin}; the repository pins ${pin:-unknown} in make/tool-versions.mk"
  fi
  if [[ "${bin}" != "${path_bin}" ]]; then
    check_warn "the stages call helm from PATH (${path_bin:-none}); put the pinned binary first: export PATH=\"$(dirname "${bin}"):\${PATH}\""
  fi
}

check_checkout() {
  local f
  for f in "${SBOM_DIR}/nke-sbom/nke-deploy.yaml" \
    "${SBOM_DIR}/charts/controller/Chart.yaml" \
    "${TOOL_VERSIONS_MK}"; do
    if [[ -f "${f}" ]]; then
      check_pass "checkout: ${f}"
    else
      check_fail "checkout: ${f} is missing. The install needs a full repository checkout."
    fi
  done
}

check_cluster() {
  local nodes count archs
  if [[ -n "${DRY_RUN}" ]]; then
    kc version
    kc get nodes -o custom-columns='NAME:.metadata.name,ARCH:.metadata.labels.kubernetes\.io/arch'
    check_skip "cluster: API, node count and architecture need a cluster (DRY_RUN)"
    return 0
  fi
  if ! kc version >/dev/null 2>&1; then
    check_fail "cluster: API not reachable with ${SITE_KUBECONFIG}"
    return 0
  fi
  check_pass "cluster: API reachable with ${SITE_KUBECONFIG}"
  nodes="$(kc get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.labels.kubernetes\.io/arch}{"\n"}{end}' 2>/dev/null || true)"
  count="$(printf '%s\n' "${nodes}" | sed '/^$/d' | wc -l | tr -d ' ')"
  if [[ "${count}" -ge 1 && $((count % 2)) -eq 1 ]]; then
    check_pass "cluster: ${count} node(s), an odd number"
  else
    check_fail "cluster: ${count} node(s); expected an odd number, at least 1"
  fi
  archs="$(printf '%s\n' "${nodes}" | awk 'NF { print $2 }' | sort -u | tr '\n' ' ')"
  archs="${archs% }"
  if [[ "${archs}" == *" "* ]]; then
    check_warn "cluster: mixed node architectures (${archs}); stage 2 must stage images for each"
  else
    check_pass "cluster: node architecture ${archs:-unknown}"
  fi
}

check_egress() {
  local url
  for url in "${EGRESS_URLS[@]}"; do
    if [[ -n "${DRY_RUN}" ]]; then
      run curl -sSI --max-time 10 "${url}"
      check_skip "egress: ${url} (DRY_RUN)"
    elif curl -sSI --max-time 10 -o /dev/null "${url}" 2>/dev/null; then
      check_pass "egress: ${url}"
    else
      check_fail "egress: ${url} not reachable within 10s"
    fi
  done
}

check_site_config() {
  if [[ -f "${SITE_CONFIG}" ]]; then
    check_pass "site config: ${SITE_CONFIG}"
  else
    check_warn "site config: ${SITE_CONFIG} not found; stage 3 (describe-site) needs it"
  fi
}

# check_api_auth warns when the controller overlay leaves apiAuth disabled.
check_api_auth() {
  local overlay="${SBOM_DIR}/nke-sbom/controller/overlay.yaml"
  if [[ ! -f "${overlay}" ]]; then
    check_fail "api auth: ${overlay} is missing"
    return 0
  fi
  if awk '
    /^apiAuth:/ { in_block = 1; next }
    in_block && /^[^[:space:]#]/ { in_block = 0 }
    in_block && /^[[:space:]]+enabled:[[:space:]]*false/ { found = 1 }
    END { exit !found }' "${overlay}"; then
    check_warn "api auth: apiAuth.enabled is false in ${overlay}. The controller API is unauthenticated on NodePort 30080. The comment above apiAuth in that file says how to turn it on"
  else
    check_pass "api auth: apiAuth is not disabled in ${overlay}"
  fi
}

check_disk() {
  local avail_kb avail_gb
  avail_kb="$(df -Pk "${OUT_DIR}" 2>/dev/null | awk 'NR == 2 { print $4 }')"
  if [[ -z "${avail_kb}" ]]; then
    check_warn "disk: cannot read free space for ${OUT_DIR}"
    return 0
  fi
  avail_gb=$((avail_kb / 1024 / 1024))
  if [[ "${avail_gb}" -ge "${MIN_DISK_GB}" ]]; then
    check_pass "disk: ${avail_gb} GB free under ${OUT_DIR}"
  else
    check_warn "disk: ${avail_gb} GB free under ${OUT_DIR}; stage 2 image tarballs and diagnostics want at least ${MIN_DISK_GB} GB"
  fi
}

stage_check() {
  check_tools
  check_helm_version
  check_checkout
  check_cluster
  check_egress
  check_site_config
  check_api_auth
  check_disk
}

# The readiness report is the whole stage. A failing report stops install-all.
stage_run() {
  stage_check
  [[ "${CHECK_FAILURES}" -eq 0 ]] || die "readiness check failed: ${CHECK_FAILURES} finding(s)"
}

stage_main "$@"
