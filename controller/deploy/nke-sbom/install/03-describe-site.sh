#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Render site-config.yaml into the BootstrapConfig JSON and the values files
# stage 5 installs from: four required, nktp.yaml when the config has an nktp
# block. No cluster access.
#
# Usage: ./03-describe-site.sh [run|check]
#
# The guide docs/site-bootstrap/install-site-controller.md is the authority.
# Commands between `# doc-begin` and `# doc-end` are extracted verbatim into
# the guide. Keep them copy-pasteable: use the lib variables, no functions.
set -euo pipefail
STAGE_NEEDS_KUBECONFIG=0
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

stage_run() {
  require_cmd python3
  [[ -f "${SITE_CONFIG}" ]] ||
    die "SITE_CONFIG=${SITE_CONFIG} does not exist. Copy site-config.example.yaml and fill it in."
  # doc-begin
  python3 "${INSTALL_DIR}/describe_site.py" render \
    --config "${SITE_CONFIG}" --out "${OUT_DIR}" --sbom-dir "${SBOM_DIR}"
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
