#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Atomically populates the installed-disk build cache directory (Makefile's
# INSTALLED_DIR/<key>) with a fresh install stage's output: the qcow2, its
# efivars.fd (kept, not dropped, by the install-stage Packer source -- see
# nke-node-fde-install.pkr.hcl), and the SSH keypair baked into it.
# provision.sh strips authorized_keys from the final image, so that keypair
# exists nowhere else once the provision stage finishes -- it must be cached
# alongside the disk or a later cache hit could never SSH into it.
#
# Idempotent and safe under concurrent builds racing the same cache key: a
# destination that already has a qcow2 is left alone rather than replaced,
# the same "a stale hit is not fine, but a redundant write is harmless"
# posture prepare-artifacts already takes for the image-inputs scratch dir.

set -euo pipefail

SOURCE_QCOW2="${SOURCE_QCOW2:?SOURCE_QCOW2 is required}"
SOURCE_EFIVARS="${SOURCE_EFIVARS:?SOURCE_EFIVARS is required}"
SSH_PRIVATE_KEY_FILE="${SSH_PRIVATE_KEY_FILE:?SSH_PRIVATE_KEY_FILE is required}"
SSH_PUBLIC_KEY_FILE="${SSH_PUBLIC_KEY_FILE:?SSH_PUBLIC_KEY_FILE is required}"
DEST_DIR="${DEST_DIR:?DEST_DIR is required}"

if [[ -f "${DEST_DIR}/nke-node-installed.qcow2" ]]; then
  echo "cache-installed-disk: ${DEST_DIR} already cached; nothing to do"
  exit 0
fi

for f in "${SOURCE_QCOW2}" "${SOURCE_EFIVARS}" "${SSH_PRIVATE_KEY_FILE}" "${SSH_PUBLIC_KEY_FILE}"; do
  [[ -f "${f}" ]] || { echo "cache-installed-disk: missing input: ${f}" >&2; exit 1; }
done

mkdir -p "$(dirname "${DEST_DIR}")"
stage="$(mktemp -d "$(dirname "${DEST_DIR}")/.installed-cache.tmp.XXXXXX")"
cleanup() { rm -rf -- "${stage}"; }
trap cleanup EXIT

cp "${SOURCE_QCOW2}" "${stage}/nke-node-installed.qcow2"
cp "${SOURCE_EFIVARS}" "${stage}/efivars.fd"
cp "${SSH_PRIVATE_KEY_FILE}" "${stage}/id_ed25519"
cp "${SSH_PUBLIC_KEY_FILE}" "${stage}/id_ed25519.pub"
chmod 600 "${stage}/id_ed25519"

if [[ -f "${DEST_DIR}/nke-node-installed.qcow2" ]]; then
  # Lost a race against a concurrent build of the same key; its result is
  # just as valid, so keep it rather than fail.
  echo "cache-installed-disk: ${DEST_DIR} was cached concurrently; keeping the existing entry"
  exit 0
fi
rm -rf -- "${DEST_DIR}"
mv -- "${stage}" "${DEST_DIR}"
trap - EXIT
echo "cache-installed-disk: cached ${DEST_DIR}"
