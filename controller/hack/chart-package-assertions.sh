#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Assert that the documented packaging command replaces both placeholder chart
# versions. CHART and CHART_PACKAGE_VERSION may override the defaults.
# Set CHART_ARCHIVE and an explicit CHART_PACKAGE_VERSION to check an existing
# package without invoking Helm or changing its bytes. CHART_PACKAGE_APP_VERSION
# defaults to CHART_PACKAGE_VERSION. This is a metadata check, not deployment acceptance.

set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
chart="${CHART:-${repo_root}/controller/deploy/charts/controller}"
version="${CHART_PACKAGE_VERSION:-0.0.0-$(git -C "${repo_root}" rev-parse --short=12 HEAD)}"
app_version="${CHART_PACKAGE_APP_VERSION:-${version}}"
if [[ ${CHART_ARCHIVE+x} ]]; then
  [[ -n "${CHART_ARCHIVE}" && -f "${CHART_ARCHIVE}" && -r "${CHART_ARCHIVE}" ]] || {
    echo "ERROR: CHART_ARCHIVE must name a readable packaged chart" >&2; exit 1;
  }
  [[ -n "${CHART_PACKAGE_VERSION:-}" ]] || {
    echo "ERROR: CHART_PACKAGE_VERSION is required with CHART_ARCHIVE" >&2; exit 1;
  }
  [[ ! ${CHART+x} ]] || { echo "ERROR: supply CHART_ARCHIVE or CHART, not both" >&2; exit 1; }
  archive="${CHART_ARCHIVE}"
else
  dist="$(mktemp -d)"
  trap 'rm -rf "${dist}"' EXIT
  trap 'exit 130' INT
  trap 'exit 143' HUP TERM

  helm package "${chart}" \
    --version "${version}" \
    --app-version "${app_version}" \
    --destination "${dist}" >/dev/null

  archive=""
  for candidate in "${dist}"/*.tgz; do
    [[ -e "${candidate}" ]] || continue
    if [[ -n "${archive}" ]]; then
      echo "ERROR: helm package produced more than one archive" >&2
      exit 1
    fi
    archive="${candidate}"
  done
  [[ -n "${archive}" ]] || { echo "ERROR: helm package produced no archive" >&2; exit 1; }
fi

# Consume the complete tar listing. Exiting awk at the first match closes the
# pipe early, and GNU tar treats the resulting SIGPIPE as a write error under
# pipefail even though BSD tar does not.
chart_yaml="$(tar -tzf "${archive}" | awk -F/ 'NF == 2 && $1 != "" && $2 == "Chart.yaml" { chart = $0; count++ } END { if (count == 1) print chart; else exit 1 }')" || {
  echo "ERROR: packaged chart must contain exactly one root Chart.yaml" >&2; exit 1;
}

metadata="$(tar -xOzf "${archive}" "${chart_yaml}")"
packaged_version="$(awk '$1 == "version:" { gsub(/"/, "", $2); print $2; exit }' <<<"${metadata}")"
packaged_app_version="$(awk '$1 == "appVersion:" { gsub(/"/, "", $2); print $2; exit }' <<<"${metadata}")"

if [[ "${packaged_version}" != "${version}" ]]; then
  echo "ERROR: packaged chart version is ${packaged_version:-missing}; expected ${version}" >&2
  exit 1
fi
if [[ "${packaged_app_version}" != "${app_version}" ]]; then
  echo "ERROR: packaged chart appVersion is ${packaged_app_version:-missing}; expected ${app_version}" >&2
  exit 1
fi

echo "packaged $(basename "${archive}") at version ${packaged_version}"
