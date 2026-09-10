#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Runs the GRUB luks.key cleanup helper from luks-tpm-firstboot.sh against
# temporary drop-ins. It does not require a VM, TPM, or root filesystem.

set -euo pipefail

SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/luks-tpm-firstboot.sh"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "${TEST_ROOT}"' EXIT
DROPINS="${TEST_ROOT}/grub.d"
HELPER="${TEST_ROOT}/cleanup-helper.sh"
mkdir -p "${DROPINS}"

# Extract and run the production helper so this test exercises its real loop
# and sed command rather than maintaining a second copy of the expression.
awk '/^remove_luks_key_from_grub_dropins\(\)/{copy=1} copy {print} copy && /^}$/ {exit}' "${SCRIPT}" > "${HELPER}"
# shellcheck disable=SC1090
source "${HELPER}"

cat > "${DROPINS}/first.cfg" <<'EOF'
GRUB_CMDLINE_LINUX_DEFAULT="luks.key=TESTKEY console=ttyS0"
EOF
cat > "${DROPINS}/middle.cfg" <<'EOF'
GRUB_CMDLINE_LINUX_DEFAULT="quiet luks.key=TESTKEY console=ttyS0"
EOF
cat > "${DROPINS}/only-key.cfg" <<'EOF'
GRUB_CMDLINE_LINUX_DEFAULT="luks.key=TESTKEY"
EOF
cat > "${DROPINS}/no-key.cfg" <<'EOF'
GRUB_CMDLINE_LINUX_DEFAULT="quiet console=ttyS0"
EOF
cp -p "${DROPINS}/no-key.cfg" "${TEST_ROOT}/no-key.before"

GRUB_UPDATED=false
remove_luks_key_from_grub_dropins "${DROPINS}"

if grep -F -q 'luks.key=' "${DROPINS}"/*.cfg; then
  exit 1
fi
[[ "$(<"${DROPINS}/first.cfg")" == 'GRUB_CMDLINE_LINUX_DEFAULT="console=ttyS0"' ]]
[[ "$(<"${DROPINS}/middle.cfg")" == 'GRUB_CMDLINE_LINUX_DEFAULT="quiet console=ttyS0"' ]]
[[ "$(<"${DROPINS}/only-key.cfg")" == 'GRUB_CMDLINE_LINUX_DEFAULT=""' ]]
cmp -s "${TEST_ROOT}/no-key.before" "${DROPINS}/no-key.cfg"
grep -F -q 'console=ttyS0' "${DROPINS}/first.cfg" "${DROPINS}/middle.cfg" "${DROPINS}/no-key.cfg"

cp -p "${DROPINS}/first.cfg" "${TEST_ROOT}/first.after"
cp -p "${DROPINS}/middle.cfg" "${TEST_ROOT}/middle.after"
cp -p "${DROPINS}/only-key.cfg" "${TEST_ROOT}/only-key.after"
GRUB_UPDATED=false
remove_luks_key_from_grub_dropins "${DROPINS}"
cmp -s "${TEST_ROOT}/first.after" "${DROPINS}/first.cfg"
cmp -s "${TEST_ROOT}/middle.after" "${DROPINS}/middle.cfg"
cmp -s "${TEST_ROOT}/only-key.after" "${DROPINS}/only-key.cfg"
[[ "${GRUB_UPDATED}" == false ]]

echo "luks-tpm-firstboot GRUB cleanup checks passed"
