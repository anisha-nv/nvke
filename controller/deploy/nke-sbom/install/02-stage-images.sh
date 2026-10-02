#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Import the controller, profiles and CAPNICo images into the k8s.io
# containerd namespace of every site node, so the install pulls nothing.
#
# Usage: ./02-stage-images.sh [run|check]
#
# The guide docs/site-bootstrap/install-site-controller.md is the authority.
# Commands between `# doc-begin` and `# doc-end` are extracted verbatim into
# the guide. Keep them copy-pasteable: use the lib variables, no functions.
#
# Images. The controller and profiles images come from `IMAGES` (space
# separated), else SITE_IMAGE_CONTROLLER and SITE_IMAGE_PROFILES in
# ${OUT_DIR}/site.env when stage 3 has run, else images.controller and
# images.profiles in SITE_CONFIG. CAPNICO_IMAGE defaults to the image: line
# of nke-sbom/capnico/capnico.yaml and is pulled for the node architecture
# first, so the node needs no registry credential. EXTRA_IMAGES are staged
# as well. Every image is exported from the local nerdctl or docker, whichever
# holds it (IMAGE_CLI overrides), one tarball per image under
# ${OUT_DIR}/images, and imported with `ctr -n k8s.io images import`.
#
# Nodes. The node list is `kubectl get nodes`. NODE_EXEC says how to reach a
# node: a command prefix with {node} in it, run in front of ctr.
#
#   NODE_EXEC unset                   this script runs on the node; ctr runs here
#   NODE_EXEC='ssh root@{node}'       real hosts, by node name
#   NODE_EXEC='nerdctl exec -i {node}' container nodes
#
# NODE_MAP renames nodes for NODE_EXEC when the node name is not the address:
# space separated `node-name=target` pairs. NODE_ARCH defaults to the first
# node's kubernetes.io/arch label.
#
# A node that already holds an image at the same digest is skipped, so a
# rerun changes nothing. The check lists every image on every node with its
# digest and fails on a missing or stale one.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

IMAGES="${IMAGES:-}"
EXTRA_IMAGES="${EXTRA_IMAGES:-}"
CAPNICO_IMAGE="${CAPNICO_IMAGE:-$(awk '/^ *image: / { print $2; exit }' "${SBOM_DIR}/nke-sbom/capnico/capnico.yaml")}"
[[ -n "${CAPNICO_IMAGE}" ]] || die "no image: line in ${SBOM_DIR}/nke-sbom/capnico/capnico.yaml; set CAPNICO_IMAGE"
IMAGE_CLI="${IMAGE_CLI:-}"
NODE_EXEC="${NODE_EXEC:-}"
NODE_MAP="${NODE_MAP:-}"
NODE_ARCH="${NODE_ARCH:-}"
NODES=""
export CAPNICO_IMAGE NODE_EXEC NODE_ARCH

check_skip() { printf 'SKIP  %s\n' "$*"; }

# env_value prints one KEY from ${OUT_DIR}/site.env, sourced in a subshell.
env_value() {
  [[ -f "${OUT_DIR}/site.env" ]] || return 0
  # shellcheck disable=SC1091
  (. "${OUT_DIR}/site.env" && printf '%s' "${!1:-}")
}

# config_value prints one scalar under a top-level section of SITE_CONFIG.
config_value() {
  [[ -f "${SITE_CONFIG}" ]] || return 0
  awk -v section="$1" -v key="$2" '
    /^[^[:space:]#]/ { in_section = ($0 ~ "^" section ":"); next }
    in_section && $1 == key ":" {
      sub(/^[[:space:]]*[^:]+:[[:space:]]*/, "")
      sub(/[[:space:]]+#.*$/, "")
      gsub(/^["\047]|["\047]$/, "")
      print
      exit
    }
  ' "${SITE_CONFIG}"
}

# resolve_images fills IMAGES: the environment, then site.env, then SITE_CONFIG.
resolve_images() {
  [[ -n "${IMAGES// /}" ]] ||
    IMAGES="$(env_value SITE_IMAGE_CONTROLLER) $(env_value SITE_IMAGE_PROFILES)"
  [[ -n "${IMAGES// /}" ]] ||
    IMAGES="$(config_value images controller) $(config_value images profiles)"
  if [[ -z "${IMAGES// /}" ]]; then
    [[ -n "${DRY_RUN}" ]] ||
      die "no images to stage. Set IMAGES, or images.controller and images.profiles in ${SITE_CONFIG}"
    IMAGES='<controller-image> <profiles-image>'
  fi
  IMAGES="$(printf '%s' "${IMAGES} ${EXTRA_IMAGES}" | xargs)"
  export IMAGES
}

# resolve_nodes fills NODES from the cluster and NODE_ARCH from its first node.
resolve_nodes() {
  if [[ -n "${DRY_RUN}" ]]; then
    NODES='<node>'
    [[ -n "${NODE_ARCH}" ]] || NODE_ARCH="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
    return 0
  fi
  NODES="$(kc get nodes -o jsonpath='{range .items[*]}{.metadata.name}{" "}{end}')"
  [[ -n "${NODES// /}" ]] || die "no nodes found with ${SITE_KUBECONFIG}"
  [[ -n "${NODE_ARCH}" ]] ||
    NODE_ARCH="$(kc get nodes -o jsonpath='{.items[0].metadata.labels.kubernetes\.io/arch}')"
  [[ -n "${NODE_ARCH}" ]] || die "cannot read kubernetes.io/arch from the first node; set NODE_ARCH"
}

# resolve_image_cli picks nerdctl or docker: the first that holds the first
# image, else the first on PATH.
resolve_image_cli() {
  local cli
  [[ -z "${IMAGE_CLI}" ]] || return 0
  for cli in nerdctl docker; do
    if command -v "${cli}" >/dev/null 2>&1 && "${cli}" image inspect "${IMAGES%% *}" >/dev/null 2>&1; then
      IMAGE_CLI="${cli}"
      return 0
    fi
  done
  for cli in nerdctl docker; do
    if command -v "${cli}" >/dev/null 2>&1; then
      IMAGE_CLI="${cli}"
      return 0
    fi
  done
  die "neither nerdctl nor docker found on PATH; set IMAGE_CLI"
}

# node_exec prints the NODE_EXEC prefix for a node, NODE_MAP applied.
node_exec() {
  local node="$1" target="$1" pair
  for pair in ${NODE_MAP}; do
    [[ "${pair%%=*}" != "${node}" ]] || target="${pair#*=}"
  done
  printf '%s' "${NODE_EXEC//\{node\}/${target}}"
}

# node_sh runs a command on a node through the NODE_EXEC prefix.
node_sh() {
  local node="$1" prefix
  shift
  prefix="$(node_exec "${node}")"
  # shellcheck disable=SC2086
  ${prefix} "$@"
}

# full_ref prints the reference as containerd stores it: docker.io and
# library are filled in for short names.
full_ref() {
  local ref="$1" first="${1%%/*}"
  if [[ "${ref}" != */* ]]; then
    printf 'docker.io/library/%s' "${ref}"
  elif [[ "${first}" != *.* && "${first}" != *:* && "${first}" != localhost ]]; then
    printf 'docker.io/%s' "${ref}"
  else
    printf '%s' "${ref}"
  fi
}

# local_digest prints the manifest digest of an image in IMAGE_CLI, or nothing.
local_digest() {
  local digest
  digest="$("${IMAGE_CLI}" image inspect --format '{{index .RepoDigests 0}}' "$1" 2>/dev/null || true)"
  [[ "${digest}" == *@sha256:* ]] || return 0
  printf '%s' "${digest##*@}"
}

# node_digest prints the digest of an image in the node's k8s.io namespace, or nothing.
node_digest() {
  node_sh "$1" ctr -n k8s.io images ls "name==$(full_ref "$2")" 2>/dev/null | awk 'NR > 1 { print $3; exit }'
}

stage_run() {
  local image tar digest node missing exec_prefix
  resolve_images
  resolve_nodes
  resolve_image_cli
  for image in ${IMAGES}; do
    [[ -n "${DRY_RUN}" ]] || "${IMAGE_CLI}" image inspect "${image}" >/dev/null 2>&1 ||
      die "${image} not found in ${IMAGE_CLI}. Build it first."
  done
  log "images: ${IMAGES} ${CAPNICO_IMAGE} from ${IMAGE_CLI}"
  log "nodes: ${NODES}(${NODE_ARCH}) via ${NODE_EXEC:-local ctr}"

  # doc-begin
  # The provider image is public. Pull it for the node architecture here, so
  # the nodes need no registry credential.
  if ! "${IMAGE_CLI}" image inspect "${CAPNICO_IMAGE}" >/dev/null 2>&1; then
    run "${IMAGE_CLI}" pull --platform "linux/${NODE_ARCH}" "${CAPNICO_IMAGE}"
  fi

  # One tarball per image, imported into every node that lacks the digest.
  # Namespace k8s.io: an image in any other namespace is invisible to the kubelet.
  mkdir -p "${OUT_DIR}/images"
  for image in ${IMAGES} ${CAPNICO_IMAGE}; do
    digest="$(local_digest "${image}")"
    missing=""
    for node in ${NODES}; do
      if [[ -z "${DRY_RUN}" && -n "${digest}" && "$(node_digest "${node}" "${image}")" == "${digest}" ]]; then
        continue
      fi
      missing="${missing} ${node}"
    done
    if [[ -z "${missing}" ]]; then
      log "${image} ${digest} is on every node"
      continue
    fi
    tar="${OUT_DIR}/images/$(printf '%s' "${image}" | tr '/:@' '___').tar"
    run "${IMAGE_CLI}" save -o "${tar}" "${image}"
    for node in ${missing}; do
      log "${image} -> ${node}"
      exec_prefix="$(node_exec "${node}")"
      run_from "${tar}" ${exec_prefix} ctr -n k8s.io images import -
    done
    rm -f "${tar}"
  done
  # doc-end
}

stage_check() {
  local image node digest want
  if [[ -n "${DRY_RUN}" ]]; then
    check_skip "image presence needs the nodes (DRY_RUN)"
    return 0
  fi
  resolve_images
  resolve_nodes
  resolve_image_cli
  for node in ${NODES}; do
    for image in ${IMAGES} ${CAPNICO_IMAGE}; do
      digest="$(node_digest "${node}" "${image}")"
      want="$(local_digest "${image}")"
      if [[ -z "${digest}" ]]; then
        check_fail "${node}: ${image} missing from containerd namespace k8s.io"
      elif [[ -n "${want}" && "${digest}" != "${want}" ]]; then
        check_fail "${node}: ${image} ${digest} differs from the local ${want}; rerun this stage"
      else
        check_pass "${node}: ${image} ${digest}"
      fi
    done
  done
}

stage_main "$@"
