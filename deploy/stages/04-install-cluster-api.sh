#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Install Cluster API core, the kubeadm providers and cert-manager at the
# version the repository pins.
#
# Usage: ./04-install-cluster-api.sh [run|check]
#
# The guide docs/site-bootstrap/install-site-controller.md describes this
# stage. The commands between `# doc-begin` and `# doc-end` are the stage's
# work; keep them readable on their own: lib variables, no functions.
#
# CAPI_VERSION and CERT_MANAGER_VERSION come from make/tool-versions.mk. The
# clusterctl binary decides which cert-manager it installs, so the binary must
# match CAPI_VERSION. The run step skips when capi-controller-manager is
# already Available at CAPI_VERSION. A different installed version is an error:
# `clusterctl init` does not upgrade. See docs/site-bootstrap/infrastructure-providers.md.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"

CAPI_VERSION="${CAPI_VERSION:-$(mk_var CAPI_VERSION)}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-$(mk_var CERT_MANAGER_VERSION)}"
[[ -n "${CAPI_VERSION}" ]] || die "CAPI_VERSION not found in ${TOOL_VERSIONS_MK}"
export CAPI_VERSION CERT_MANAGER_VERSION

# deployment_image prints the first container image of a Deployment, or nothing.
deployment_image() {
  kc -n "$1" get deployment "$2" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || true
}

# deployment_available prints the Available condition status of a Deployment, or nothing.
deployment_available() {
  kc -n "$1" get deployment "$2" -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null || true
}

stage_run() {
  local image installed binary
  require_cmd clusterctl

  binary="$(clusterctl version -o short 2>/dev/null || true)"
  [[ "${binary}" == "${CAPI_VERSION}" ]] ||
    warn "clusterctl ${binary:-unknown} does not match CAPI_VERSION ${CAPI_VERSION}; it decides the cert-manager version"

  if [[ -z "${DRY_RUN}" ]]; then
    image="$(deployment_image capi-system capi-controller-manager)"
    installed="${image##*:}"
    if [[ -n "${image}" && "${installed}" == "${CAPI_VERSION}" &&
      "$(deployment_available capi-system capi-controller-manager)" == "True" ]]; then
      log "Cluster API ${CAPI_VERSION} already installed"
      return 0
    fi
    if [[ -n "${image}" && "${installed}" != "${CAPI_VERSION}" ]]; then
      die "Cluster API ${installed} is installed; this stage installs ${CAPI_VERSION}. Use clusterctl upgrade, see docs/operations/upgrade.md"
    fi
  fi

  # doc-begin
  # Pinned, so clusterctl does not install whatever is latest. Installs
  # cert-manager too. Safe to rerun: clusterctl skips what is present.
  run clusterctl init --kubeconfig "${SITE_KUBECONFIG}" \
    --core "cluster-api:${CAPI_VERSION}" \
    --bootstrap "kubeadm:${CAPI_VERSION}" \
    --control-plane "kubeadm:${CAPI_VERSION}" \
    --wait-providers
  # doc-end
}

stage_check() {
  local entry ns name available established image tag
  if [[ -n "${DRY_RUN}" ]]; then
    check_skip "Deployments and CRDs need a cluster (DRY_RUN)"
    return 0
  fi

  for entry in \
    cert-manager/cert-manager \
    cert-manager/cert-manager-cainjector \
    cert-manager/cert-manager-webhook \
    capi-system/capi-controller-manager \
    capi-kubeadm-bootstrap-system/capi-kubeadm-bootstrap-controller-manager \
    capi-kubeadm-control-plane-system/capi-kubeadm-control-plane-controller-manager; do
    ns="${entry%/*}"
    name="${entry#*/}"
    available="$(deployment_available "${ns}" "${name}")"
    if [[ "${available}" == "True" ]]; then
      check_pass "deployment ${entry} is Available"
    else
      check_fail "deployment ${entry} is not Available (status ${available:-missing})"
    fi
  done

  established="$(kc get crd clusters.cluster.x-k8s.io -o jsonpath='{.status.conditions[?(@.type=="Established")].status}' 2>/dev/null || true)"
  if [[ "${established}" == "True" ]]; then
    check_pass "CRD clusters.cluster.x-k8s.io is Established"
  else
    check_fail "CRD clusters.cluster.x-k8s.io is not Established (status ${established:-missing})"
  fi

  image="$(deployment_image capi-system capi-controller-manager)"
  tag="${image##*:}"
  if [[ -n "${image}" && "${tag}" == "${CAPI_VERSION}" ]]; then
    check_pass "Cluster API ${tag} matches CAPI_VERSION"
  else
    check_fail "Cluster API ${tag:-missing} does not match CAPI_VERSION ${CAPI_VERSION}"
  fi

  image="$(deployment_image cert-manager cert-manager)"
  tag="${image##*:}"
  if [[ -z "${CERT_MANAGER_VERSION}" || "${tag}" == "${CERT_MANAGER_VERSION}" ]]; then
    check_pass "cert-manager ${tag:-unknown} matches CERT_MANAGER_VERSION"
  else
    check_warn "cert-manager ${tag:-missing} does not match CERT_MANAGER_VERSION ${CERT_MANAGER_VERSION}; the clusterctl binary chose it"
  fi
}

stage_main "$@"
