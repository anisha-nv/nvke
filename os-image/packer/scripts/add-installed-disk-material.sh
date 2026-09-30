#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Extends the material manifest prepare-packer-inputs already wrote (which
# has installer-iso and image-inputs) with an installed-disk record, so the
# provision build authenticates its disk input the same way the install
# build authenticates the ISO: by digest, through the same manifest chain
# verify-material.sh and variables.pkr.hcl already check. Symlinks the actual
# qcow2 (cache hit or freshly built) alongside the other materials, the same
# way prepare-packer-inputs symlinks the installer ISO.

set -euo pipefail

MATERIAL_MANIFEST="${MATERIAL_MANIFEST:?MATERIAL_MANIFEST is required}"
INSTALLED_DISK_QCOW2="${INSTALLED_DISK_QCOW2:?INSTALLED_DISK_QCOW2 is required}"
ARCH="${ARCH:?ARCH is required}"
PYTHON="${PYTHON:-python3}"

[[ -f "${MATERIAL_MANIFEST}" ]] || { echo "add-installed-disk-material: no manifest at ${MATERIAL_MANIFEST}" >&2; exit 1; }
[[ -f "${INSTALLED_DISK_QCOW2}" ]] || { echo "add-installed-disk-material: no qcow2 at ${INSTALLED_DISK_QCOW2}" >&2; exit 1; }

material_dir="$(dirname "${MATERIAL_MANIFEST}")/materials"
mkdir -p "${material_dir}"
ln -sf "${INSTALLED_DISK_QCOW2}" "${material_dir}/installed-disk"

digest="$(sha256sum "${INSTALLED_DISK_QCOW2}")"
digest="sha256:${digest%% *}"

ARCH="${ARCH}" DIGEST="${digest}" "${PYTHON}" - "${MATERIAL_MANIFEST}" <<'PY'
import json
import os
import sys

path = sys.argv[1]
with open(path, encoding="utf-8") as f:
    manifest = json.load(f)

manifest["materials"] = [
    m for m in manifest["materials"] if m.get("binding") != "installed-disk"
]
manifest["materials"].append({
    "binding": "installed-disk",
    "component": "nke-installed-disk",
    "kind": "disk-image",
    "architecture": os.environ["ARCH"],
    "digest": os.environ["DIGEST"],
    "path": "materials/installed-disk",
})

tmp = f"{path}.tmp"
with open(tmp, "w", encoding="utf-8") as f:
    json.dump(manifest, f)
os.replace(tmp, path)
PY

echo "add-installed-disk-material: recorded ${INSTALLED_DISK_QCOW2} (${digest}) in ${MATERIAL_MANIFEST}"
