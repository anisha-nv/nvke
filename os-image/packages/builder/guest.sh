#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Ensure the Packer guest has a verified local package repository. Prepared
# debs take the fast path; otherwise build exactly the staged locked recipes.
set -euo pipefail
umask 022

artifact_root="${1:?usage: guest.sh ARTIFACT_ROOT}"
config="${artifact_root}/package-build.conf"
source_root="${artifact_root}/package-source"
debs_dir="${artifact_root}/debs"

if [[ ! -f "${config}" ]]; then
  echo "guest packages: missing ${config}" >&2
  exit 1
fi
# The Packer host writes only shell-quoted architecture and version selectors.
# shellcheck disable=SC1090
source "${config}"
case "${ARCH:?package-build.conf must set ARCH}" in amd64 | arm64) ;; *) echo 'guest packages: unsupported ARCH' >&2; exit 1 ;; esac
case "${BUILD_OVN:-0}" in 0 | 1) ;; *) echo 'guest packages: BUILD_OVN must be 0 or 1' >&2; exit 1 ;; esac
export ARCH BUILD_OVN
for selector in K8S_VERSIONS CALICO_VERSIONS OVN_STREAMS; do
  if [[ -z "${!selector:-}" ]]; then
    unset "${selector}"
  else
    export "${selector}"
  fi
done
if [[ "$(dpkg --print-architecture)" != "${ARCH}" ]]; then
  echo "guest packages: guest dpkg architecture does not match ARCH=${ARCH}" >&2
  exit 1
fi

verify_debs() {
  local file line listed_file
  local -A listed=()
  if [[ ! -f "${debs_dir}/ARCH" || ! -f "${debs_dir}/Packages" || ! -f "${debs_dir}/SHA256SUMS" ]]; then
    echo 'guest packages: incomplete prepared package repository' >&2
    return 1
  fi
  if [[ "$(<"${debs_dir}/ARCH")" != "${ARCH}" ]]; then
    echo "guest packages: prepared packages do not match ARCH=${ARCH}" >&2
    return 1
  fi
  shopt -s nullglob
  local deb_files=("${debs_dir}"/*.deb)
  if (( ${#deb_files[@]} == 0 )); then
    echo 'guest packages: incomplete prepared package repository has no debs' >&2
    return 1
  fi
  while IFS= read -r line; do
    if [[ ! "${line}" =~ ^[[:xdigit:]]{64}[[:space:]]+\*?(.+)$ ]]; then
      echo 'guest packages: malformed SHA256SUMS' >&2
      return 1
    fi
    listed_file="${BASH_REMATCH[1]}"
    case "${listed_file}" in
      ARCH | Packages | ./*.deb) listed["${listed_file}"]=1 ;;
      *) echo "guest packages: SHA256SUMS contains unsafe path ${listed_file}" >&2; return 1 ;;
    esac
  done < "${debs_dir}/SHA256SUMS"
  for file in ARCH Packages; do
    if [[ -z "${listed[${file}]:-}" ]]; then
      echo "guest packages: SHA256SUMS is missing ${file}" >&2
      return 1
    fi
  done
  for file in "${deb_files[@]}"; do
    listed_file="./$(basename "${file}")"
    if [[ -z "${listed[${listed_file}]:-}" ]]; then
      echo "guest packages: SHA256SUMS is missing ${listed_file}" >&2
      return 1
    fi
  done
  if ! (cd "${debs_dir}" && sha256sum --strict -c SHA256SUMS); then
    echo 'guest packages: prepared package checksum verification failed' >&2
    return 1
  fi
}

if [[ -d "${debs_dir}" ]] && find "${debs_dir}" -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
  verify_debs
  exit 0
fi

if [[ "${BUILD_OVN:-0}" == 1 ]]; then
  echo 'guest packages: BUILD_OVN requires restricted NGC dependencies; build it on an authorized native host' >&2
  exit 1
fi
if [[ ! -x "${source_root}/os-image/packages/builder/build-packages.sh" ]]; then
  echo "guest packages: staged package builder is missing from ${source_root}" >&2
  exit 1
fi
if [[ "$(uname -s)" != Linux ]]; then
  echo 'guest packages: fallback requires a native Linux guest' >&2
  exit 1
fi

build_root="$(mktemp -d "${artifact_root}/build.XXXXXX")"
trap 'rm -rf "${build_root}"' EXIT
mkdir -p "${build_root}/tmp"
export TMPDIR="${build_root}/tmp"
df -hT /tmp "${artifact_root}" >&2
before_packages="${build_root}/packages-before"
after_packages="${build_root}/packages-after"
new_packages="${build_root}/packages-new"

declare -a missing_packages=()
declare -A tool_packages=(
  ["make"]=make ["curl"]=curl ["gpg"]=gnupg ["dpkg-scanpackages"]=dpkg-dev
  ["gzip"]=gzip ["tar"]=tar ["sha256sum"]=coreutils
)
for tool in "${!tool_packages[@]}"; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    missing_packages+=("${tool_packages[${tool}]}")
  fi
done
if (( ${#missing_packages[@]} > 0 )); then
  dpkg-query -W -f '${db:Status-Status} ${binary:Package}\n' | awk '$1 == "installed" { print $2 }' | LC_ALL=C sort > "${before_packages}"
  apt-get update
  apt-get -y install --no-install-recommends "${missing_packages[@]}"
fi

case "${ARCH}" in
  amd64)
    go_archive='go1.26.6.linux-amd64.tar.gz'
    go_sha256='708effb774be8237570d0add163225abbdfaf4fca28b2611df167beba4feef89'
    ;;
  arm64)
    go_archive='go1.26.6.linux-arm64.tar.gz'
    go_sha256='d0507e9e9d7fe012aae570108cbd76c15de879e17130ab8cb90d4d7445cb1f2e'
    ;;
esac
go_tarball="${build_root}/${go_archive}"
curl --fail --silent --show-error --location --connect-timeout 15 --max-time 300 \
  -o "${go_tarball}" "https://go.dev/dl/${go_archive}"
printf '%s  %s\n' "${go_sha256}" "${go_tarball}" | sha256sum -c -
tar -C "${build_root}" -xzf "${go_tarball}"

export GOBIN="${build_root}/bin"
export GOPATH="${build_root}/gopath"
export GOCACHE="${build_root}/gocache"
export GOMODCACHE="${build_root}/gomodcache"
export GOTOOLCHAIN=local
export PATH="${build_root}/go/bin:${PATH}"
mkdir -p "${GOBIN}" "${GOPATH}" "${GOCACHE}" "${GOMODCACHE}"

# The source tree supplies these pinned versions. The Go/module cache remains
# in the temporary build directory and is removed after the package build.
crane_version="$(awk '$1 == "CRANE_VERSION" { print $3; exit }' "${source_root}/make/tool-versions.mk")"
nfpm_version="$(awk '$1 == "NFPM_VERSION" { print $3; exit }' "${source_root}/make/tool-versions.mk")"
if [[ -z "${crane_version}" || -z "${nfpm_version}" ]]; then
  echo 'guest packages: staged tool versions are incomplete' >&2
  exit 1
fi
go install "github.com/google/go-containerregistry/cmd/crane@v${crane_version}"
go install "github.com/goreleaser/nfpm/v2/cmd/nfpm@v${nfpm_version}"

rm -rf "${debs_dir}"
DISCARD_PACKAGE_CACHE=1 "${source_root}/os-image/packages/builder/build-packages.sh" \
  "${source_root}" "${build_root}/cache" "${debs_dir}" "${GOBIN}" debs
verify_debs

if (( ${#missing_packages[@]} > 0 )); then
  dpkg-query -W -f '${db:Status-Status} ${binary:Package}\n' | awk '$1 == "installed" { print $2 }' | LC_ALL=C sort > "${after_packages}"
  LC_ALL=C comm -13 "${before_packages}" "${after_packages}" > "${new_packages}"
  if [[ -s "${new_packages}" ]]; then
    xargs -r apt-get -y purge < "${new_packages}"
  fi
  apt-get clean
fi
