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

INSTALL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STAGE_NAME="$(basename "$0" .sh)"
STAGE_MODE=""
CHECK_FAILURES=0
ON_FAIL_DONE=""

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

# The deploy tree the stages install from: controller/deploy.
SBOM_DIR="${SBOM_DIR:-$(cd "${INSTALL_DIR}/../.." && pwd)}"
export SBOM_DIR

# The repository's tool pins. mk_var reads one.
TOOL_VERSIONS_MK="${SBOM_DIR}/../../make/tool-versions.mk"

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
  for ns in cloud-workflow-engine capnico-system; do
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
