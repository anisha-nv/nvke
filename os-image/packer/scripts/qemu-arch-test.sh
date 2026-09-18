#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=qemu-arch.sh
source "${ROOT}/qemu-arch.sh"

workdir="$(mktemp -d)"
trap 'rm -rf "${workdir}"' EXIT
mkdir -p "${workdir}/firmware"
touch "${workdir}/firmware/edk2-aarch64-code.fd" "${workdir}/firmware/edk2-arm-vars.fd"
cat >"${workdir}/qemu" <<EOF
#!/usr/bin/env bash
if [[ "\$1" == -L && "\$2" == help ]]; then printf '%s\\n' '${workdir}/firmware'; fi
if [[ "\$1" == -accel && "\$2" == help ]]; then printf '%s\\n' hvf tcg; fi
if [[ "\$1" == -device && "\$2" == tpm-tis-device,help && -z "\${NO_PPI:-}" ]]; then printf '%s\\n' 'ppi=<bool>'; fi
EOF
chmod +x "${workdir}/qemu"

QEMU_BIN="${workdir}/qemu"
# Exercise firmware search with controlled candidates, independent of OVMF/AAVMF
# installed on the host. An existing distro candidate must beat QEMU's fallback.
touch "${workdir}/distro-code.fd"
[[ "$(nke_find_firmware "$QEMU_BIN" edk2-aarch64-code.fd "${workdir}/distro-code.fd")" == "${workdir}/distro-code.fd" ]]
[[ "$(nke_find_firmware "$QEMU_BIN" edk2-aarch64-code.fd "${workdir}/missing.fd")" == "${workdir}/firmware/edk2-aarch64-code.fd" ]]
if nke_find_firmware "$QEMU_BIN" missing.fd "${workdir}/missing.fd" >/dev/null; then
  echo 'missing firmware unexpectedly found' >&2
  exit 1
fi

unset OVMF_CODE OVMF_VARS_TEMPLATE
uname() { case "$1" in -m) printf '%s\n' arm64 ;; -s) printf '%s\n' Darwin ;; esac; }
sysctl() { printf '%s\n' 1; }
nke_qemu_env arm64
# Architecture selection may use the host's distro firmware or our QEMU fixture.
[[ -f "${EFI_CODE_DEFAULT}" ]]
[[ -f "${EFI_VARS_DEFAULT}" ]]
[[ "${TPM_DEVICE}" == tpm-tis-device ]]
[[ "${ACCEL_ARGS[*]}" == '-accel hvf' ]]
[[ "${TPM_DEVICE_SPEC}" == 'tpm-tis-device,tpmdev=tpm0,ppi=off' ]]

ACCELERATOR=tcg
unset QEMU_CPU
nke_qemu_env arm64
[[ "${NKE_ACCELERATOR}" == tcg ]]
[[ "${NKE_ACCELERATED}" == 0 ]]
[[ "${QEMU_CPU}" == max,pauth-impdef=on ]]
REQUIRE_ACCELERATION=1
if nke_qemu_env arm64 >/dev/null 2>&1; then
  echo 'strict acceleration unexpectedly accepted explicit TCG' >&2
  exit 1
fi
unset ACCELERATOR REQUIRE_ACCELERATION

export NO_PPI=1
nke_qemu_env arm64
[[ "${TPM_DEVICE_SPEC}" == 'tpm-tis-device,tpmdev=tpm0' ]]
unset NO_PPI

uname() { case "$1" in -m) printf '%s\n' x86_64 ;; -s) printf '%s\n' Linux ;; esac; }
OVMF_CODE=/firmware/code OVMF_VARS_TEMPLATE=/firmware/vars
nke_qemu_env arm64
[[ "${ACCEL_ARGS[*]}" == '-accel tcg' ]]
[[ "${NKE_ACCELERATED}" == 0 ]]
[[ "${TPM_DEVICE_SPEC}" == 'tpm-tis-device,tpmdev=tpm0' ]]

REQUIRE_ACCELERATION=1
if nke_qemu_env arm64 >/dev/null 2>&1; then
  echo 'strict acceleration unexpectedly accepted TCG' >&2
  exit 1
fi
unset REQUIRE_ACCELERATION

if nke_qemu_env ppc64le >/dev/null 2>&1; then
  echo 'invalid architecture unexpectedly accepted' >&2
  exit 1
fi
uname() { case "$1" in -m) printf '%s\n' ppc64le ;; -s) printf '%s\n' Linux ;; esac; }
if nke_qemu_env amd64 >/dev/null 2>&1; then
  echo 'invalid host architecture unexpectedly accepted' >&2
  exit 1
fi
if nke_arch_from_qcow /tmp/image.qcow2 >/dev/null 2>&1; then
  echo 'architecture-less qcow unexpectedly accepted' >&2
  exit 1
fi
