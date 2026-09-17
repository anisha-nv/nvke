#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Packaged by the flake as smoke-builder with pinned SSH and coreutils.
set -euo pipefail
arch='' key='' output='' port=2222 persistence=0
while (($#)); do
  case "$1" in
    --arch) arch=${2:?}; shift 2 ;;
    --key) key=${2:?}; shift 2 ;;
    --port) port=${2:?}; shift 2 ;;
    --output) output=${2:?}; shift 2 ;;
    --check-persistence) persistence=1; shift ;;
    --help|-h) echo 'Usage: smoke-builder --arch amd64|arm64 --key PATH --output DIR [--port PORT] [--check-persistence]'; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; exit 2 ;;
  esac
done
case "$arch" in amd64|arm64) ;; *) echo '--arch must be amd64 or arm64' >&2; exit 2 ;; esac
: "${key:?--key is required}" "${output:?--output is required}"
mkdir -p "$output"
output=$(realpath "$output")
options=(-F /dev/null -i "$(realpath "$key")" -o IdentityAgent=none -o IdentitiesOnly=yes
  -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new
  -o "UserKnownHostsFile=$output/known_hosts")
ssh=(ssh "${options[@]}" -p "$port" root@127.0.0.1)
guest_dir=$("${ssh[@]}" 'mktemp -d /tmp/nke-builder-smoke.XXXXXXXX')
printf -v command 'bash -s -- %q %q %q' "$guest_dir" "$arch" "$persistence"
"${ssh[@]}" "$command" <<'GUEST' | tee "$output/guest.log"
set -euo pipefail
cd "$1"
if [[ "$3" == 1 ]]; then
    test -s /root/.cache/nke-builder-smoke/store-path
    nix-store --check-validity "$(cat /root/.cache/nke-builder-smoke/store-path)"
    test -n "$(find "$(go env GOCACHE)" -name '*-d' -print -quit)"
    echo 'PASS: Nix store object, database registration, and Go cache survived restart'
fi
test "$(go env GOARCH)" = "$2"
test "$(uname -s)" = Linux
for tool in gcc dpkg-deb dpkg-scanpackages curl gpg sha256sum tar rsync; do
    command -v "$tool"
done
go version
cat > probe.go <<'GO'
package main
// #include <unistd.h>
import "C"
import "fmt"
func main() { fmt.Println("cgo pid:", C.getpid()) }
GO
CGO_ENABLED=1 go build -o probe probe.go
./probe
readelf -l probe | grep 'interpreter'
check-ubuntu-elf --arch "$2" probe
mkdir -p package/DEBIAN package/usr/bin extracted
cp probe package/usr/bin/nke-builder-probe
cat > package/DEBIAN/control <<CONTROL
Package: nke-builder-probe
Version: 1.0
Architecture: $2
Maintainer: NKE
Description: NixOS builder smoke test
CONTROL
dpkg-deb --root-owner-group --build package probe.deb
test "$(dpkg-deb --field probe.deb Architecture)" = "$2"
dpkg-deb --extract probe.deb extracted
./extracted/usr/bin/nke-builder-probe
dpkg-scanpackages . /dev/null > Packages
grep -q '^Package: nke-builder-probe$' Packages
mkdir -p /root/.cache/nke-builder-smoke
nix-store --add probe.go > /root/.cache/nke-builder-smoke/store-path
sha256sum probe.deb > probe.sha256
GUEST
scp "${options[@]}" -P "$port" "root@127.0.0.1:$guest_dir/probe.deb" \
  "root@127.0.0.1:$guest_dir/probe.sha256" "$output/"
(cd "$output" && sha256sum --check probe.sha256)
echo "PASS: $arch compiler, Debian packaging, apt index, and artifact transfer"
echo "Artifacts retained in $output; guest scratch directory: $guest_dir"
