# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# shellcheck shell=bash
#
# Shared helpers for the staged site controller install: environment,
# logging, dry-run, kubectl and helm wrappers, check bookkeeping and the
# failure diagnostics trap. Sourced by every stage script. Not run by hand.
#
# Bash 3.2 compatible: no mapfile, no associative arrays.
#
# Header template. Every stage script starts with this block:
#
#   #!/usr/bin/env bash
#   # SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
#   # SPDX-License-Identifier: Apache-2.0
#   #
#   # <One line: what this stage does.>
#   #
#   # Usage: ./NN-name.sh [run|check]
#   #
#   # The guide docs/site-bootstrap/install-site-controller.md is the authority.
#   # Commands between `# doc-begin` and `# doc-end` are extracted verbatim into
#   # the guide. Keep them copy-pasteable: use the lib variables, no functions.
#   set -euo pipefail
#   . "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
#
#   stage_run() { ...; }
#   stage_check() { ...; }
#   stage_main "$@"
#
# Inside stage_check, test with `if`, then call check_pass, check_warn,
# check_skip or check_fail. A bare failing command aborts the check instead
# of counting it.

set -Eeuo pipefail

# deploy/ holds install.sh, this file, the executor nke-deploy.sh, lib/ with
# the Python helpers and stages/ with one script per stage. The deploy path
# the stages install is deploy/nico unless DEPLOY_PATH names another.
DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB_DIR="${DEPLOY_DIR}/lib"
STAGES_DIR="${DEPLOY_DIR}/stages"
REPO_ROOT="$(cd "${DEPLOY_DIR}/.." && pwd)"
export DEPLOY_DIR LIB_DIR STAGES_DIR REPO_ROOT
STAGE_NAME="$(basename "$0" .sh)"
STAGE_MODE=""
CHECK_FAILURES=0
ON_FAIL_DONE=""

# NODE_EXEC reaches a node: a command prefix with {node} in it, empty when the
# script runs on the node. NODE_MAP renames nodes as name=target pairs when the
# node name is not the address NODE_EXEC needs.
NODE_EXEC="${NODE_EXEC:-}"
NODE_MAP="${NODE_MAP:-}"
# node_exec prints the NODE_EXEC prefix for a node, NODE_MAP applied.
node_exec() {
  local node="$1" target="$1" pair
  for pair in ${NODE_MAP}; do
    [[ "${pair%%=*}" != "${node}" ]] || target="${pair#*=}"
  done
  printf '%s' "${NODE_EXEC//\{node\}/${target}}"
}

# node_sh runs a command on a node through the NODE_EXEC prefix. Arguments
# are passed as given; with ssh, an argument with spaces is split again on
# the node, so keep each argument a single word.
node_sh() {
  local node="$1" prefix
  shift
  prefix="$(node_exec "${node}")"
  # shellcheck disable=SC2086
  ${prefix} "$@"
}

# The node's image filesystem. The kubelet evicts pods and garbage-collects
# unused images against this filesystem: imagefs.available<15% by default.
# Images stage 2 pulls are unused until a pod runs them, so they go first.
# CONTAINERD_ROOT is the fallback path when crictl is not on the node.
# IMAGEFS_MIN_FREE_PCT is the free share below which stages 0 and 2 warn.
CONTAINERD_ROOT="${CONTAINERD_ROOT:-/var/lib/containerd}"
IMAGEFS_MIN_FREE_PCT="${IMAGEFS_MIN_FREE_PCT:-20}"
# node_imagefs_free NODE prints "<free-percent> <free-gb> <mountpoint>" for
# the node's image filesystem: the mountpoint crictl imagefsinfo reports,
# else CONTAINERD_ROOT, measured with df. Prints nothing when df fails.
node_imagefs_free() {
  local node="$1" mount total avail
  mount="$(node_sh "${node}" crictl imagefsinfo 2>/dev/null |
    sed -n 's/.*"mountpoint": *"\([^"]*\)".*/\1/p' | head -n 1 || true)"
  mount="${mount:-${CONTAINERD_ROOT}}"
  read -r total avail < <(node_sh "${node}" df -Pk "${mount}" 2>/dev/null | awk 'NR == 2 { print $2, $4 }' || true) || true
  [[ -n "${total:-}" && "${total}" -gt 0 ]] || return 0
  printf '%s %s %s\n' $((avail * 100 / total)) $((avail / 1024 / 1024)) "${mount}"
}
# imagefs_low_msg NODE PCT GB MOUNT prints the warning text for a node whose
# image filesystem is under IMAGEFS_MIN_FREE_PCT free.
imagefs_low_msg() {
  printf '%s: image filesystem %s is %s%% free (%s GB), under %s%%. The kubelet evicts pods and garbage-collects unused images when imagefs.available falls below its threshold, the staged controller image among them. Nodes that share one disk draw on the same space. Free space on the node or pre-pull fewer images (PREPULL_EXTERNAL=0)' \
    "$1" "$4" "$2" "$3" "${IMAGEFS_MIN_FREE_PCT}"
}


log() { printf '\n=== [%s] %s\n' "${STAGE_NAME}" "$*" >&2; }
warn() { printf '[%s] WARN: %s\n' "${STAGE_NAME}" "$*" >&2; }
die() {
  printf '[%s] ERROR: %s\n' "${STAGE_NAME}" "$*" >&2
  exit 1
}

# abs_path prints an absolute path for an existing file or directory.
abs_path() {
  local p="$1"
  if [[ -d "${p}" ]]; then
    (cd "${p}" && pwd)
  else
    printf '%s/%s\n' "$(cd "$(dirname "${p}")" && pwd)" "$(basename "${p}")"
  fi
}

# Environment. Every value has a default and an env override.

# A stage with no cluster access sets STAGE_NEEDS_KUBECONFIG=0 before sourcing
# this file. The kubeconfig is then optional and only resolved when present.
STAGE_NEEDS_KUBECONFIG="${STAGE_NEEDS_KUBECONFIG:-1}"
SITE_KUBECONFIG="${SITE_KUBECONFIG:-}"
if [[ "${STAGE_NEEDS_KUBECONFIG}" != "0" ]]; then
  [[ -n "${SITE_KUBECONFIG}" ]] ||
    die "SITE_KUBECONFIG is not set. Export the path of the site cluster kubeconfig."
  [[ -f "${SITE_KUBECONFIG}" ]] ||
    die "SITE_KUBECONFIG=${SITE_KUBECONFIG} does not exist."
fi
if [[ -n "${SITE_KUBECONFIG}" && -f "${SITE_KUBECONFIG}" ]]; then
  SITE_KUBECONFIG="$(abs_path "${SITE_KUBECONFIG}")"
fi
export SITE_KUBECONFIG

# site-config.yaml holds credentials. Keep it out of the repository.
SITE_CONFIG="${SITE_CONFIG:-./site-config.yaml}"
export SITE_CONFIG

# Rendered values, generated passwords and diagnostics. Ignored by git.
OUT_DIR="${OUT_DIR:-./.site-install}"
(umask 077 && mkdir -p "${OUT_DIR}")
chmod 0700 "${OUT_DIR}"
OUT_DIR="$(abs_path "${OUT_DIR}")"
export OUT_DIR

# The deploy path the stages install. Absolute, so the stages can run from
# any directory.
DEPLOY_PATH="${DEPLOY_PATH:-${DEPLOY_DIR}/nico}"
DEPLOY_PATH="$(cd "${DEPLOY_PATH}" 2>/dev/null && pwd || printf '%s' "${DEPLOY_PATH}")"
export DEPLOY_PATH

# The controller release's namespace, read from the deploy path so the checks
# follow it. The deploy item is named controller in every path.
controller_namespace() {
  local ns=""
  if [[ -f "${DEPLOY_PATH}/controller/release.yaml" ]]; then
    ns="$(python3 "${LIB_DIR}/nke-deploy-items.py" release "${DEPLOY_PATH}/controller/release.yaml" 2>/dev/null | awk -F$'\x1f' '{ print $7 }')"
  fi
  printf '%s' "${ns:-nke-controller}"
}
CONTROLLER_NAMESPACE="${CONTROLLER_NAMESPACE:-$(controller_namespace)}"
export CONTROLLER_NAMESPACE

# The repository's tool pins. mk_var reads one.
TOOL_VERSIONS_MK="${REPO_ROOT}/make/tool-versions.mk"

PHASE_TIMEOUT="${PHASE_TIMEOUT:-15m}"
REQUIRE_TIMEOUT="${REQUIRE_TIMEOUT:-180s}"
DRY_RUN="${DRY_RUN:-}"
export PHASE_TIMEOUT REQUIRE_TIMEOUT DRY_RUN

# run executes a command, or prints its argv when DRY_RUN is set.
run() {
  if [[ -n "${DRY_RUN}" ]]; then
    printf '%q ' "$@"
    printf '\n'
    return 0
  fi
  "$@"
}

# run_from is run with stdin redirected from a file. The dry-run line shows
# the redirect.
run_from() {
  local file="$1"
  shift
  if [[ -n "${DRY_RUN}" ]]; then
    printf '%q ' "$@"
    printf '<%q\n' "${file}"
    return 0
  fi
  "$@" <"${file}"
}

# kc and hl pin every kubectl and helm call to the site kubeconfig.
kc() { run kubectl --kubeconfig "${SITE_KUBECONFIG}" "$@"; }
hl() { run helm --kubeconfig "${SITE_KUBECONFIG}" "$@"; }

# mk_var prints the value of a `NAME ?= value` line in make/tool-versions.mk.
mk_var() {
  sed -n "s/^$1[[:space:]]*?=[[:space:]]*//p" "${TOOL_VERSIONS_MK}" 2>/dev/null | head -n 1
}

# require_cmd dies when a tool is missing from PATH.
require_cmd() {
  local cmd
  for cmd in "$@"; do
    command -v "${cmd}" >/dev/null 2>&1 || die "required tool not found: ${cmd}"
  done
}

# The release spec names every artifact the install pulls: release-manifest.yaml,
# parsed by release_spec.py. Either RELEASE_FILE points at one, or RELEASE_REF
# names an NKD Release and `nkdctl release verify` output is converted into
# ${OUT_DIR}/release-manifest.yaml. A stage that does not read the spec sets
# STAGE_NEEDS_RELEASE=0 before sourcing this file.
STAGE_NEEDS_RELEASE="${STAGE_NEEDS_RELEASE:-1}"
RELEASE_FILE="${RELEASE_FILE:-}"
RELEASE_REF="${RELEASE_REF:-}"
RELEASE_VERIFY_ARGS="${RELEASE_VERIFY_ARGS:-}"
RELEASE_SOURCE_REPO="${RELEASE_SOURCE_REPO:-}"
RELEASE_SOURCE_TAG="${RELEASE_SOURCE_TAG:-}"
RELEASE_SOURCE_COMMIT="${RELEASE_SOURCE_COMMIT:-}"
RELEASE_CHART_VERSIONS="${RELEASE_CHART_VERSIONS:-}"

# release_meta evaluates RELEASE, LANE, SCOPE, SOURCE_REPO, SOURCE_TAG and
# SOURCE_COMMIT into the current shell.
release_meta() {
  eval "$(python3 "${LIB_DIR}/release_spec.py" meta "${RELEASE_FILE}")"
}
# release_ref NAME prints the item's ref. Fails when the item is absent.
release_ref() { python3 "${LIB_DIR}/release_spec.py" ref "${RELEASE_FILE}" "$1"; }
# release_items [TYPE] prints "name<TAB>type<TAB>ref<TAB>version" per item.
release_items() {
  if [[ $# -ge 1 && -n "$1" ]]; then
    python3 "${LIB_DIR}/release_spec.py" items "${RELEASE_FILE}" --type "$1"
  else
    python3 "${LIB_DIR}/release_spec.py" items "${RELEASE_FILE}"
  fi
}

# resolve_release_ref verifies RELEASE_REF and converts the description into
# ${OUT_DIR}/release-manifest.yaml. The converted file is reused while its
# `release:` still equals RELEASE_REF, so every stage does not re-verify.
# Runs for real under DRY_RUN: the stages need the file to print their argv.
resolve_release_ref() {
  local out="${OUT_DIR}/release-manifest.yaml" json="${OUT_DIR}/nkd-release.json" current=""
  if [[ -f "${out}" ]]; then
    current="$(python3 "${LIB_DIR}/release_spec.py" meta "${out}" 2>/dev/null |
      sed -n "s/^RELEASE='\(.*\)'$/\1/p")"
    if [[ "${current}" == "${RELEASE_REF}" ]]; then
      RELEASE_FILE="${out}"
      return 0
    fi
  fi
  [[ -n "${RELEASE_SOURCE_REPO}" && -n "${RELEASE_SOURCE_TAG}" ]] ||
    die "RELEASE_REF needs the source pin the description lacks. Export RELEASE_SOURCE_REPO and RELEASE_SOURCE_TAG (RELEASE_SOURCE_COMMIT is optional) and RELEASE_CHART_VERSIONS=name=version,..."
  require_cmd nkdctl python3
  log "verifying release ${RELEASE_REF}"
  # shellcheck disable=SC2086
  nkdctl release verify --reference "${RELEASE_REF}" ${RELEASE_VERIFY_ARGS} >"${json}" ||
    die "nkdctl release verify failed for ${RELEASE_REF}"
  python3 "${LIB_DIR}/nkd-release-to-nke.py" "${json}" \
    --source-repo "${RELEASE_SOURCE_REPO}" --source-tag "${RELEASE_SOURCE_TAG}" \
    --source-commit "${RELEASE_SOURCE_COMMIT}" --versions "${RELEASE_CHART_VERSIONS}" >"${out}.tmp" ||
    die "converting ${json} into ${out} failed"
  mv "${out}.tmp" "${out}"
  RELEASE_FILE="${out}"
}

if [[ "${STAGE_NEEDS_RELEASE}" != "0" ]]; then
  if [[ -z "${RELEASE_FILE}" && -z "${RELEASE_REF}" ]]; then
    die "neither RELEASE_FILE nor RELEASE_REF is set. Export RELEASE_FILE=<path to release-manifest.yaml> or RELEASE_REF=<OCI reference of an NKD Release>."
  fi
  if [[ -z "${RELEASE_FILE}" ]]; then
    resolve_release_ref
  fi
  [[ -f "${RELEASE_FILE}" ]] ||
    die "RELEASE_FILE=${RELEASE_FILE} does not exist."
  RELEASE_FILE="$(abs_path "${RELEASE_FILE}")"
fi
export RELEASE_FILE RELEASE_REF RELEASE_VERIFY_ARGS

# One line per finding. Only check_fail counts towards the exit code: WARN
# is advice, SKIP marks a check that needs a cluster or the network and was
# not run, as under DRY_RUN.
check_pass() { printf 'PASS  %s\n' "$*"; }
check_warn() { printf 'WARN  %s\n' "$*"; }
check_skip() { printf 'SKIP  %s\n' "$*"; }
check_fail() {
  printf 'FAIL  %s\n' "$*"
  CHECK_FAILURES=$((CHECK_FAILURES + 1))
}

# redact masks values whose key matches password, token, secret or key.
# Reads stdin. Handles `key: value` and `KEY=value` lines.
redact() {
  awk '{
    lower = tolower($0)
    if (match(lower, /^[[:space:]]*[^:=]*(password|token|secret|key)[^:=]*[:=][[:space:]]*/)) {
      print substr($0, 1, RLENGTH) "<redacted>"
    } else {
      print $0
    }
  }'
}

# on_fail collects cluster state after a failed run step. Never fails itself.
on_fail() {
  local rc="${1:-1}"
  [[ -z "${ON_FAIL_DONE}" ]] || return 0
  ON_FAIL_DONE=1
  printf '[%s] run failed with exit code %s\n' "${STAGE_NAME}" "${rc}" >&2
  if [[ -n "${DRY_RUN}" || -z "${SITE_KUBECONFIG}" ]]; then
    return 0
  fi
  local dir="${OUT_DIR}/diagnostics/${STAGE_NAME}"
  mkdir -p "${dir}" 2>/dev/null || return 0
  local k="kubectl --kubeconfig ${SITE_KUBECONFIG}"
  ${k} get nodes -o wide >"${dir}/nodes.txt" 2>&1 || true
  ${k} get pods -A -o wide >"${dir}/pods.txt" 2>&1 || true
  { ${k} get events -A --sort-by=.lastTimestamp 2>&1 || true; } | tail -200 >"${dir}/events.txt" || true
  helm --kubeconfig "${SITE_KUBECONFIG}" list -A >"${dir}/helm-list.txt" 2>&1 || true
  local ns deploy name
  for ns in "${CONTROLLER_NAMESPACE}" capnico-system; do
    ${k} get namespace "${ns}" >/dev/null 2>&1 || continue
    for deploy in $(${k} -n "${ns}" get deployments -o name 2>/dev/null || true); do
      name="${deploy#deployment.apps/}"
      ${k} -n "${ns}" logs "${deploy}" --all-containers --tail=200 \
        >"${dir}/${ns}-${name}.log" 2>&1 || true
    done
  done
  printf '[%s] diagnostics written to %s\n' "${STAGE_NAME}" "${dir}" >&2
  return 0
}

on_exit() {
  local rc=$?
  if [[ "${rc}" -ne 0 && "${STAGE_MODE}" == "run" ]]; then
    on_fail "${rc}" || true
  fi
}

stage_usage() {
  printf 'Usage: ./%s.sh [run|check]\n' "${STAGE_NAME}" >&2
  exit 2
}

# stage_main dispatches to stage_run or stage_check, defined by the caller.
# Exit code: 0 after run, the failure count after check.
stage_main() {
  STAGE_MODE="${1:-run}"
  [[ $# -le 1 ]] || stage_usage
  case "${STAGE_MODE}" in
  run)
    trap 'on_fail $?' ERR
    trap on_exit EXIT
    log "run"
    stage_run
    log "run complete"
    exit 0
    ;;
  check)
    log "check"
    stage_check
    if [[ "${CHECK_FAILURES}" -eq 0 ]]; then
      log "check passed"
    else
      log "check failed: ${CHECK_FAILURES} finding(s)"
    fi
    exit "${CHECK_FAILURES}"
    ;;
  *)
    stage_usage
    ;;
  esac
}
