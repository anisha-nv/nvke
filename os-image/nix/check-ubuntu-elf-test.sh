#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
checker=$(cd "$(dirname "$0")" && pwd)/check-ubuntu-elf.sh
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
cat > "$scratch/readelf" <<'MOCK'
#!/usr/bin/env bash
case "$1" in
  -lW) echo "[Requesting program interpreter: ${LOADER:-/lib/ld-linux-aarch64.so.1}]" ;;
  -dW) printf '%s\n' "${DYNAMIC:-(NEEDED) Shared library: [libc.so.6]}" ;;
  -VW) echo "${VERSIONS:-GLIBC_2.17 GLIBC_2.39}" ;;
esac
MOCK
chmod +x "$scratch/readelf"
export PATH="$scratch:$PATH"
bash "$checker" --arch arm64 example
LOADER=/lib64/ld-linux-x86-64.so.2 bash "$checker" --arch amd64 example
for scenario in loader rpath needed libc version patch-version; do
  if (
    case "$scenario" in
      loader) export LOADER=/nix/store/example/ld.so ;;
      rpath) export DYNAMIC='(RUNPATH) Library runpath: [/nix/store/example/lib]' ;;
      needed) export DYNAMIC=$'(NEEDED) Shared library: [libc.so.6]\n(NEEDED) Shared library: [/nix/store/example/lib.so]' ;;
      libc) export DYNAMIC='(NEEDED) Shared library: [libother.so]' ;;
      version) export VERSIONS=GLIBC_2.40 ;;
      patch-version) export VERSIONS=GLIBC_2.39.1 ;;
    esac
    bash "$checker" --arch arm64 example
  ) >"$scratch/result" 2>&1; then
    echo "FAIL: accepted $scenario" >&2; exit 1
  fi
done
echo 'PASS: Ubuntu ELF validation accepts both loaders and rejects six incompatible cases'
