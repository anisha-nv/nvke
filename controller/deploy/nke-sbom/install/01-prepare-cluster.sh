#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Make the site cluster schedulable and give it a default StorageClass.
#
# Usage: ./01-prepare-cluster.sh [run|check]
#
# The guide docs/site-bootstrap/install-site-controller.md is the authority.
# Commands between `# doc-begin` and `# doc-end` are extracted verbatim into
# the guide. Keep them copy-pasteable: use the lib variables, no functions.
#
# The control-plane NoSchedule taint is removed only from nodes that carry it,
# so a rerun changes nothing. The StorageClass step installs rancher
# local-path-provisioner only when no StorageClass is the default. A site with
# its own CSI driver sets SKIP_STORAGECLASS=1 and marks its own class default;
# the check still requires exactly one default StorageClass.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

# The same release docs/operations/faq.md installs. local-path stores volumes
# under /opt/local-path-provisioner on the node that schedules the pod. It is
# a single-node answer; it does not survive that node.
LOCAL_PATH_VERSION="${LOCAL_PATH_VERSION:-v0.0.31}"
LOCAL_PATH_URL="https://raw.githubusercontent.com/rancher/local-path-provisioner/${LOCAL_PATH_VERSION}/deploy/local-path-storage.yaml"
# Set to any value to leave StorageClasses alone.
SKIP_STORAGECLASS="${SKIP_STORAGECLASS:-}"
export LOCAL_PATH_VERSION LOCAL_PATH_URL SKIP_STORAGECLASS

# tainted_nodes prints the nodes that carry the control-plane NoSchedule taint.
tainted_nodes() {
  kc get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.taints[?(@.key=="node-role.kubernetes.io/control-plane")].effect}{"\n"}{end}' |
    awk '$2 ~ /NoSchedule/ { print $1 }'
}

# default_storageclasses prints the StorageClasses annotated as default.
default_storageclasses() {
  kc get storageclass -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.annotations.storageclass\.kubernetes\.io/is-default-class}{"\n"}{end}' |
    awk '$2 == "true" { print $1 }'
}

stage_run() {
  local tainted default_sc
  # The queries need a cluster. A dry run substitutes placeholders so the
  # mutations still print.
  tainted='<node>'
  default_sc=''
  if [[ -z "${DRY_RUN}" ]]; then
    tainted="$(tainted_nodes)"
    default_sc="$(default_storageclasses)"
  fi

  # doc-begin
  # Let workloads schedule on control-plane nodes. Only nodes that carry the
  # taint are touched.
  printf '%s\n' "${tainted}" | while read -r node; do
    [[ -n "${node}" ]] || continue
    kc taint nodes "${node}" node-role.kubernetes.io/control-plane:NoSchedule-
  done

  # Default StorageClass for Postgres and Temporal. Skipped when one exists
  # or when SKIP_STORAGECLASS is set.
  if [[ -z "${SKIP_STORAGECLASS}" && -z "${default_sc}" ]]; then
    kc apply -f "${LOCAL_PATH_URL}"
    kc annotate storageclass local-path storageclass.kubernetes.io/is-default-class=true --overwrite
    kc -n local-path-storage rollout status deployment/local-path-provisioner --timeout="${REQUIRE_TIMEOUT}"
  fi
  # doc-end
}

stage_check() {
  local tainted defaults count available
  if [[ -n "${DRY_RUN}" ]]; then
    check_skip "taints, default StorageClass and provisioner need a cluster (DRY_RUN)"
    return 0
  fi

  tainted="$(tainted_nodes | tr '\n' ' ')"
  if [[ -z "${tainted// /}" ]]; then
    check_pass "no node carries the control-plane NoSchedule taint"
  else
    check_fail "control-plane NoSchedule taint still on: ${tainted% }"
  fi

  defaults="$(default_storageclasses | tr '\n' ' ')"
  count="$(printf '%s' "${defaults}" | wc -w | tr -d ' ')"
  if [[ "${count}" -eq 1 ]]; then
    check_pass "default StorageClass: ${defaults% }"
  elif [[ "${count}" -eq 0 ]]; then
    check_fail "no default StorageClass. Run this stage, or install your CSI driver and annotate its class storageclass.kubernetes.io/is-default-class=true"
  else
    check_fail "more than one default StorageClass: ${defaults% }"
  fi

  if kc -n local-path-storage get deployment local-path-provisioner >/dev/null 2>&1; then
    available="$(kc -n local-path-storage get deployment local-path-provisioner -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null || true)"
    if [[ "${available}" == "True" ]]; then
      check_pass "local-path-provisioner is Available"
    else
      check_fail "local-path-provisioner is not Available (status ${available:-unknown})"
    fi
  else
    check_pass "local-path-provisioner not installed; the site provides its own storage"
  fi
}

stage_main "$@"
