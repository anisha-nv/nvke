#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Pull the controller image by digest on every site node, through the node's
# containerd, and pre-pull the external images as a best effort, so the
# controller pod never waits on a registry and the rest rarely does.
#
# Usage: ./02-stage-images.sh [run|check]
#
# The guide docs/site-bootstrap/install-site-controller.md describes this
# stage. The commands between `# doc-begin` and `# doc-end` are the stage's
# work; keep them readable on their own: lib variables, no functions.
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
# Required and best effort. The controller pull is required: a failure ends
# the stage. The external pre-pull is best effort: a failed pull is a WARN
# naming the node, the image and the registry, and the stage goes on, because
# the kubelet pulls that image itself when the pod schedules. PREPULL_EXTERNAL=0
# skips the external pre-pull entirely.
#
# Disk. Every pulled image lands on the node's image filesystem. Below the
# kubelet's eviction threshold the kubelet garbage-collects unused images,
# the staged controller image among them, and marks the node DiskPressure.
# Nodes that share one disk draw on the same space. The stage reads each
# node's headroom first and warns under IMAGEFS_MIN_FREE_PCT (20) free.
#
# Reruns. A node that already holds an image is skipped. Stage order: this
# stage runs before stage 3 writes images.txt, so a first run pulls only the
# controller image. install.sh runs this stage again after stage 3 to
# pre-pull the external images, and once more with PREPULL_EXTERNAL=0 right
# before stage 5 to restore a garbage-collected controller image. Run by hand,
# rerun it after stage 3 yourself; otherwise the kubelet pulls the external
# images when the pods schedule. The check fails on a missing controller
# digest and warns on a missing external image.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"

HOSTS_DIR="${HOSTS_DIR:-/etc/containerd/certs.d}"
EXTRA_IMAGES="${EXTRA_IMAGES:-}"
PREPULL_EXTERNAL="${PREPULL_EXTERNAL:-1}"
NODE_EXEC="${NODE_EXEC:-}"
IMAGES_TXT="${OUT_DIR}/images.txt"
CONTROLLER_REF=""
CONTROLLER_DIGEST=""
EXTERNAL_IMAGES=""
NODES=""
export HOSTS_DIR NODE_EXEC PREPULL_EXTERNAL

# resolve_images reads the controller ref from the spec and the external
# images from images.txt, comment and blank lines dropped, EXTRA_IMAGES added.
resolve_images() {
  CONTROLLER_REF="$(release_ref controller)" ||
    die "the release spec ${RELEASE_FILE} has no controller item"
  CONTROLLER_DIGEST="${CONTROLLER_REF##*@}"
  if [[ -f "${IMAGES_TXT}" ]]; then
    EXTERNAL_IMAGES="$(grep -v '^[[:space:]]*#' "${IMAGES_TXT}" | grep -v '^[[:space:]]*$' || true)"
  elif [[ "${PREPULL_EXTERNAL}" != "0" ]]; then
    warn "${IMAGES_TXT} not found: 03-describe-site.sh writes it. Only the controller image is pulled now; install.sh reruns this stage after stage 3, or rerun it yourself to pre-pull the external images."
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

# warn_imagefs reads each node's image filesystem headroom and warns under
# IMAGEFS_MIN_FREE_PCT free. Advice only: the pulls go ahead.
warn_imagefs() {
  local node pct gb mount
  [[ -z "${DRY_RUN}" ]] || return 0
  for node in ${NODES}; do
    read -r pct gb mount < <(node_imagefs_free "${node}") || true
    if [[ -z "${pct:-}" ]]; then
      warn "${node}: cannot read the image filesystem; crictl imagefsinfo and df ${CONTAINERD_ROOT} failed"
    elif [[ "${pct}" -lt "${IMAGEFS_MIN_FREE_PCT}" ]]; then
      warn "$(imagefs_low_msg "${node}" "${pct}" "${gb}" "${mount}")"
    else
      log "${node}: image filesystem ${mount} ${pct}% free (${gb} GB)"
    fi
  done
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
  if [[ "${PREPULL_EXTERNAL}" == "0" ]]; then
    log "external: not pre-pulled (PREPULL_EXTERNAL=0); the kubelet pulls each image when its pod schedules"
  else
    log "external: ${EXTERNAL_IMAGES:-none}"
  fi
  log "nodes: ${NODES} via ${NODE_EXEC:-local ctr}, hosts dir ${HOSTS_DIR}"
  warn_imagefs

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

  # Every external image in images.txt, pre-pulled on every node so the pods
  # start without waiting on a registry. Best effort: a failed pull is a
  # warning and the stage goes on, because the kubelet pulls the image itself
  # when the pod schedules. PREPULL_EXTERNAL=0 skips this loop; the kubelet
  # then pulls every external image on demand, which costs no node disk now.
  if [[ "${PREPULL_EXTERNAL}" == "0" ]]; then
    log "external images skipped (PREPULL_EXTERNAL=0): the kubelet pulls them on demand"
  else
    for image in ${EXTERNAL_IMAGES}; do
      for node in ${NODES}; do
        if [[ -z "${DRY_RUN}" && -n "$(node_digest "${node}" "${image}")" ]]; then
          continue
        fi
        exec_prefix="$(node_exec "${node}")"
        run ${exec_prefix} ctr -n k8s.io images pull --hosts-dir "${HOSTS_DIR}" "${image}" ||
          warn "${node}: pull of ${image} failed; registry $(registry_host "${image}") is not serving it to the node. The kubelet pulls it when the pod schedules"
      done
    done
  fi
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
  if [[ "${PREPULL_EXTERNAL}" == "0" ]]; then
    check_skip "external images: not pre-pulled (PREPULL_EXTERNAL=0); the kubelet pulls them on demand"
    return 0
  fi
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
