#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Render site-config.yaml into the BootstrapConfig JSON and the values files
# stage 5 installs from: four required, nktp.yaml when the config has an nktp
# block. Write the two inventory files: images.txt, every external image the
# install pulls, and version-bom.cdx.json, one component per release spec
# item. No cluster access. `helm template` needs the chart sources, so
# images.txt needs the network unless IMAGES_TXT_SKIP=1.
#
# Usage: ./03-describe-site.sh [run|check]
#
# The guide docs/site-bootstrap/install-site-controller.md is the authority.
# Commands between `# doc-begin` and `# doc-end` are extracted verbatim into
# the guide. Keep them copy-pasteable: use the lib variables, no functions.
set -euo pipefail
# shellcheck disable=SC2034 # read by lib.sh
STAGE_NEEDS_KUBECONFIG=0
# The release spec is optional here. With one, images.controller defaults to
# the controller item's ref and the BOM is written. Without one,
# images.controller must be set and no BOM is written.
if [[ -z "${RELEASE_FILE:-}" && -z "${RELEASE_REF:-}" ]]; then
  # shellcheck disable=SC2034 # read by lib.sh
  STAGE_NEEDS_RELEASE=0
fi
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

IMAGES_TXT_SKIP="${IMAGES_TXT_SKIP:-}"
RELEASE_CONTROLLER_REF="${RELEASE_CONTROLLER_REF:-}"
export IMAGES_TXT_SKIP

stage_run() {
  require_cmd python3
  [[ -f "${SITE_CONFIG}" ]] ||
    die "SITE_CONFIG=${SITE_CONFIG} does not exist. Copy site-config.example.yaml and fill it in."
  # The renderer does not parse the spec. The controller ref reaches it as an
  # environment variable; the BOM items come from release_spec.py, which it
  # calls by the CLI contract.
  if [[ -n "${RELEASE_FILE}" && -z "${RELEASE_CONTROLLER_REF}" ]]; then
    RELEASE_CONTROLLER_REF="$(release_ref controller 2>/dev/null || true)"
  fi
  export RELEASE_CONTROLLER_REF
  # doc-begin
  python3 "${INSTALL_DIR}/describe_site.py" render \
    --config "${SITE_CONFIG}" --out "${OUT_DIR}" --sbom-dir "${SBOM_DIR}" \
    --release-file "${RELEASE_FILE}"
  # doc-end
}

stage_check() {
  require_cmd python3
  if python3 "${INSTALL_DIR}/describe_site.py" check --out "${OUT_DIR}"; then
    check_pass "rendered files in ${OUT_DIR} are complete"
  else
    check_fail "rendered files in ${OUT_DIR} are incomplete"
    return 0
  fi

  # The password only matters once Postgres holds one. Compare with the live
  # Secret when a kubeconfig is set and the cluster answers; otherwise skip.
  if [[ -z "${SITE_KUBECONFIG}" ]]; then
    check_skip "live database password: SITE_KUBECONFIG is not set"
    return 0
  fi
  if ! kubectl --kubeconfig "${SITE_KUBECONFIG}" version >/dev/null 2>&1; then
    check_skip "live database password: cluster is not reachable"
    return 0
  fi
  if ! kubectl --kubeconfig "${SITE_KUBECONFIG}" -n postgres get secret postgres-postgresql >/dev/null 2>&1; then
    check_skip "live database password: secret postgres/postgres-postgresql does not exist yet"
    return 0
  fi
  local live
  live="$(kubectl --kubeconfig "${SITE_KUBECONFIG}" -n postgres get secret postgres-postgresql \
    -o jsonpath='{.data.postgres-password}' | base64 -d)"
  if [[ -z "${live}" ]]; then
    live="$(kubectl --kubeconfig "${SITE_KUBECONFIG}" -n postgres get secret postgres-postgresql \
      -o jsonpath='{.data.password}' | base64 -d)"
  fi
  if [[ "${live}" == "$(cat "${OUT_DIR}/db-password")" ]]; then
    check_pass "rendered database password matches the live Secret"
  else
    check_fail "rendered database password differs from postgres/postgres-postgresql; the PVC keeps the old one"
  fi
}

stage_main "$@"
