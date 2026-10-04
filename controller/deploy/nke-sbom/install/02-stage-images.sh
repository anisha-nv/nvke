#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Pull the controller image by digest and pre-pull every external image on
# every site node, through the node's containerd, so nothing waits on a
# registry when a pod schedules.
#
# Usage: ./02-stage-images.sh [run|check]
#
# The guide docs/site-bootstrap/install-site-controller.md is the authority.
# Commands between `# doc-begin` and `# doc-end` are extracted verbatim into
# the guide. Keep them copy-pasteable: use the lib variables, no functions.
#
# Images. The controller image is the `controller` item's ref in the release
# spec (RELEASE_FILE), a digest reference. The external images are the lines
# of ${OUT_DIR}/images.txt, written by 03-describe-site.sh: the images the
# charts run with the site values, the profiles image among them, the CAPNICo
# image and the Cluster API provider images. EXTRA_IMAGES adds more, space
# separated. Nothing is exported from a local runtime: every image is pulled
# on the node, by `ctr -n k8s.io images pull`, so only the node's registry
# configuration matters.
#
# Nodes. The node list is `kubectl get nodes`. NODE_EXEC says how to reach a
# node: a command prefix with {node} in it, run in front of ctr.
#
#   NODE_EXEC unset                   this script runs on the node; ctr runs here
#   NODE_EXEC='ssh root@{node}'       real hosts, by node name
#   NODE_EXEC='nerdctl exec -i {node}' container nodes
#
# NODE_MAP renames nodes for NODE_EXEC when the node name is not the address:
# space separated `node-name=target` pairs.
#
# Mirrors. ctr is given --hosts-dir HOSTS_DIR, /etc/containerd/certs.d by
# default, the directory the node's containerd reads hosts.toml files from.
# A mirror or a plain-http registry configured there applies to these pulls
# as it applies to the kubelet's.
#
# Reruns. A node that already holds an image is skipped. Stage order: this
# stage runs before stage 3 writes images.txt, so a first run pulls only the
# controller image. install-all.sh runs this stage again after stage 3 to
# pre-pull the external images. Run by hand, rerun it after stage 3 yourself;
# otherwise the kubelet pulls them when the pods schedule. The check fails on
# a missing controller digest and warns on a missing external image.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

HOSTS_DIR="${HOSTS_DIR:-/etc/containerd/certs.d}"
EXTRA_IMAGES="${EXTRA_IMAGES:-}"
NODE_EXEC="${NODE_EXEC:-}"
NODE_MAP="${NODE_MAP:-}"
IMAGES_TXT="${OUT_DIR}/images.txt"
CONTROLLER_REF=""
CONTROLLER_DIGEST=""
EXTERNAL_IMAGES=""
NODES=""
export HOSTS_DIR NODE_EXEC

# resolve_images reads the controller ref from the spec and the external
# images from images.txt, comment and blank lines dropped, EXTRA_IMAGES added.
resolve_images() {
  CONTROLLER_REF="$(release_ref controller)" ||
    die "the release spec ${RELEASE_FILE} has no controller item"
  CONTROLLER_DIGEST="${CONTROLLER_REF##*@}"
  if [[ -f "${IMAGES_TXT}" ]]; then
    EXTERNAL_IMAGES="$(grep -v '^[[:space:]]*#' "${IMAGES_TXT}" | grep -v '^[[:space:]]*$' || true)"
  else
    warn "${IMAGES_TXT} not found: 03-describe-site.sh writes it. Only the controller image is pulled now; install-all.sh reruns this stage after stage 3, or rerun it yourself to pre-pull the external images."
  fi
  EXTERNAL_IMAGES="$(printf '%s %s' "${EXTERNAL_IMAGES}" "${EXTRA_IMAGES}" | xargs)"
  export CONTROLLER_REF EXTERNAL_IMAGES
}

# resolve_nodes fills NODES from the cluster.
resolve_nodes() {
  if [[ -n "${DRY_RUN}" ]]; then
    NODES='<node>'
    return 0
  fi
  NODES="$(kc get nodes -o jsonpath='{range .items[*]}{.metadata.name}{" "}{end}')"
  [[ -n "${NODES// /}" ]] || die "no nodes found with ${SITE_KUBECONFIG}"
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

# registry_host prints the registry a reference is pulled from.
registry_host() {
  local full
  full="$(full_ref "$1")"
  printf '%s' "${full%%/*}"
}

# node_digest prints the digest of an image in the node's k8s.io namespace, or nothing.
node_digest() {
  node_sh "$1" ctr -n k8s.io images ls "name==$(full_ref "$2")" 2>/dev/null | awk 'NR > 1 { print $3; exit }'
}

stage_run() {
  local image node exec_prefix
  resolve_images
  resolve_nodes
  log "controller: ${CONTROLLER_REF}"
  log "external: ${EXTERNAL_IMAGES:-none}"
  log "nodes: ${NODES} via ${NODE_EXEC:-local ctr}, hosts dir ${HOSTS_DIR}"

  # doc-begin
  # The controller image, by digest from the release spec, pulled through the
  # node's containerd. Namespace k8s.io: an image in any other namespace is
  # invisible to the kubelet. --hosts-dir applies the node's hosts.toml
  # mirrors and plain-http registries to this pull as to the kubelet's.
  for node in ${NODES}; do
    if [[ -z "${DRY_RUN}" && "$(node_digest "${node}" "${CONTROLLER_REF}")" == "${CONTROLLER_DIGEST}" ]]; then
      log "${node}: ${CONTROLLER_REF} present"
      continue
    fi
    exec_prefix="$(node_exec "${node}")"
    run ${exec_prefix} ctr -n k8s.io images pull --hosts-dir "${HOSTS_DIR}" "${CONTROLLER_REF}" ||
      die "${node}: pull of ${CONTROLLER_REF} failed; registry $(registry_host "${CONTROLLER_REF}") is not serving it to the node"
  done

  # Every external image in images.txt, pre-pulled on every node so a registry
  # the node cannot reach fails here and not when a pod schedules.
  for image in ${EXTERNAL_IMAGES}; do
    for node in ${NODES}; do
      if [[ -z "${DRY_RUN}" && -n "$(node_digest "${node}" "${image}")" ]]; then
        continue
      fi
      exec_prefix="$(node_exec "${node}")"
      run ${exec_prefix} ctr -n k8s.io images pull --hosts-dir "${HOSTS_DIR}" "${image}" ||
        die "${node}: pull of ${image} failed; registry $(registry_host "${image}") is not serving it to the node"
    done
  done
  # doc-end
}

stage_check() {
  local image node digest
  if [[ -n "${DRY_RUN}" ]]; then
    check_skip "image presence needs the nodes (DRY_RUN)"
    return 0
  fi
  resolve_images
  resolve_nodes
  for node in ${NODES}; do
    digest="$(node_digest "${node}" "${CONTROLLER_REF}")"
    if [[ "${digest}" == "${CONTROLLER_DIGEST}" ]]; then
      check_pass "${node}: controller ${CONTROLLER_REF}"
    elif [[ -z "${digest}" ]]; then
      check_fail "${node}: controller ${CONTROLLER_REF} missing from containerd namespace k8s.io; rerun this stage"
    else
      check_fail "${node}: controller image is ${digest}, the spec says ${CONTROLLER_DIGEST}; rerun this stage"
    fi
  done
  if [[ ! -f "${IMAGES_TXT}" ]]; then
    check_skip "external images: ${IMAGES_TXT} not written yet; run 03-describe-site.sh, then rerun this stage"
    return 0
  fi
  for image in ${EXTERNAL_IMAGES}; do
    for node in ${NODES}; do
      digest="$(node_digest "${node}" "${image}")"
      if [[ -n "${digest}" ]]; then
        check_pass "${node}: ${image} ${digest}"
      else
        check_warn "${node}: ${image} not pre-pulled; the kubelet pulls it from $(registry_host "${image}") when a pod schedules, or rerun this stage"
      fi
    done
  done
}

stage_main "$@"
