#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Secure and register the site: patch the Cluster API controllers with the
# SPIRE exec-auth plugin, then point nkectl at the site.
#
# Usage: ./06-secure-and-register.sh [run|check]
#
# The guide docs/site-bootstrap/install-site-controller.md describes this
# stage. The commands between `# doc-begin` and `# doc-end` are the stage's
# work; keep them readable on their own: lib variables, no functions.
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
#
# API reachability. The controller chart renders a NetworkPolicy that selects
# the controller pod and admits the API port only from pods in the cluster.
# Every other ingress is denied, the NodePort included, so a client off the
# nodes cannot reach the API. API_CLIENT_CIDRS lists the client networks,
# space separated. When set, the run step applies a second NetworkPolicy
# that admits them. The check requests /api/version from this host and fails
# when it does not answer 200.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"

HACK_DIR="${REPO_ROOT}/controller/hack"

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
# then SITE_CONFIG. The run step registers the site with them. The check
# builds the API URL from SITE_VIP.
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

# Client networks the API admits, space separated CIDRs. Empty applies
# nothing. Environment only: describe_site.py does not write it to site.env.
API_CLIENT_CIDRS="${API_CLIENT_CIDRS:-}"
# The controller namespace comes from the deploy path (lib.sh). The pod
# listens on 8080, the service.port of the deploy path's controller overlay.
# The NodePort Service forwards 30080 to it, and a NetworkPolicy matches the
# pod port.
API_NAMESPACE="${CONTROLLER_NAMESPACE}"
API_POD_PORT=8080
API_CLIENTS_POLICY="nke-controller-api-clients"
API_URL_PATH="/api/version"

# api_clients_policy prints a NetworkPolicy that admits API_CLIENT_CIDRS to
# the controller pod on API_POD_PORT. Policies are additive: this one adds
# to the chart's, which admits in-cluster pods.
api_clients_policy() {
  local cidr
  cat <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ${API_CLIENTS_POLICY}
  namespace: ${API_NAMESPACE}
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: nke-controller
  policyTypes:
    - Ingress
  ingress:
    - from:
EOF
  for cidr in ${API_CLIENT_CIDRS}; do
    printf '        - ipBlock:\n            cidr: %s\n' "${cidr}"
  done
  cat <<EOF
      ports:
        - port: ${API_POD_PORT}
          protocol: TCP
EOF
}

# apply_api_clients_policy writes the policy to OUT_DIR and applies it with
# server-side apply, so a rerun with other CIDRs replaces the list.
apply_api_clients_policy() {
  local file="${OUT_DIR}/api-clients-networkpolicy.yaml" cidr
  for cidr in ${API_CLIENT_CIDRS}; do
    [[ "${cidr}" =~ ^[0-9a-fA-F.:]+/[0-9]+$ ]] ||
      die "API_CLIENT_CIDRS: ${cidr} is not a CIDR. Use the form 10.0.0.0/8, space separated."
  done
  api_clients_policy >"${file}"
  kc apply --server-side --field-manager site-install -f "${file}"
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

  if [[ -n "${API_CLIENT_CIDRS}" ]]; then
    log "admitting API clients from: ${API_CLIENT_CIDRS}"
    apply_api_clients_policy
  else
    log "API_CLIENT_CIDRS is unset; the API stays reachable from in-cluster pods only"
  fi
}

stage_check() {
  require_cmd kubectl curl
  require_nkectl
  resolve_site

  local entry ns name volume
  if [[ -n "${DRY_RUN}" ]]; then
    check_skip "exec-auth volumes and rollouts need a cluster (DRY_RUN)"
    EXEC_AUTH_DEPLOYMENTS=""
  fi
  for entry in ${EXEC_AUTH_DEPLOYMENTS}; do
    ns="${entry%%/*}"
    name="${entry#*/}"
    if ! kc -n "${ns}" get "deployment/${name}" >/dev/null 2>&1; then
      check_skip "${ns}/${name} not installed"
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

  # nkectl get providers reads the kubeconfig, not the API. This request
  # proves the API answers from this host, which the chart's NetworkPolicy
  # otherwise denies. /api/version needs no token.
  local api_url="http://${SITE_VIP}:30080${API_URL_PATH}" code
  if [[ -n "${DRY_RUN}" ]]; then
    check_skip "GET ${api_url} needs the site (DRY_RUN)"
  else
    code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "${api_url}" 2>/dev/null || true)"
    # curl prints 000 when nothing answers.
    [[ "${code}" != "000" ]] || code=""
    if [[ "${code}" == "200" ]]; then
      check_pass "GET ${api_url}: 200"
    else
      check_fail "GET ${api_url} answered ${code:-nothing} within 10s from this host. The chart's NetworkPolicy admits the API only from pods in the cluster, so a client off the nodes needs a NetworkPolicy that admits its CIDR. Export API_CLIENT_CIDRS=\"<cidr> ...\" (this host's network) and rerun ./06-secure-and-register.sh run. See docs/site-bootstrap/install-site-controller.md, section 6, Choices."
    fi
  fi
}

stage_main "$@"
