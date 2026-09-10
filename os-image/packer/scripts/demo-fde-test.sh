#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Exercises demo-fde.sh lifecycle and negative-evidence helpers without a
# built image or QEMU. The acceptance run itself remains demo-fde.sh.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT}/demo-fde.sh"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "${TEST_ROOT}"' EXIT
FUNCTIONS_FILE="${TEST_ROOT}/functions.sh"

awk '/^# Boots \$DISK against the swtpm state dir/{copy=1} /^echo "=== Scenario: host volumes/{copy=0} copy' "${SCRIPT}" > "${FUNCTIONS_FILE}"

# stop_vm must wait for its two owned child PIDs without deleting per-boot
# logs. The outer cleanup owns removal after a successful full run.
(
  # shellcheck disable=SC1090
  source "${FUNCTIONS_FILE}"
  trap - EXIT INT TERM
  WORKDIR="${TEST_ROOT}/work"
  RETAIN_EVIDENCE=1
  mkdir -p "${WORKDIR}"
  VM_RUNWORK="${WORKDIR}/boot-a"
  mkdir -p "${VM_RUNWORK}"
  : > "${VM_RUNWORK}/qemu.log"
  sleep 60 & VM_QPID=$!
  sleep 60 & VM_SWTPM_PID=$!
  stop_vm
  [[ -d "${WORKDIR}/boot-a" ]]
  [[ -z "${VM_QPID}" && -z "${VM_SWTPM_PID}" ]]
)

# A generic timeout/error must not be accepted as wrong-TPM proof. A real
# cryptroot failure or encrypted-disk prompt is accepted only while both
# recorded child processes remain live.
(
  # shellcheck disable=SC1090
  source "${FUNCTIONS_FILE}"
  trap - EXIT INT TERM
  WORKDIR="${TEST_ROOT}/evidence"
  RETAIN_EVIDENCE=1
  VM_RUNWORK="${WORKDIR}/boot-b"
  mkdir -p "${VM_RUNWORK}"
  sleep 60 & VM_QPID=$!
  sleep 60 & VM_SWTPM_PID=$!
  printf '%s\n' 'kernel error: unrelated boot failure' > "${VM_RUNWORK}/qemu.log"
  if wrong_tpm_console_evidence; then
    exit 1
  fi
  printf '%s\n' 'Please unlock encrypted disk luks-root:' > "${VM_RUNWORK}/qemu.log"
  wrong_tpm_console_evidence
  kill "${VM_QPID}" "${VM_SWTPM_PID}"
  wait "${VM_QPID}" "${VM_SWTPM_PID}" 2>/dev/null || true
  if wrong_tpm_console_evidence; then
    exit 1
  fi
)

# A dead TPM is reported before an SSH timeout can become a negative result.
(
  # shellcheck disable=SC1090
  source "${FUNCTIONS_FILE}"
  trap - EXIT INT TERM
  ssh_guest() { return 1; }
  sleep 60 & VM_QPID=$!
  VM_SWTPM_PID=""
  # shellcheck disable=SC2034 # Consumed by the sourced wait_ssh helper.
  BOOT_TIMEOUT=1
  wait_ssh
  [[ "${VM_BOOT_STATE}" == "swtpm-exited" ]]
  kill "${VM_QPID}"
  wait "${VM_QPID}" 2>/dev/null || true
)

# Cleanup must retain evidence and force a nonzero exit if teardown fails.
CLEANUP_DIR="${TEST_ROOT}/cleanup"
mkdir -p "${CLEANUP_DIR}"
if (
  # shellcheck disable=SC1090
  source "${FUNCTIONS_FILE}"
  WORKDIR="${CLEANUP_DIR}"
  # shellcheck disable=SC2034 # Consumed by the sourced cleanup helper.
  RETAIN_EVIDENCE=0
  stop_vm() { return 1; }
  cleanup
); then
  exit 1
else
  CLEANUP_STATUS=$?
fi
[[ "${CLEANUP_STATUS}" -eq 1 ]]
[[ -d "${CLEANUP_DIR}" ]]

echo "demo-fde lifecycle checks passed"
