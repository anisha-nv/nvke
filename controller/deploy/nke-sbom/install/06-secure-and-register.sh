#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Secure and register the site: patch the Cluster API controllers with the
# SPIRE exec-auth plugin, then point nkectl at the site.
#
# Usage: ./06-secure-and-register.sh [run|check]
#
# The guide docs/site-bootstrap/install-site-controller.md is the authority.
# Commands between `# doc-begin` and `# doc-end` are extracted verbatim into
# the guide. Keep them copy-pasteable: use the lib variables, no functions.
#
# The patch lives in Deployments that `clusterctl init` owns. `clusterctl
# upgrade` rewrites those Deployments and drops the patch. Rerun this stage
# after every `clusterctl upgrade`.
#
# API exposure. The default install leaves API authentication off. The API
# is plain HTTP on NodePort 30080, reachable on every node IP. That is what
# `nkectl config set-site --url` below points at. To require a token on the
# API, follow controller/docs/configuring-api-authentication.md. The dev
# scaffolding in controller/deploy/k8s/nke-auth-dev.yaml is not part of this
# install.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

HACK_DIR="$(cd "${SBOM_DIR}/.." && pwd)/hack"

# The site name and VIP come from stage 3: ${OUT_DIR}/site.env when it has
# been run, else the site config it reads. Both can be set in the environment
# instead.
# site_value prints one scalar under the top-level `site:` key of SITE_CONFIG.
site_value() {
  [[ -f "${SITE_CONFIG}" ]] || return 0
  awk -v key="$1" '
    /^[^[:space:]#]/ { in_site = ($0 ~ /^site:/); next }
    in_site && $1 == key ":" {
      sub(/^[[:space:]]*[^:]+:[[:space:]]*/, "")
      sub(/[[:space:]]+#.*$/, "")
      gsub(/^["\047]|["\047]$/, "")
      print
      exit
    }
  ' "${SITE_CONFIG}"
}

# env_value prints one KEY from ${OUT_DIR}/site.env, sourced in a subshell.
env_value() {
  [[ -f "${OUT_DIR}/site.env" ]] || return 0
  # shellcheck disable=SC1091
  (. "${OUT_DIR}/site.env" && printf '%s' "${!1:-}")
}

# resolve_site fills SITE_NAME and SITE_VIP: the environment, then site.env,
# then SITE_CONFIG. Only the run step needs them; check works from the cluster.
resolve_site() {
  SITE_NAME="${SITE_NAME:-$(env_value SITE_NAME)}"
  SITE_VIP="${SITE_VIP:-$(env_value SITE_VIP)}"
  SITE_NAME="${SITE_NAME:-$(site_value name)}"
  SITE_VIP="${SITE_VIP:-$(site_value vip)}"
  local v
  for v in SITE_NAME SITE_VIP; do
    [[ -z "${!v}" ]] || continue
    if [[ -n "${DRY_RUN}" ]]; then
      warn "${v} is unset and ${SITE_CONFIG} has no value for it; using a placeholder"
      printf -v "${v}" '<%s>' "${v}"
    else
      die "${v} is unset and ${SITE_CONFIG} has no value for it. Set site.name and site.vip, or export ${v}."
    fi
  done
  export SITE_NAME SITE_VIP
}

# require_nkectl dies when nkectl is missing, with a pointer to the install
# guide. In dry-run mode it only warns, so the argv can still be printed.
require_nkectl() {
  command -v nkectl >/dev/null 2>&1 && return 0
  local msg="required tool not found: nkectl. Install it: docs/site-bootstrap/installation.md"
  if [[ -n "${DRY_RUN}" ]]; then
    warn "${msg}"
    return 0
  fi
  die "${msg}"
}

# The Deployments the exec-auth overlay patches. The script skips the ones
# that are not installed, and so does the check.
EXEC_AUTH_DEPLOYMENTS="capi-system/capi-controller-manager
capi-kubeadm-control-plane-system/capi-kubeadm-control-plane-controller-manager
capd-system/capd-controller-manager"

stage_run() {
  require_cmd kubectl
  require_nkectl
  resolve_site

  log "patching the Cluster API controllers with the SPIRE exec-auth plugin"
  # doc-begin
  run "${HACK_DIR}/apply-capi-spiffe-exec-auth.sh" --kubeconfig "${SITE_KUBECONFIG}"
  # doc-end

  log "registering the site with nkectl"
  # doc-begin
  run nkectl config set-site \
    --name "${SITE_NAME}" \
    --url "http://${SITE_VIP}:30080" \
    --kubeconfig "${SITE_KUBECONFIG}"
  # doc-end
}

stage_check() {
  require_cmd kubectl
  require_nkectl

  local entry ns name volume
  if [[ -n "${DRY_RUN}" ]]; then
    printf 'SKIP  exec-auth volumes and rollouts need a cluster (DRY_RUN)\n'
    EXEC_AUTH_DEPLOYMENTS=""
  fi
  for entry in ${EXEC_AUTH_DEPLOYMENTS}; do
    ns="${entry%%/*}"
    name="${entry#*/}"
    if ! kc -n "${ns}" get "deployment/${name}" >/dev/null 2>&1; then
      printf 'SKIP  %s/%s not installed\n' "${ns}" "${name}"
      continue
    fi
    # The overlay adds a volume whose CSI driver is csi.spiffe.io. Its
    # presence is the patch; a complete rollout is the patch running.
    volume="$(kc -n "${ns}" get "deployment/${name}" \
      -o jsonpath='{.spec.template.spec.volumes[?(@.csi.driver=="csi.spiffe.io")].name}')"
    if [[ -n "${volume}" ]]; then
      check_pass "${ns}/${name} mounts csi.spiffe.io"
    else
      check_fail "${ns}/${name} has no csi.spiffe.io volume; rerun this stage"
    fi
    if kc -n "${ns}" rollout status "deployment/${name}" --timeout="${REQUIRE_TIMEOUT}" >/dev/null; then
      check_pass "${ns}/${name} rollout complete"
    else
      check_fail "${ns}/${name} rollout not complete"
    fi
  done

  # doc-begin
  run nkectl get providers
  # doc-end
  if [[ -n "${DRY_RUN}" ]]; then
    check_pass "nkectl get providers: not evaluated in dry run"
  elif nkectl get providers | grep -Eq '^nico[[:space:]]+CONFIGURED'; then
    check_pass "nkectl get providers: nico CONFIGURED"
  else
    check_fail "nkectl get providers: nico is not CONFIGURED"
  fi
  printf 'NOTE  CONFIGURED means the provider CRDs are established, its manager is available\n'
  printf '      and its credentials Secret exists. It does not mean the credentials work.\n'
}

stage_main "$@"
