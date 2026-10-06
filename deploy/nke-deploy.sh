#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Install a deploy path: walk its nke-deploy.yaml and apply the items in the
# order listed. The list IS the order -- see
# docs/site-bootstrap/deploy-path.md, which this format follows.
#
# This is the executor every deploy path under deploy/ and controller/deploy/
# is written for. deploy/nico is the customer path; the e2e lanes install
# theirs with the same script. The helpers it needs are in lib/ beside it.
#
# A helm item is one chart installed as one release; the item order does the
# sequencing. There are no enable flags and no phases, so nothing this script
# emits can override what an item's values decided.
#
# Charts come from the deploy path or the item's repository, or, with
# --release-manifest, from the OCI reference the release manifest pins for the
# item, pulled by digest and verified.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ITEMS_PY="${SCRIPT_DIR}/lib/nke-deploy-items.py"
RENDER_PY="${SCRIPT_DIR}/lib/render-template.py"
SPEC_PY="${SCRIPT_DIR}/lib/release_spec.py"

DRY_RUN=""
KUBECONFIG_PATH=""
PHASE_TIMEOUT="${PHASE_TIMEOUT:-15m}"
REQUIRE_TIMEOUT="${REQUIRE_TIMEOUT:-180s}"
PASSTHROUGH=()
CONFIG=""
CHART_OVERRIDES=()
MANIFEST_OVERRIDES=()
VALUES_FOR=()
DEPLOY_PATH=""
RELEASE_MANIFEST=""
CHART_CACHE=""
# PLAIN_HTTP_REGISTRIES lists registries that serve plain HTTP, space
# separated, host:port. oras gets --plain-http for a ref on one of them.
PLAIN_HTTP_REGISTRIES="${PLAIN_HTTP_REGISTRIES:-}"

log() { printf '\n=== %s\n' "$*" >&2; }
die() { printf 'nke-deploy: %s\n' "$*" >&2; exit 1; }

usage() {
  cat >&2 <<'USAGE'
usage: nke-deploy.sh install --deploy-path DIR [options] [-- helm args]

  --deploy-path DIR   the deploy path to install: a directory holding
                      nke-deploy.yaml and one directory per item, e.g.
                      deploy/nico or controller/deploy/capd-e2e
  --release-manifest PATH
                      release-manifest.yaml: the published artifacts by OCI
                      reference and digest. A helm item named in it is pulled
                      from that reference and its chart layer digest verified,
                      instead of being fetched from the item's repository.
  --chart-cache DIR   where pulled charts are kept between runs. A chart whose
                      digest already matches is not pulled again. Default: a
                      temporary directory removed at exit.
  --chart-override ITEM=PATH
                      where to find one item's chart, for a component this
                      repository does not publish. Wins over the release
                      manifest. Repeatable.
  --manifest-override ITEM=DIR
                      apply DIR instead of a manifest item's own directory:
                      `kubectl kustomize DIR` when it holds a kustomization,
                      else its .yaml files. Repeatable.
  --config PATH       BootstrapConfig, as JSON. Required by a template item;
                      its fields are the context those templates render against.
  --values-for ITEM=PATH
                      an extra values file for one item, applied after the
                      item's own values. This is how per-run values reach a
                      single component: with one release per chart the keys are
                      no longer prefixed, so a value meant for the controller
                      cannot be broadcast to every item. Repeatable.
  --dry-run           print the argv each step would run, and exit
  --kubeconfig PATH   passed to every kubectl call
  --                  everything after is forwarded to helm verbatim
USAGE
  exit 2
}

# run executes a command, or prints it when --dry-run is set. Every helm and
# kubectl call goes through here, which is what makes --dry-run a complete
# description of the install rather than a summary of it.
run() {
  if [[ -n "${DRY_RUN}" ]]; then
    printf '%q ' "$@"
    printf '\n'
    return 0
  fi
  "$@"
}

kubectl_run() {
  if [[ -n "${KUBECONFIG_PATH}" ]]; then
    run kubectl --kubeconfig "${KUBECONFIG_PATH}" "$@"
  else
    run kubectl "$@"
  fi
}

# apply_manifest_item applies every .yaml in the item directory, lexicographically.
# The generator names them 00-, 10-, 40- precisely so that order is the intended
# one: namespaces exist before anything lands in them.
apply_manifest_item() {
  local dir="$1" file found=""
  for file in "${dir}"/*.yaml; do
    [[ -e "${file}" ]] || continue
    found=1
    kubectl_run apply --server-side -f "${file}"
  done
  [[ -n "${found}" ]] || die "manifest item ${dir} contains no .yaml files"
}

# render_template_item renders the item and applies what comes out.
#
# The output goes to a temp directory rather than over the source: the tree may
# be a read-only extraction of a published artifact, and what is rendered holds
# credentials. Removed as soon as it is applied.
render_template_item() {
  local dir="$1" item="$2" out
  [[ -n "${CONFIG}" ]] || die "item ${item} is type template, which needs --config <BootstrapConfig json>.
Its .yaml.tmpl files render against that context; see
docs/site-bootstrap/deploy-path.md for the field names."
  out="$(mktemp -d)"
  python3 "${RENDER_PY}" "${CONFIG}" "${dir}" "${out}" >/dev/null
  apply_manifest_item "${out}"
  rm -rf "${out}"
}

# apply_argocd_item creates the Application. Server-side apply of the CR rather
# than `argocd app create --upsert`: same object, no binary, no login.
apply_argocd_item() {
  local dir="$1"
  [[ -f "${dir}/application.yaml" ]] || die "argocd item ${dir} has no application.yaml"
  kubectl_run apply --server-side -f "${dir}/application.yaml"
}

# wait_for_requires blocks on the preconditions a phase declares but the chart
# does not own. helm --wait cannot do this: it waits on workloads, and a
# Certificate is not one, so it would return before the CA is issued.
wait_for_requires() {
  local phase="$1" specs="$2" spec kind name namespace condition
  for spec in ${specs}; do
    IFS=: read -r kind name namespace condition <<<"${spec}"
    if [[ -z "${DRY_RUN}" ]] && ! kubectl ${KUBECONFIG_PATH:+--kubeconfig "${KUBECONFIG_PATH}"} \
        -n "${namespace}" get "${kind}" "${name}" >/dev/null 2>&1; then
      die "phase ${phase} requires ${kind}/${name} in namespace ${namespace}, which does not exist.
It is not part of this chart -- an earlier item supplies it. In this repository
that is the nke-base-config item, from controller/deploy/k8s/oidc-upstream-ca.yaml."
    fi
    log "waiting for ${kind}/${name} condition=${condition}"
    kubectl_run -n "${namespace}" wait --for="condition=${condition}" \
      "${kind}/${name}" --timeout="${REQUIRE_TIMEOUT}"
  done
}

# manifest_override prints the --manifest-override directory for an item, if any.
manifest_override() {
  local entry
  for entry in ${MANIFEST_OVERRIDES[@]+"${MANIFEST_OVERRIDES[@]}"}; do
    if [[ "${entry%%=*}" == "$1" ]]; then
      printf '%s' "${entry#*=}"
      return
    fi
  done
}

# apply_manifest_override applies the override directory for a manifest item.
apply_manifest_override() {
  local dir="$1"
  if [[ ! -f "${dir}/kustomization.yaml" ]]; then
    apply_manifest_item "${dir}"
  elif [[ -n "${DRY_RUN}" ]]; then
    # Both halves of the pipe, since a dry run prints argv instead of piping.
    run kubectl kustomize "${dir}"
    kubectl_run apply --server-side -f -
  else
    kubectl kustomize "${dir}" | kubectl_run apply --server-side -f -
  fi
}

# sha256_file FILE prints sha256:<hex> of FILE, or nothing when FILE is absent.
sha256_file() {
  [[ -f "$1" ]] || return 0
  python3 -c 'import hashlib, sys; print("sha256:" + hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest())' "$1"
}

# oras_flags prints --plain-http for a ref on a PLAIN_HTTP_REGISTRIES host. A
# registry with a port that is not listed is probed once: /v2/ over https
# first, then http. Registries without a port are never plain HTTP.
oras_flags() {
  local host="${1%%/*}"
  case " ${PLAIN_HTTP_REGISTRIES} " in *" ${host} "*) printf -- '--plain-http'; return 0 ;; esac
  [[ "${host}" == *:* && -z "${DRY_RUN}" ]] || return 0
  if ! curl -sS -o /dev/null --max-time 5 "https://${host}/v2/" 2>/dev/null &&
     curl -sS -o /dev/null --max-time 5 "http://${host}/v2/" 2>/dev/null; then
    printf -- '--plain-http'
  fi
  return 0
}

# chart_layer REF FALLBACK prints "<digest> <file>" for the chart archive the
# Helm OCI artifact REF names. REF carries the manifest digest; the manifest is
# fetched and the chart layer read from it: the layer with the Helm chart
# content media type, else the first. Its digest is the sha256 of the .tgz; its
# org.opencontainers.image.title is the file name, FALLBACK when it has none.
# Under --dry-run nothing is fetched: the argv is printed and a placeholder
# digest returned with FALLBACK.
chart_layer() {
  local ref="$1" fallback="$2"
  if [[ -n "${DRY_RUN}" ]]; then
    # shellcheck disable=SC2046
    run oras manifest fetch $(oras_flags "${ref}") "${ref}" >&2
    printf 'sha256:dry-run %s\n' "${fallback}"
    return 0
  fi
  # shellcheck disable=SC2046
  oras manifest fetch $(oras_flags "${ref}") "${ref}" | python3 -c '
import json, sys
layers = json.load(sys.stdin).get("layers", [])
chart = [l for l in layers if l.get("mediaType") == "application/vnd.cncf.helm.chart.content.v1.tar+gzip"] or layers[:1]
if not chart:
    sys.exit("manifest has no layers")
print(chart[0]["digest"], chart[0].get("annotations", {}).get("org.opencontainers.image.title") or sys.argv[1])
' "${fallback}"
}

# release_chart ITEM CHART VERSION prints the path of the chart archive the
# release manifest names for ITEM, pulled by digest into the chart cache and
# verified, or nothing when the manifest has no chart item of that name.
release_chart() {
  local item="$1" chart_name="$2" version="$3" ref layer digest file dir have
  # Only a chart item of the same name counts; an image item is not a chart.
  ref="$(python3 "${SPEC_PY}" items "${RELEASE_MANIFEST}" --type chart 2>/dev/null |
    awk -F'\t' -v n="${item}" '$1 == n { print $3; exit }')"
  [[ -n "${ref}" ]] || return 0
  dir="${CHART_CACHE}/${item}"
  layer="$(chart_layer "${ref}" "${chart_name}-${version}.tgz")" || die "cannot read the chart layer of ${ref}"
  digest="${layer%% *}"
  file="${dir}/${layer#* }"
  if [[ "$(sha256_file "${file}")" != "${digest}" ]]; then
    # oras pull skips a layer without a title annotation, which a helm-pushed
    # chart has none of; fetch the layer blob by its digest into our own name.
    mkdir -p "${dir}"
    # shellcheck disable=SC2046
    run oras blob fetch $(oras_flags "${ref}") --output "${file}" "${ref%%@*}@${digest}" >&2
  fi
  if [[ -z "${DRY_RUN}" ]]; then
    have="$(sha256_file "${file}")"
    [[ "${have}" == "${digest}" ]] || die "chart ${item}: ${file} is ${have:-missing}; the release manifest's chart layer is ${digest}"
  fi
  printf '%s' "${file}"
}

# chart_override prints the --chart-override value for an item, if one was given.
chart_override() {
  local entry
  for entry in ${CHART_OVERRIDES[@]+"${CHART_OVERRIDES[@]}"}; do
    if [[ "${entry%%=*}" == "$1" ]]; then
      printf '%s' "${entry#*=}"
      return
    fi
  done
}

# values_for prints the --values-for paths for an item, newline separated.
values_for() {
  local entry
  for entry in ${VALUES_FOR[@]+"${VALUES_FOR[@]}"}; do
    if [[ "${entry%%=*}" == "$1" ]]; then
      printf '%s\n' "${entry#*=}"
    fi
  done
}

# build_chart_dependencies downloads the archives a path chart's Chart.lock pins
# into its charts/ directory, which a checkout does not carry. helm dependency
# build refuses to run until every repository in Chart.yaml has been added, so
# they are added to a throwaway Helm config, leaving the caller's repository
# list alone -- the same steps as `make nktp-chart-deps`. OCI dependencies need
# no repository entry. A chart without a Chart.lock has nothing to build.
build_chart_dependencies() {
  local chart="$1" helm_home url n=0
  [[ -f "${chart}/Chart.lock" ]] || return 0
  helm_home="$(mktemp -d)"
  local helm_env=(env HELM_REPOSITORY_CONFIG="${helm_home}/repositories.yaml"
    HELM_REPOSITORY_CACHE="${helm_home}/cache")
  log "building chart dependencies of ${chart}"
  while IFS= read -r url; do
    [[ "${url}" == oci://* ]] && continue
    n=$((n + 1))
    run "${helm_env[@]}" helm repo add "dependency-${n}" "${url}"
  done < <(awk '$1 == "repository:" { print $2 }' "${chart}/Chart.yaml" | sort -u)
  run "${helm_env[@]}" helm dependency build "${chart}" --skip-refresh
  rm -rf "${helm_home}"
}

# install_single installs one chart as one release. No enable flags and no phase
# loop: a release contains only its own chart, so there is nothing to turn off.
install_helm_item() {
  local dir="$1" item="$2"
  [[ -f "${dir}/release.yaml" ]] || die "helm item ${dir} has no release.yaml"

  local form chart_name version repository path release namespace values requires
  IFS=$'\x1f' read -r form chart_name version repository path release namespace values requires \
    < <(python3 "${ITEMS_PY}" release "${dir}/release.yaml")

  # An override wins, because the caller had to build or fetch the chart. Then
  # the release manifest, which pins a published chart by digest. Then a path
  # inside the deploy path, then a repository the chart is published to.
  local ref=() override released=""
  override="$(chart_override "${item}")"
  [[ -n "${override}" || -z "${RELEASE_MANIFEST}" ]] || released="$(release_chart "${item}" "${chart_name}" "${version}")"
  if [[ -n "${override}" ]]; then
    ref=("${override}")
  elif [[ -n "${released}" ]]; then
    ref=("${released}")
  elif [[ -n "${path}" ]]; then
    ref=("${DEPLOY_PATH}/${path}")
    build_chart_dependencies "${ref[0]}"
  elif [[ "${repository}" == oci://* ]]; then
    ref=("${repository%/}/${chart_name}" --version "${version}")
  elif [[ -n "${repository}" ]]; then
    # A classic index.yaml repository names the chart separately.
    ref=("${chart_name}" --repo "${repository}" --version "${version}")
  else
    die "item ${item} names no chart source.
Give release.yaml a chart.repository or chart.path, or pass
--chart-override ${item}=<path> -- which is what a chart this repository does
not publish needs."
  fi

  wait_for_requires "${item}" "${requires}"

  # The item's own values first, then whatever the run supplies for it. Helm
  # merges -f in order, so the caller's file wins on any key it names.
  local values_args=() file
  for file in ${values//,/ }; do
    values_args+=(--values "${dir}/${file}")
  done
  while IFS= read -r file; do
    [[ -n "${file}" ]] || continue
    [[ -f "${file}" ]] || die "--values-for ${item}: no such file: ${file}"
    values_args+=(--values "${file}")
  done < <(values_for "${item}")

  log "item ${item} -> release ${release} (${chart_name} ${version}) in ${namespace}"
  run helm upgrade --install "${release}" "${ref[@]}" \
    ${KUBECONFIG_PATH:+--kubeconfig "${KUBECONFIG_PATH}"} \
    --namespace "${namespace}" \
    --wait --timeout "${PHASE_TIMEOUT}" \
    ${values_args[@]+"${values_args[@]}"} \
    ${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"}
}

install() {
  [[ -n "${DEPLOY_PATH}" ]] || die "--deploy-path is required"
  if [[ -n "${RELEASE_MANIFEST}" ]]; then
    [[ -f "${RELEASE_MANIFEST}" ]] || die "--release-manifest ${RELEASE_MANIFEST}: no such file"
    python3 "${SPEC_PY}" meta "${RELEASE_MANIFEST}" >/dev/null || die "--release-manifest ${RELEASE_MANIFEST} does not parse"
    if [[ -z "${CHART_CACHE}" ]]; then
      CHART_CACHE="$(mktemp -d)"
      trap 'rm -rf "${CHART_CACHE}"' EXIT
    fi
    mkdir -p "${CHART_CACHE}"
  fi
  [[ -f "${DEPLOY_PATH}/nke-deploy.yaml" ]] \
    || die "no nke-deploy.yaml in ${DEPLOY_PATH}"

  local name type
  while IFS=$'\t' read -r name type; do
    [[ -n "${name}" ]] || continue
    local dir="${DEPLOY_PATH}/${name}"
    [[ -d "${dir}" ]] || die "item ${name} has no directory at ${dir}"
    log "item ${name} (${type})"
    local override
    override="$(manifest_override "${name}")"
    case "${type}" in
      manifest)
        if [[ -n "${override}" ]]; then
          apply_manifest_override "${override}"
        else
          apply_manifest_item "${dir}"
        fi
        ;;
      argocd)   apply_argocd_item "${dir}" ;;
      helm)     install_helm_item "${dir}" "${name}" ;;
      template) render_template_item "${dir}" "${name}" ;;
      *) die "item ${name} has unknown type ${type}" ;;
    esac
  done < <(python3 "${ITEMS_PY}" items "${DEPLOY_PATH}/nke-deploy.yaml")
}

[[ $# -gt 0 ]] || usage
case "$1" in
  install) shift ;;
  *) usage ;;
esac

while [[ $# -gt 0 ]]; do
  case "$1" in
    --deploy-path) DEPLOY_PATH="${2%/}"; shift 2 ;;
    --config) CONFIG="$2"; shift 2 ;;
    --release-manifest) RELEASE_MANIFEST="$2"; shift 2 ;;
    --chart-cache) CHART_CACHE="$2"; shift 2 ;;
    --chart-override) CHART_OVERRIDES+=("$2"); shift 2 ;;
    --manifest-override) MANIFEST_OVERRIDES+=("$2"); shift 2 ;;
    --values-for) VALUES_FOR+=("$2"); shift 2 ;;
    --kubeconfig) KUBECONFIG_PATH="$2"; shift 2 ;;
    --timeout) PHASE_TIMEOUT="$2"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    --) shift; PASSTHROUGH=("$@"); break ;;
    -h | --help) usage ;;
    *) die "unknown argument: $1" ;;
  esac
done

install
