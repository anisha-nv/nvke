#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Runs the host-FDE (full disk encryption) acceptance scenarios against a
# built FDE image, under QEMU with swtpm as the virtual TPM. Prints
# pass/fail per scenario and exits non-zero if any scenario fails.
#
# Scope: os-image/ Linux host disk encryption only. Kubernetes Secrets/etcd
# encryption-at-rest is a separate controller/agent concern and is not
# covered by this script.
#
# Usage: ./demo-fde.sh [--help]
#
# Env overrides:
#   QCOW          path to the FDE qcow2 (default: the single *.qcow2 under
#                 packer/output-fde/, e.g. nke-node-fde-ubuntu-26.04-amd64.qcow2)
#   SSH_KEY       ssh key used for the build (default: packer/.ssh/id_ed25519)
#   SSH_PORT      host port to forward to guest :22 (default: 2222)
#   BOOT_TIMEOUT  seconds to wait for SSH per boot (default: 360 kvm / 1800 tcg)
#   CPU_MODEL     QEMU -cpu model (default: host kvm / max tcg)
#   TIMEOUT_BIN    timeout-compatible command (default: timeout, then gtimeout)
#   FDE_TEST_OUTPUT_DIR  retain this run's QEMU and swtpm evidence below this
#                 directory; by default the temporary evidence is removed.
#
# No interactive/blind console typing is needed: autoinstall-lvm.pkrtpl bakes
# a build-time luks.key=<passphrase> GRUB parameter, so even the very first
# real boot of a freshly built image unlocks itself via
# files/luks/luks-cmdline-script — the same mechanism production uses via an
# iPXE luks.key= kernel parameter. luks-tpm-firstboot.sh strips luks.key=
# from GRUB once the TPM bind succeeds, so a later boot against a different
# TPM (scenario 2) has no fallback and correctly fails to unlock.

set -euo pipefail

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  sed -n '2,32p' "${BASH_SOURCE[0]}"
  exit 0
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=packer/scripts/qemu-arch.sh
source "${ROOT}/packer/scripts/qemu-arch.sh"
QCOW="${QCOW:-}"
if [[ -z "${QCOW}" ]]; then
  # The output filename encodes the Ubuntu version/arch (see vm_name in
  # nke-node-fde.pkr.hcl), so glob for it instead of hardcoding it here.
  shopt -s nullglob
  fde_qcows=("${ROOT}/packer/output-fde"/*.qcow2)
  shopt -u nullglob
  if [[ ${#fde_qcows[@]} -eq 1 ]]; then
    QCOW="${fde_qcows[0]}"
  else
    echo "demo-fde: QCOW not set and found ${#fde_qcows[@]} qcow2 files under packer/output-fde/ (need exactly 1); run 'make os-image-fde' or set QCOW explicitly" >&2
    exit 1
  fi
fi
SSH_KEY="${SSH_KEY:-${ROOT}/packer/.ssh/id_ed25519}"
SSH_PORT="${SSH_PORT:-2222}"

if [[ ! -f "${QCOW}" ]]; then
  echo "demo: no image at ${QCOW}; run 'make -C packer os-image-fde' first" >&2
  exit 1
fi
if [[ ! -f "${SSH_KEY}" ]]; then
  echo "demo: no ssh key at ${SSH_KEY}; run 'make -C packer ssh-key' first" >&2
  exit 1
fi
if [[ ! -f "${SSH_KEY}.pub" ]]; then
  echo "demo: no public ssh key at ${SSH_KEY}.pub; run 'make -C packer ssh-key' first" >&2
  exit 1
fi

# qemu-img resolves a relative backing path from the overlay's directory, not
# from this script's working directory. Canonicalize both user-provided paths
# before any temporary working directory is created.
QCOW="$(cd "$(dirname "${QCOW}")" && pwd -P)/$(basename "${QCOW}")"
SSH_KEY="$(cd "$(dirname "${SSH_KEY}")" && pwd -P)/$(basename "${SSH_KEY}")"

if [[ -z "${TIMEOUT_BIN:-}" ]]; then
  if command -v timeout >/dev/null 2>&1; then
    TIMEOUT_BIN=timeout
  elif command -v gtimeout >/dev/null 2>&1; then
    TIMEOUT_BIN=gtimeout
  else
    echo "demo: a timeout-compatible command is required (set TIMEOUT_BIN on macOS)" >&2
    exit 3
  fi
fi

nke_qemu_env "$(nke_arch_from_qcow "${QCOW}")"
CPU_MODEL="${CPU_MODEL:-${QEMU_CPU}}"
if [[ "${NKE_ACCELERATED}" -eq 1 ]]; then
  # The first real boot does partition/LUKS/PV/LV growth, a TPM bind+verify,
  # and two full initramfs regens before it ever reaches cloud-init/sshd —
  # comfortably past 180s in practice, so the default here is generous
  # rather than tuned to the fast-path boots.
  BOOT_TIMEOUT="${BOOT_TIMEOUT:-360}"
else
  BOOT_TIMEOUT="${BOOT_TIMEOUT:-1800}"
fi

for tool in "${QEMU_BIN}" qemu-img swtpm cloud-localds ssh "${TIMEOUT_BIN}"; do
  command -v "${tool}" >/dev/null 2>&1 || { echo "demo: required tool '${tool}' not found" >&2; exit 3; }
done

OVMF_CODE="${OVMF_CODE:-${EFI_CODE_DEFAULT}}"
OVMF_VARS_TEMPLATE="${OVMF_VARS_TEMPLATE:-${EFI_VARS_DEFAULT}}"

RETAIN_EVIDENCE=0
if [[ -n "${FDE_TEST_OUTPUT_DIR:-}" ]]; then
  mkdir -p "${FDE_TEST_OUTPUT_DIR}"
  FDE_TEST_OUTPUT_DIR="$(cd "${FDE_TEST_OUTPUT_DIR}" && pwd -P)"
  WORKDIR="$(mktemp -d "${FDE_TEST_OUTPUT_DIR}/demo-fde.XXXXXX")"
  RETAIN_EVIDENCE=1
else
  WORKDIR="$(mktemp -d)"
fi
DISK="${WORKDIR}/nke-node-fde-demo.qcow2"

qemu-img create -f qcow2 -F qcow2 -b "${QCOW}" "${DISK}" >/dev/null

RESULTS=()
report() {
  local scenario="$1" verdict="$2" detail="${3:-}"
  RESULTS+=("${verdict}: ${scenario}${detail:+ — ${detail}}")
  echo "[${verdict}] ${scenario}${detail:+ — ${detail}}"
}

ssh_guest() {
  # timeout, not just ConnectTimeout: ConnectTimeout only bounds the SSH
  # handshake, not the remote command actually returning. Several callers
  # use this inside `until ssh_guest ...; do ...; done` retry loops with
  # their own deadline check in the loop body -- a remote command that
  # connects but never returns would block forever in the loop condition,
  # where that deadline check never runs.
  "${TIMEOUT_BIN}" 30 ssh -i "${SSH_KEY}" -p "${SSH_PORT}" \
    -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=5 -o BatchMode=yes \
    ubuntu@127.0.0.1 "$@"
}

# Boots $DISK against the swtpm state dir at $1 and leaves it running. Sets
# VM_RUNWORK/VM_QPID/VM_SWTPM_PID for wait_ssh/stop_vm to use — deliberately
# does NOT tear the VM down itself, so a caller that needs to run more than
# one ssh_guest command after boot (scenario 1's firstboot-marker and
# luks-tpm-verify checks) has a still-live VM to run them against. Call
# stop_vm once done with a given boot.
VM_RUNWORK=""
VM_QPID=""
VM_SWTPM_PID=""
VM_BOOT_STATE=""
PROCESS_STOP_TIMEOUT=15

process_is_live() {
  local pid="$1"
  [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null
}

stop_process() {
  local pid="$1" deadline
  [[ -z "${pid}" ]] && return 0
  if ! process_is_live "${pid}"; then
    wait "${pid}" 2>/dev/null || true
    return 0
  fi

  kill "${pid}" 2>/dev/null || true
  deadline=$((SECONDS + PROCESS_STOP_TIMEOUT))
  while process_is_live "${pid}"; do
    if (( SECONDS >= deadline )); then
      kill -KILL "${pid}" 2>/dev/null || true
      break
    fi
    sleep 1
  done
  deadline=$((SECONDS + 5))
  while process_is_live "${pid}" && (( SECONDS < deadline )); do
    sleep 1
  done
  if process_is_live "${pid}"; then
    echo "demo-fde: process ${pid} did not terminate after ${PROCESS_STOP_TIMEOUT}s" >&2
    return 1
  fi
  wait "${pid}" 2>/dev/null || true
}

wrong_tpm_console_evidence() {
  local console_log="${VM_RUNWORK}/qemu.log"
  process_is_live "${VM_QPID}" && process_is_live "${VM_SWTPM_PID}" || return 1
  [[ -f "${console_log}" ]] || return 1

  # A timeout alone proves neither that the guest reached its encrypted-root
  # prompt nor that it is using the TPM instance started for this scenario.
  # Keep this narrow: only cryptroot/Clevis failures or an encrypted-disk
  # unlock prompt are accepted as the expected wrong-TPM evidence.
  LC_ALL=C grep -Eiq \
    '(cryptroot|clevis).*(failed|failure|unable|error)|please unlock (the )?(encrypted )?disk .+:|enter passphrase for .*(crypt|encrypted|disk)' \
    "${console_log}"
}

cleanup() {
  local status=$? stopped=1
  trap - EXIT
  stop_vm && stopped=0 || true
  if [[ -n "${WORKDIR:-}" && -d "${WORKDIR}" ]]; then
    if [[ "${stopped}" -ne 0 ]]; then
      echo "demo-fde: process teardown was incomplete; evidence preserved in ${WORKDIR}" >&2
      status=1
    elif [[ "${RETAIN_EVIDENCE}" -eq 1 || "${status}" -ne 0 ]]; then
      echo "demo-fde: evidence retained in ${WORKDIR}" >&2
    else
      rm -rf "${WORKDIR}"
    fi
  fi
  exit "${status}"
}

trap 'exit 130' INT
trap 'exit 143' TERM
trap cleanup EXIT

start_vm() {
  local tpmdir="$1" tpm_deadline
  VM_RUNWORK="$(mktemp -d "${WORKDIR}/boot.XXXXXX")"
  local runwork="${VM_RUNWORK}"
  local efivars="${runwork}/OVMF_VARS.fd"
  install -m 0600 "${OVMF_VARS_TEMPLATE}" "${efivars}"

  cat >"${runwork}/user-data" <<EOF
#cloud-config
ssh_authorized_keys:
  - $(cat "${SSH_KEY}.pub")
EOF
  echo "instance-id: nke-fde-demo-$$-${RANDOM}
local-hostname: nke-fde-demo" >"${runwork}/meta-data"
  cloud-localds "${runwork}/seed.iso" "${runwork}/user-data" "${runwork}/meta-data"

  rm -f "${tpmdir}/swtpm-sock" "${tpmdir}/.lock"
  swtpm socket --tpmstate "dir=${tpmdir}" \
    --ctrl "type=unixio,path=${tpmdir}/swtpm-sock" \
    --tpm2 >"${runwork}/swtpm.log" 2>&1 &
  VM_SWTPM_PID=$!
  tpm_deadline=$((SECONDS + 5))
  while [[ ! -S "${tpmdir}/swtpm-sock" ]]; do
    if ! process_is_live "${VM_SWTPM_PID}" || (( SECONDS >= tpm_deadline )); then
      echo "demo-fde: swtpm did not create its socket" >&2
      return 1
    fi
    sleep 1
  done

  local qemu_args=(
    -machine "type=${QEMU_MACHINE}"
    -cpu "${CPU_MODEL}"
    -m 4096 -smp 2
    "${ACCEL_ARGS[@]}"
    -nographic
    -drive "if=pflash,format=raw,readonly=on,file=${OVMF_CODE}"
    -drive "if=pflash,format=raw,file=${efivars}"
    -drive "if=virtio,file=${DISK},format=qcow2"
    -drive "if=virtio,file=${runwork}/seed.iso,format=raw"
    -chardev "socket,id=chrtpm,path=${tpmdir}/swtpm-sock"
    -tpmdev "emulator,id=tpm0,chardev=chrtpm"
    -device "${TPM_DEVICE_SPEC}"
    -netdev "user,id=net0,hostfwd=tcp::${SSH_PORT}-:22"
    -device "virtio-net,netdev=net0"
  )

  "${QEMU_BIN}" "${qemu_args[@]}" >"${runwork}/qemu.log" 2>&1 &
  VM_QPID=$!
  echo "${VM_QPID}" > "${runwork}/qemu.pid"
  sleep 1
  if ! process_is_live "${VM_QPID}"; then
    echo "demo-fde: QEMU exited before boot; refusing to treat an occupied SSH port as a guest result" >&2
    return 1
  fi
}

# Polls for SSH up to BOOT_TIMEOUT and records the result for the caller.
wait_ssh() {
  local deadline=$((SECONDS + BOOT_TIMEOUT)) up=0
  until ssh_guest true 2>/dev/null; do
    if ! process_is_live "${VM_QPID}"; then
      VM_BOOT_STATE="qemu-exited"
      return
    fi
    if ! process_is_live "${VM_SWTPM_PID}"; then
      VM_BOOT_STATE="swtpm-exited"
      return
    fi
    if (( SECONDS > deadline )); then
      up=1
      break
    fi
    sleep 5
  done
  [[ "${up}" -eq 0 ]] && VM_BOOT_STATE="up" || VM_BOOT_STATE="timeout"
}

# Tears down the VM started by the most recent start_vm.
stop_vm() {
  local stopped=0
  stop_process "${VM_QPID}" || stopped=1
  stop_process "${VM_SWTPM_PID}" || stopped=1
  if [[ "${stopped}" -ne 0 ]]; then
    return 1
  fi
  VM_RUNWORK=""
  VM_QPID=""
  VM_SWTPM_PID=""
  VM_BOOT_STATE=""
}

echo "=== Scenario: host volumes are LUKS2, sealed to the node TPM ==="
TPM_A="${WORKDIR}/tpm-a"
mkdir -p "${TPM_A}"
FIRST_BOOT_PROVEN=0
start_vm "${TPM_A}"
wait_ssh
STATE="${VM_BOOT_STATE}"
if [[ "${STATE}" != "up" ]]; then
  report "host-volumes-luks2-tpm-sealed" "FAIL" "VM did not become SSH-reachable within ${BOOT_TIMEOUT}s (first-boot TPM bind may have failed)"
else
  # VM is still running here (stop_vm hasn't been called yet) — these
  # checks need a live guest, unlike the pass/fail-only scenarios below.
  #
  # sshd/cloud-init's SSH-key injection isn't ordered against
  # luks-tpm-firstboot.service, so SSH can (and does, in practice) become
  # reachable a bit before that service — still mid partition-grow / dual
  # initramfs-regen — finishes. Poll for the marker rather than assuming
  # SSH-up means firstboot-done.
  FIRSTBOOT_DEADLINE=$((SECONDS + 180))
  until ssh_guest 'sudo test -f /root/.luks-tpm-firstboot-done' 2>/dev/null; do
    if (( SECONDS > FIRSTBOOT_DEADLINE )); then
      break
    fi
    sleep 5
  done
  if ssh_guest 'sudo test -f /root/.luks-tpm-firstboot-done' 2>/dev/null; then
    if ssh_guest 'sudo /usr/local/bin/luks-tpm-verify' 2>/dev/null; then
      SLOTS="$(ssh_guest "LUKS_DEV=\$(lsblk -ln -o NAME,FSTYPE | grep crypto_LUKS | awk '{print \"/dev/\" \$1}' | head -1); sudo cryptsetup luksDump \"\${LUKS_DEV}\" | grep -cE '^[[:space:]]+[0-9]+: luks2'" 2>/dev/null || echo "?")"
      if [[ "${SLOTS}" == "1" ]]; then
        report "host-volumes-luks2-tpm-sealed" "PASS" "TPM-bound, unsealable now, passphrase slot removed (1 active key slot)"
        FIRST_BOOT_PROVEN=1
      else
        report "host-volumes-luks2-tpm-sealed" "FAIL" "expected exactly 1 key slot (TPM-only) after bind, got: ${SLOTS}"
      fi
    else
      report "host-volumes-luks2-tpm-sealed" "FAIL" "luks-tpm-verify reported an unsealable or missing TPM binding"
    fi
  else
    report "host-volumes-luks2-tpm-sealed" "FAIL" "/root/.luks-tpm-firstboot-done missing — first-boot binding never completed"
  fi
fi
stop_vm

if [[ "${FIRST_BOOT_PROVEN}" -eq 1 ]]; then
  echo "=== Scenario: an encrypted volume cannot be read off the node ==="
  TPM_B="${WORKDIR}/tpm-b"
  mkdir -p "${TPM_B}"
  start_vm "${TPM_B}"
  wait_ssh
  STATE="${VM_BOOT_STATE}"
  if [[ "${STATE}" == "timeout" ]] && wrong_tpm_console_evidence; then
    report "moved-to-different-tpm-fails" "PASS" "live QEMU and swtpm remained at the encrypted-root unlock failure or prompt"
  else
    report "moved-to-different-tpm-fails" "FAIL" "expected a live TPM-bound encrypted-root unlock failure or prompt; SSH state was ${STATE}"
  fi
  stop_vm

  echo "=== Scenario: encryption survives a same-TPM reboot ==="
  start_vm "${TPM_A}"
  wait_ssh
  STATE="${VM_BOOT_STATE}"
  if [[ "${STATE}" == "up" ]]; then
    if ssh_guest 'sudo grep -F "luks.key=" /proc/cmdline /etc/default/grub /etc/default/grub.d/*.cfg /boot/grub/grub.cfg >/dev/null 2>&1; [ "$?" -eq 1 ]'; then
      report "same-tpm-reboot" "PASS" "a later boot against the original TPM auto-unlocks with no build-time key in GRUB"
    else
      report "same-tpm-reboot" "FAIL" "luks.key remains in the running kernel command line or persisted GRUB configuration"
    fi
  else
    report "same-tpm-reboot" "FAIL" "a later boot against the original TPM failed to auto-unlock within ${BOOT_TIMEOUT}s"
  fi
  stop_vm
else
  report "moved-to-different-tpm-fails" "FAIL" "not attempted because the first same-TPM boot did not prove a TPM-only unlock"
  report "same-tpm-reboot" "FAIL" "not attempted because the first same-TPM boot did not prove a TPM-only unlock"
fi

echo
echo "=== Summary ==="
FAILED=0
for r in "${RESULTS[@]}"; do
  echo "${r}"
  [[ "${r}" == FAIL:* ]] && FAILED=1
done

exit "${FAILED}"
