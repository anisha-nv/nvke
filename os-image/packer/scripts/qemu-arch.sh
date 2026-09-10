#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Shared QEMU architecture selection for the image boot harnesses. The image
# filename is the source of truth, so an arm64 image cannot accidentally be
# launched with x86 QEMU.

nke_arch_from_qcow() {
  case "$(basename "$1")" in
    *-amd64.qcow2) printf '%s\n' amd64 ;;
    *-arm64.qcow2) printf '%s\n' arm64 ;;
    *)
      echo "qemu-arch: cannot determine architecture from '$1' (expected *-amd64.qcow2 or *-arm64.qcow2)" >&2
      return 1
      ;;
  esac
}

# Prefer distro firmware paths, then the EDK2 files QEMU exposes through -L.
nke_find_firmware() {
  local qemu="$1" name="$2" candidate dir
  shift 2
  for candidate in "$@"; do
    if [[ -f "$candidate" ]]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  while IFS= read -r dir; do
    if [[ -n "$dir" && -f "$dir/$name" ]]; then
      printf '%s\n' "$dir/$name"
      return 0
    fi
  done < <("$qemu" -L help 2>/dev/null)
  return 1
}

# shellcheck disable=SC2034 # Values are the public interface for callers that source this file.
# Sets QEMU_BIN, QEMU_MACHINE, QEMU_CPU, ACCEL_ARGS, TPM_DEVICE,
# EFI_CODE_DEFAULT, EFI_VARS_DEFAULT, NKE_ACCELERATED, NKE_ACCELERATOR and
# TPM_DEVICE_SPEC. TPM_DEVICE_SPEC includes the software TPM's tpmdev id.
nke_qemu_env() {
  local arch="$1" host_arch host_os qemu_cpu_override="${QEMU_CPU:-}" hvf_supported=0 kvm_supported=0
  case "$arch" in
    amd64)
      QEMU_BIN="${QEMU_BIN:-qemu-system-x86_64}"
      QEMU_MACHINE="${QEMU_MACHINE:-q35}"
      EFI_CODE_DEFAULT="$(nke_find_firmware "$QEMU_BIN" edk2-x86_64-code.fd /usr/share/OVMF/OVMF_CODE_4M.fd)" || true
      EFI_VARS_DEFAULT="$(nke_find_firmware "$QEMU_BIN" edk2-i386-vars.fd /usr/share/OVMF/OVMF_VARS_4M.fd)" || true
      TPM_DEVICE=tpm-tis
      ;;
    arm64)
      QEMU_BIN="${QEMU_BIN:-qemu-system-aarch64}"
      QEMU_MACHINE="${QEMU_MACHINE:-virt}"
      EFI_CODE_DEFAULT="$(nke_find_firmware "$QEMU_BIN" edk2-aarch64-code.fd /usr/share/AAVMF/AAVMF_CODE.fd)" || true
      EFI_VARS_DEFAULT="$(nke_find_firmware "$QEMU_BIN" edk2-arm-vars.fd /usr/share/AAVMF/AAVMF_VARS.fd)" || true
      TPM_DEVICE=tpm-tis-device
      ;;
    *)
      echo "qemu-arch: unsupported architecture '$arch': expected amd64 or arm64" >&2
      return 1
      ;;
  esac

  if [[ -z "$EFI_CODE_DEFAULT" && -z "${OVMF_CODE+x}" ]] || [[ -z "$EFI_VARS_DEFAULT" && -z "${OVMF_VARS_TEMPLATE+x}" ]]; then
    echo "qemu-arch: no UEFI firmware found for $arch; set OVMF_CODE and OVMF_VARS_TEMPLATE" >&2
    return 1
  fi

  case "$(uname -m)" in
    x86_64|amd64) host_arch=amd64 ;;
    aarch64|arm64) host_arch=arm64 ;;
    *)
      echo "qemu-arch: unsupported host architecture '$(uname -m)'" >&2
      return 1
      ;;
  esac
  host_os="$(uname -s)"
  if "$QEMU_BIN" -accel help 2>/dev/null | grep -Fxq hvf; then
    hvf_supported=1
  fi
  if "$QEMU_BIN" -accel help 2>/dev/null | grep -Fxq kvm; then
    kvm_supported=1
  fi

  if [[ "$host_arch" != "$arch" ]]; then
    ACCEL_ARGS=(-accel tcg)
    NKE_ACCELERATED=0
    NKE_ACCELERATOR=tcg
  elif [[ "$host_os" == Darwin && "$(sysctl -n kern.hv_support 2>/dev/null)" == 1 && "$hvf_supported" == 1 ]]; then
    ACCEL_ARGS=(-accel hvf)
    NKE_ACCELERATED=1
    NKE_ACCELERATOR=hvf
  elif [[ "$host_os" == Linux && -c /dev/kvm && -w /dev/kvm && "$kvm_supported" == 1 ]]; then
    ACCEL_ARGS=(-enable-kvm)
    NKE_ACCELERATED=1
    NKE_ACCELERATOR=kvm
  else
    ACCEL_ARGS=(-accel tcg)
    NKE_ACCELERATED=0
    NKE_ACCELERATOR=tcg
  fi

  # TCG is an explicit development choice even where hardware acceleration is
  # available. Hardware accelerator requests must match the detected usable
  # capability; accepting a forced mismatch would fail only after a build has
  # started.
  case "${ACCELERATOR:-}" in
    ""|"${NKE_ACCELERATOR}") ;;
    tcg)
      ACCEL_ARGS=(-accel tcg)
      NKE_ACCELERATED=0
      NKE_ACCELERATOR=tcg
      ;;
    kvm|hvf)
      echo "qemu-arch: requested accelerator ${ACCELERATOR} is unavailable for ${arch}; detected ${NKE_ACCELERATOR}" >&2
      return 1
      ;;
    *)
      echo "qemu-arch: unsupported accelerator '${ACCELERATOR}': expected kvm, hvf, or tcg" >&2
      return 1
      ;;
  esac

  QEMU_CPU="${qemu_cpu_override:-$(if [[ "$NKE_ACCELERATED" == 1 ]]; then printf host; else printf max; fi)}"

  TPM_DEVICE_SPEC="${TPM_DEVICE},tpmdev=tpm0"
  # QEMU 11's ARM tpm-tis-device PPI RAM region cannot be mapped by HVF
  # (HV_BAD_ARGUMENT in hvf-all.c). Disabling PPI fixes that path. Only add
  # the property when QEMU advertises it: older ARM QEMU releases do not have
  # PPI and reject an unknown property instead.
  if [[ "$NKE_ACCELERATOR" == hvf && "$TPM_DEVICE" == tpm-tis-device ]] && \
    "$QEMU_BIN" -device "${TPM_DEVICE},help" 2>/dev/null | grep -Eq '(^|[[:space:]])ppi='; then
    TPM_DEVICE_SPEC+=",ppi=off"
  fi

  if [[ "${REQUIRE_ACCELERATION:-0}" == 1 && "$NKE_ACCELERATED" != 1 ]]; then
    echo "qemu-arch: ${arch} cannot be hardware accelerated on this ${host_arch} ${host_os} host" >&2
    return 1
  fi
}
