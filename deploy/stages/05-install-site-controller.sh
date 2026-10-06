#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Install the site controller: every item of the deploy path, in list order.
#
# Usage: ./05-install-site-controller.sh [run|check]
#
# The install itself is one call to deploy/nke-deploy.sh, the executor that
# reads nke-deploy.yaml and applies its items. This stage supplies the three
# inputs: the deploy path, the release manifest, and the files Describe Your
# Site rendered into OUT_DIR. Charts named in the release manifest are pulled
# by digest and verified; the rest come from the checkout.
#
# Before the install, the staged images are checked once more: imported images
# are eligible for kubelet garbage collection. After it, the check reads the
# provider CRDs and Deployment, every Helm release, the controller's /readyz
# and its log.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"

# CAPNICO_IMAGE repoints the CAPNICo manifest at another image, repository:tag.
# Empty leaves the manifest as committed.
CAPNICO_IMAGE="${CAPNICO_IMAGE:-}"
CAPNICO_UPSTREAM_IMAGE="ghcr.io/dsx-ai-factory/cluster-api-provider-nico/controller"
CAPNICO_TMP=""

cleanup_tmp() {
  local rc=$?
  [[ -z "${CAPNICO_TMP}" ]] || rm -rf "${CAPNICO_TMP}"
  return "${rc}"
}

# values_for_item prints --values-for ITEM=FILE for a rendered values file,
# nothing when Describe Your Site did not write one.
values_for_item() {
  local item="$1" file="${OUT_DIR}/$1.yaml"
  [[ -f "${file}" ]] || return 0
  printf -- '--values-for\n%s=%s\n' "${item}" "${file}"
}

stage_run() {
  require_cmd kubectl helm python3 oras
  for f in bootstrap-config.json controller.yaml spire.yaml postgresql.yaml temporal.yaml; do
    [[ -n "${DRY_RUN}" || -f "${OUT_DIR}/${f}" ]] ||
      die "${OUT_DIR}/${f} not found. Run 03-describe-site.sh first."
  done
  [[ -n "${RELEASE_FILE}" && ( -n "${DRY_RUN}" || -f "${RELEASE_FILE}" ) ]] ||
    die "RELEASE_FILE is not set or does not exist"
  helm_want="$(mk_var HELM_VERSION)"
  helm_have="$(helm version --template '{{.Version}}' 2>/dev/null || true)"
  [[ -z "${helm_want}" || "${helm_have}" == "${helm_want}" ]] ||
    warn "helm on PATH is ${helm_have:-unknown}; the repository pins ${helm_want} (make/tool-versions.mk)"
  trap 'cleanup_tmp; on_exit' EXIT

  # doc-begin
  # Imported images are eligible for kubelet garbage collection. Confirm they
  # are still present before anything schedules.
  "${STAGES_DIR}/02-stage-images.sh" check ||
    die "staged images are missing. Rerun 02-stage-images.sh."

  # The CAPNICo manifest names its image by tag. CAPNICO_IMAGE swaps it, for a
  # mirror or a locally built provider, through a kustomization the executor
  # applies in place of the committed manifest.
  overrides=()
  if [[ -n "${CAPNICO_IMAGE}" ]]; then
    CAPNICO_TMP="$(mktemp -d "${TMPDIR:-/tmp}/nke-capnico.XXXXXX")"
    cp "${DEPLOY_PATH}/capnico/capnico.yaml" "${CAPNICO_TMP}/capnico.yaml"
    printf 'resources: [capnico.yaml]\nimages:\n  - name: %s\n    newName: %s\n    newTag: %s\n' \
      "${CAPNICO_UPSTREAM_IMAGE}" "${CAPNICO_IMAGE%:*}" "${CAPNICO_IMAGE##*:}" >"${CAPNICO_TMP}/kustomization.yaml"
    overrides=(--manifest-override "capnico=${CAPNICO_TMP}")
  fi

  # The deploy path says what to install and in which order. The release
  # manifest says where the published charts come from, by digest. The values
  # files Describe Your Site rendered say what is specific to this site; each
  # is applied after the item's own values.
  values=()
  while IFS= read -r line; do values+=("${line}"); done < <(
    for item in controller spire postgresql temporal nktp; do values_for_item "${item}"; done)
  # Not through run: under DRY_RUN the executor prints its own argv, so the
  # dry run shows every kubectl and helm call the install makes.
  bash "${DEPLOY_DIR}/nke-deploy.sh" install \
    --deploy-path "${DEPLOY_PATH}" \
    --release-manifest "${RELEASE_FILE}" \
    --chart-cache "${OUT_DIR}/charts" \
    --config "${OUT_DIR}/bootstrap-config.json" \
    --kubeconfig "${SITE_KUBECONFIG}" \
    --timeout "${PHASE_TIMEOUT}" \
    ${values[@]+"${values[@]}"} \
    ${overrides[@]+"${overrides[@]}"} \
    ${DRY_RUN:+--dry-run}

  # helm --wait covers the chart items. The CAPNICo manifest is applied, not
  # waited for, so wait for its controller here: the check needs it Available,
  # and the controller needs its CRDs served.
  kc -n capnico-system rollout status deployment/controller-manager --timeout="${REQUIRE_TIMEOUT}"
  # doc-end
}

stage_check() {
  if [[ -n "${DRY_RUN}" ]]; then
    check_skip "provider and plane checks need a cluster (DRY_RUN)"
    return 0
  fi
  local ns="${CONTROLLER_NAMESPACE}"

  log "provider"
  for crd in nicoclusters.infrastructure.cluster.x-k8s.io nicoclustertemplates.infrastructure.cluster.x-k8s.io nicomachines.infrastructure.cluster.x-k8s.io nicomachinetemplates.infrastructure.cluster.x-k8s.io; do
    if [[ "$(kc get crd "${crd}" -o jsonpath='{.status.conditions[?(@.type=="Established")].status}' 2>/dev/null)" == "True" ]]; then
      check_pass "CRD ${crd} Established"
    else
      check_fail "CRD ${crd} not Established"
    fi
  done
  if [[ "$(kc -n capnico-system get deployment controller-manager -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null)" == "True" ]]; then
    check_pass "Deployment capnico-system/controller-manager Available"
  else
    check_fail "Deployment capnico-system/controller-manager not Available"
  fi
  for secret in capnico-system/nico-credentials capnico-system/ghcr-imagepull "${ns}/dgxc-tenant-credentials"; do
    if kc -n "${secret%/*}" get secret "${secret#*/}" >/dev/null 2>&1; then
      check_pass "Secret ${secret} present"
    else
      check_fail "Secret ${secret} missing"
    fi
  done

  log "plane"
  # Every release must be deployed. A failed release recovers by rerunning this
  # stage. A pending release needs `helm uninstall` first.
  releases="$(hl list -A -o json 2>/dev/null |
    python3 -c 'import json, sys; [print(r["name"], r["namespace"], r["status"], r["chart"]) for r in json.load(sys.stdin)]' 2>/dev/null || true)"
  while read -r name rns status _; do
    [[ -n "${name}" ]] || continue
    case "${status}" in
    deployed) check_pass "helm release ${rns}/${name} deployed" ;;
    failed) check_fail "helm release ${rns}/${name} is failed: rerun this stage" ;;
    pending-*) check_fail "helm release ${rns}/${name} is ${status}: helm uninstall ${name} -n ${rns}, then rerun this stage" ;;
    *) check_fail "helm release ${rns}/${name} is ${status}" ;;
    esac
  done <<<"${releases}"
  # Every helm item of the deploy path is a release, named and placed by its
  # release.yaml.
  while IFS=$'\t' read -r item type; do
    [[ "${type}" == helm ]] || continue
    IFS=$'\x1f' read -r _ chart version _ _ rel rns _ _ \
      < <(python3 "${LIB_DIR}/nke-deploy-items.py" release "${DEPLOY_PATH}/${item}/release.yaml")
    if ! printf '%s\n' "${releases}" | awk -v n="${rel}" -v s="${rns}" '$1 == n && $2 == s { found = 1 } END { exit !found }'; then
      check_fail "helm release ${rns}/${rel} missing: rerun this stage"
      continue
    fi
    # A chart the release manifest pins runs the version the manifest names.
    release_items chart | awk -F'\t' -v n="${item}" '$1 == n { found = 1 } END { exit !found }' || continue
    have="$(printf '%s\n' "${releases}" | awk -v n="${rel}" -v s="${rns}" '$1 == n && $2 == s { print $4; exit }')"
    if [[ "${have}" == "${chart}-${version}" ]]; then
      check_pass "helm release ${rns}/${rel} runs chart ${have}"
    else
      check_fail "helm release ${rns}/${rel} runs chart ${have:-none}; the deploy path pins ${chart}-${version}"
    fi
  done < <(python3 "${LIB_DIR}/nke-deploy-items.py" items "${DEPLOY_PATH}/nke-deploy.yaml")

  if [[ "$(kc -n "${ns}" get deployment nke-controller -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null)" == "True" ]]; then
    check_pass "Deployment ${ns}/nke-controller Available"
  else
    check_fail "Deployment ${ns}/nke-controller not Available"
  fi
  # /readyz reports the database and Temporal checks. The image ships curl.
  body="$(kc -n "${ns}" exec deploy/nke-controller -- curl -sS -m 5 http://127.0.0.1:8080/readyz 2>/dev/null || true)"
  if printf '%s' "${body}" | python3 -c 'import json, sys; r = json.load(sys.stdin); c = r.get("checks", {}); sys.exit(0 if r.get("status") == "ok" and c.get("database") == "ok" and c.get("temporal") == "ok" else 1)' 2>/dev/null; then
    check_pass "/readyz: database ok, temporal ok"
  else
    check_fail "/readyz: ${body:-no response}"
  fi
  # The worker reads the NicoMachine CRD once at start. If it started before
  # CAPNICo, it skipped the NICo controllers and must be restarted.
  if kc -n "${ns}" logs deploy/nke-controller --all-containers --tail=-1 2>/dev/null | grep -qF 'NicoMachine CRD not installed'; then
    check_fail "nke-controller logged 'NicoMachine CRD not installed': kubectl -n ${ns} rollout restart deployment/nke-controller"
  else
    check_pass "nke-controller log has no 'NicoMachine CRD not installed'"
  fi
}

stage_main "$@"
