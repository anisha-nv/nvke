#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Run the stage scripts in this directory in order. Each stage runs, then
# checks. The first non-zero exit stops the run.
#
# Stage 2 pulls the external images listed in images.txt, and stage 3 writes
# that file. When both stages are in the selected range, stage 2 runs a second
# time after stage 3, so the external images are on the nodes before stage 5
# schedules anything.
#
# Usage: ./install-all.sh [--from NAME] [--until NAME] [--dry-run] [--kubeconfig PATH]
#
# NAME is a stage script name without its numeric prefix, for example
# stage-images or install-cluster-api.
#
# The guide docs/site-bootstrap/install-site-controller.md is the authority.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

log() { printf '\n=== [install-all] %s\n' "$*" >&2; }
die() {
  printf '[install-all] ERROR: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat >&2 <<'USAGE'
usage: install-all.sh [--from NAME] [--until NAME] [--dry-run] [--kubeconfig PATH]

  --from NAME        start at this stage. Resumes a stopped run.
  --until NAME       stop after this stage.
  --dry-run          print the kubectl and helm argv instead of running them
  --kubeconfig PATH  site cluster kubeconfig; same as SITE_KUBECONFIG
  -h, --help         show this help

NAME is a stage script name without the numeric prefix.
USAGE
  exit 2
}

from=""
until_name=""
while [[ $# -gt 0 ]]; do
  case "$1" in
  --from)
    [[ $# -ge 2 ]] || usage
    from="$2"
    shift 2
    ;;
  --until)
    [[ $# -ge 2 ]] || usage
    until_name="$2"
    shift 2
    ;;
  --dry-run)
    export DRY_RUN=1
    shift
    ;;
  --kubeconfig)
    [[ $# -ge 2 ]] || usage
    export SITE_KUBECONFIG="$2"
    shift 2
    ;;
  -h | --help) usage ;;
  *) usage ;;
  esac
done

# Stage scripts are NN-name.sh. The glob is sorted in C locale, so the number
# prefix is the order.
files=()
names=()
file=""
for file in "${SCRIPT_DIR}"/[0-9][0-9]-*.sh; do
  [[ -e "${file}" ]] || continue
  files+=("${file}")
  name="$(basename "${file}" .sh)"
  names+=("${name#[0-9][0-9]-}")
done
[[ ${#files[@]} -gt 0 ]] || die "no stage scripts found in ${SCRIPT_DIR}"

index_of() {
  local wanted="$1" i
  for i in "${!names[@]}"; do
    if [[ "${names[i]}" == "${wanted}" ]]; then
      printf '%s\n' "${i}"
      return 0
    fi
  done
  printf '[install-all] ERROR: unknown stage %q. Valid stages:\n' "${wanted}" >&2
  printf '  %s\n' "${names[@]}" >&2
  exit 1
}

start=0
end=$((${#files[@]} - 1))
[[ -z "${from}" ]] || start="$(index_of "${from}")"
[[ -z "${until_name}" ]] || end="$(index_of "${until_name}")"
[[ "${start}" -le "${end}" ]] || die "--from ${from} comes after --until ${until_name}"

out_dir="${OUT_DIR:-./.site-install}"

stop() {
  local rc="$1" step="$2" i="$3"
  printf '\n[install-all] %s %s failed with exit code %s\n' "${names[i]}" "${step}" "${rc}" >&2
  printf '[install-all] diagnostics: %s/diagnostics/%s/\n' "${out_dir}" "$(basename "${files[i]}" .sh)" >&2
  printf '[install-all] resume with: %s --from %s\n' "$0" "${names[i]}" >&2
  exit "${rc}"
}

# run_stage runs one stage's run step, then its check step. A non-zero exit
# from either stops the install.
run_stage() {
  local i="$1" rc
  log "stage ${names[i]}: run"
  rc=0
  "${files[i]}" run || rc=$?
  [[ "${rc}" -eq 0 ]] || stop "${rc}" run "${i}"
  log "stage ${names[i]}: check"
  rc=0
  "${files[i]}" check || rc=$?
  [[ "${rc}" -eq 0 ]] || stop "${rc}" check "${i}"
}

# index_or_empty prints the index of a stage name, or nothing when absent.
index_or_empty() {
  local wanted="$1" i
  for i in "${!names[@]}"; do
    if [[ "${names[i]}" == "${wanted}" ]]; then
      printf '%s\n' "${i}"
      return 0
    fi
  done
}

images_idx="$(index_or_empty stage-images)"
describe_idx="$(index_or_empty describe-site)"

i="${start}"
while [[ "${i}" -le "${end}" ]]; do
  run_stage "${i}"
  # Stage 2 ran before images.txt existed. Now that stage 3 has written it,
  # run stage 2 once more so the external images are pre-pulled.
  if [[ -n "${images_idx}" && -n "${describe_idx}" && "${i}" -eq "${describe_idx}" && "${images_idx}" -ge "${start}" ]]; then
    log "pre-pulling external images now that images.txt exists"
    run_stage "${images_idx}"
  fi
  i=$((i + 1))
done

log "all selected stages passed (${names[start]} to ${names[end]})"
