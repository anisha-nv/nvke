#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Asserts that the CAPI ClusterRole (rbac-capi.yaml) grants CWE's own service
# account access to every infrastructure provider kind CWE renders, and to
# each kind's CRD. Nothing else in the chart checks this: chart-rbac-check
# validates the tenant-facing nke:cwe ClusterRole, a different RBAC surface
# entirely. A provider kind present in the controller's Go code but missing
# here renders and lints cleanly -- CWE only discovers the gap at apply time,
# denied by the API server.

set -euo pipefail

CHART="${CHART:-"$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/deploy/charts/controller"}"
[[ -d "${CHART}" ]] || { echo "chart not found at ${CHART}" >&2; exit 2; }

failures=0

# Kind, plural resource name. Add a row here when a provider's Go code starts
# rendering a new infrastructure.cluster.x-k8s.io kind; nothing else derives
# this list.
providers=(
  "LaunchLayerCluster:launchlayerclusters"
  "LaunchLayerMachine:launchlayermachines"
  "LaunchLayerMachineTemplate:launchlayermachinetemplates"
  "NicoCluster:nicoclusters"
  "NicoMachine:nicomachines"
  "ProvisionedCluster:provisionedclusters"
  "ProvisionedMachine:provisionedmachines"
  "ProvisionedMachineTemplate:provisionedmachinetemplates"
)

out="$(helm template cwe "${CHART}" --set capi.rbac.create=true)"
role="$(awk '/^kind: ClusterRole$/{c=1} c{print} c && /^---/{exit}' <<<"${out}")"
if [[ -z "${role}" ]]; then
  echo "ERROR: no ClusterRole rendered with capi.rbac.create=true" >&2
  exit 1
fi

resource_rule="$(awk '/infrastructure.cluster.x-k8s.io/{f=1} f{print} f && /verbs:/{exit}' <<<"${role}")"
crd_rule="$(awk '/apiextensions.k8s.io/{f=1} f{print} f && /verbs:/{exit}' <<<"${role}")"

echo "CAPI ClusterRole grants every provider kind CWE renders:"
for entry in "${providers[@]}"; do
  kind="${entry%%:*}"
  plural="${entry##*:}"
  if grep -qx "      - ${plural}" <<<"${resource_rule}"; then
    printf '  ok    %s (resource)\n' "${kind}"
  else
    printf '  FAIL  %s: %s missing from the infrastructure.cluster.x-k8s.io resource list\n' "${kind}" "${plural}" >&2
    failures=$((failures + 1))
  fi
  if grep -qx "      - ${plural}.infrastructure.cluster.x-k8s.io" <<<"${crd_rule}"; then
    printf '  ok    %s (CRD)\n' "${kind}"
  else
    printf '  FAIL  %s: %s.infrastructure.cluster.x-k8s.io missing from the CRD resourceNames list\n' "${kind}" "${plural}" >&2
    failures=$((failures + 1))
  fi
done

if [[ "${failures}" -ne 0 ]]; then
  echo "${failures} chart CAPI RBAC assertion(s) failed" >&2
  exit 1
fi

echo "all chart CAPI RBAC assertions passed"
