#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Stage the non-image artifacts `nkectl bootstrap site` fetches from the
# internet -- the Postgres, Temporal and SPIRE Helm charts, the Cluster API
# provider manifests and cert-manager -- into a directory the bootstrap can be
# pointed at, so it runs with github.com, go.temporal.io, spiffe.github.io and
# registry-1.docker.io unreachable. Images are hack/mirror-bootstrap-images.sh's
# job; the two scripts together cover everything the bootstrap pulls.
#
# Usage:
#   hack/stage-bootstrap-artifacts.sh [DIR]
#
#   DIR  where to stage. Defaults to ./bootstrap-artifacts. Existing files are
#        kept, so a re-run only fetches what is missing.
#
# Layout, and the variables that consume it (printed at the end):
#
#   DIR/charts/<chart>-<version>.tgz          NKECTL_CWE_{POSTGRES,TEMPORAL,SPIRE,SPIRE_CRDS}_CHART
#   DIR/capi/<provider>/<version>/*.yaml      NKECTL_CAPI_LOCAL_REPO=DIR/capi
#   DIR/capi/cert-manager/<version>/cert-manager.yaml
#
# DIR/capi is a clusterctl local repository: <label>/<version>/<components>.yaml
# with a metadata.yaml beside each, which is what clusterctl's file:// provider
# URLs parse. cli/internal/cwedeploy/capi.go renders the clusterctl
# configuration that points at it.
#
# The chart pins are read from cli/internal/cwedeploy/cwedeploy.go and the
# Cluster API version from make/tool-versions.mk, so a bump there is picked up
# here. cert-manager's version is pinned below: clusterctl chooses it, not the
# chart pins, and it has to match the cert-manager images that are mirrored.
#
# Requires `helm` and `curl`. Run it where those can reach the internet; the
# directory is what moves across the gap.

set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dir="${1:-./bootstrap-artifacts}"

# cert-manager version. clusterctl v1.12.x installs v1.19.3 by default; this
# is the same version, staged so clusterctl reads it from disk. Change it
# together with the quay.io/jetstack rows in hack/mirror-bootstrap-images.sh.
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.19.3}"

for tool in helm curl; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "error: $tool not found on PATH" >&2
    exit 1
  }
done

# pin NAME reads the string constant NAME from cwedeploy.go.
pin() {
  local value
  value="$(sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\"\([^\"]*\)\".*/\1/p" \
    "${repo_root}/cli/internal/cwedeploy/cwedeploy.go")"
  [[ -n "${value}" ]] || {
    echo "error: no constant $1 in cli/internal/cwedeploy/cwedeploy.go" >&2
    exit 1
  }
  printf '%s' "${value}"
}

postgres_ref="$(pin postgresChartRef)"
postgres_version="$(pin postgresChartVersion)"
temporal_repo="$(pin temporalChartRepo)"
temporal_name="$(pin temporalChartName)"
temporal_version="$(pin temporalChartVersion)"
spire_repo="$(pin spireChartRepo)"
spire_name="$(pin spireChartName)"
spire_version="$(pin spireChartVersion)"
spire_crds_name="$(pin spireCRDsChartName)"
spire_crds_version="$(pin spireCRDsChartVersion)"

capi_version="$(sed -n 's/^CAPI_VERSION[[:space:]]*?*=[[:space:]]*//p' "${repo_root}/make/tool-versions.mk")"
[[ -n "${capi_version}" ]] || {
  echo "error: no CAPI_VERSION in make/tool-versions.mk" >&2
  exit 1
}

charts="${dir}/charts"
capi="${dir}/capi"
mkdir -p "${charts}" "${capi}"

# pull_chart TARBALL PULL-ARGS... runs `helm pull` unless TARBALL is present.
pull_chart() {
  local tarball="$1"
  shift
  if [[ -f "${charts}/${tarball}" ]]; then
    echo "have  charts/${tarball}"
    return
  fi
  echo "pull  charts/${tarball}"
  helm pull "$@" --destination "${charts}"
  [[ -f "${charts}/${tarball}" ]] || {
    echo "error: helm pull did not produce ${charts}/${tarball}" >&2
    exit 1
  }
}

pull_chart "postgresql-${postgres_version}.tgz" "${postgres_ref}" --version "${postgres_version}"
pull_chart "${temporal_name}-${temporal_version}.tgz" "${temporal_name}" --repo "${temporal_repo}" --version "${temporal_version}"
pull_chart "${spire_name}-${spire_version}.tgz" "${spire_name}" --repo "${spire_repo}" --version "${spire_version}"
pull_chart "${spire_crds_name}-${spire_crds_version}.tgz" "${spire_crds_name}" --repo "${spire_repo}" --version "${spire_crds_version}"

# fetch URL DEST downloads URL to DEST unless DEST is present.
fetch() {
  local url="$1" dest="$2"
  if [[ -f "${dest}" ]]; then
    echo "have  ${dest#"${dir}/"}"
    return
  fi
  echo "fetch ${dest#"${dir}/"}"
  mkdir -p "$(dirname "${dest}")"
  curl -fsSL --retry 3 -o "${dest}.tmp" "${url}"
  mv "${dest}.tmp" "${dest}"
}

# The three providers `clusterctl init` installs, as <label>:<components file>.
# The labels are clusterctl's ManifestLabel for each: the bare name for the
# core provider, <type>-<name> for the others. One metadata.yaml is published
# per release and goes beside every components file.
capi_release="https://github.com/kubernetes-sigs/cluster-api/releases/download/${capi_version}"
for entry in \
  "cluster-api:core-components.yaml" \
  "bootstrap-kubeadm:bootstrap-components.yaml" \
  "control-plane-kubeadm:control-plane-components.yaml"; do
  label="${entry%%:*}"
  components="${entry##*:}"
  fetch "${capi_release}/${components}" "${capi}/${label}/${capi_version}/${components}"
  fetch "${capi_release}/metadata.yaml" "${capi}/${label}/${capi_version}/metadata.yaml"
done

fetch "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml" \
  "${capi}/cert-manager/${CERT_MANAGER_VERSION}/cert-manager.yaml"

abs_dir="$(cd "${dir}" && pwd)"
cat <<VARS

staged under ${abs_dir}. Point the bootstrap at it with:

export NKECTL_CWE_POSTGRES_CHART=${abs_dir}/charts/postgresql-${postgres_version}.tgz
export NKECTL_CWE_TEMPORAL_CHART=${abs_dir}/charts/${temporal_name}-${temporal_version}.tgz
export NKECTL_CWE_SPIRE_CHART=${abs_dir}/charts/${spire_name}-${spire_version}.tgz
export NKECTL_CWE_SPIRE_CRDS_CHART=${abs_dir}/charts/${spire_crds_name}-${spire_crds_version}.tgz
export NKECTL_CAPI_LOCAL_REPO=${abs_dir}/capi

Images are separate: hack/mirror-bootstrap-images.sh <registry>, then
NKECTL_BOOTSTRAP_REGISTRY_MIRROR=<registry>.
VARS
