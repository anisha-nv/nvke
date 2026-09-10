#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Executed in the builder container with /src read-only and /out, /cache writable.
set -euo pipefail
umask 022
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
mkdir -p "${work}/os-image"
cp -R /src/os-image/packages "${work}/os-image/"
cp -R /src/make "${work}/"
operation="${BUILD_OPERATION:?BUILD_OPERATION is required}"
"${work}/os-image/packages/builder/build-packages.sh" \
  "${work}" /cache /out /opt/nke/bin "${operation}"
