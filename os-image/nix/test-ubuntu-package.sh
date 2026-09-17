#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Run as root on Linux, against a disposable, unpacked Ubuntu root filesystem.
set -euo pipefail

package=${1:?Usage: test-ubuntu-package.sh PACKAGE.deb UBUNTU_ROOT}
root=$(realpath "${2:?Usage: test-ubuntu-package.sh PACKAGE.deb UBUNTU_ROOT}")
if [[ "$root" == / || -e "$root/nix" ]]; then
    echo 'Use a disposable Ubuntu root filesystem without /nix.' >&2
    exit 1
fi
grep -Eq '^ID=(ubuntu|"ubuntu")$' "$root/etc/os-release"
arch=$(dpkg-deb --field "$package" Architecture)
test "$(dpkg-deb --field "$package" Package)" = nke-agent
dpkg-deb --extract "$package" "$root"
bash "$(dirname "$0")/check-ubuntu-elf.sh" --arch "$arch" \
    "$root/usr/local/bin/nke-agent"
for binary in nke-agent nke-agent-updater; do
    chroot "$root" /usr/bin/env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin \
        "/usr/local/bin/$binary" --version
done
echo 'PASS: packaged agent and updater execute in Ubuntu without Nix'
