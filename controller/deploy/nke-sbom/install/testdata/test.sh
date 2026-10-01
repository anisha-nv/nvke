#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Tests for 03-describe-site.sh and describe_site.py against the fixtures in
# this directory. Each fixture is copied to a temporary directory beside a
# throwaway CA pair, rendered into a temporary OUT_DIR, and the exit code and
# error text are asserted. Needs python3 only.
#
# Usage: ./test.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="$(cd "${HERE}/.." && pwd)"
SBOM_DIR="$(cd "${INSTALL_DIR}/../.." && pwd)"
RENDERER="${INSTALL_DIR}/describe_site.py"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/describe-site-test.XXXXXX")"
trap 'rm -rf "${WORK}"' EXIT

failures=0
pass() { printf 'PASS  %s\n' "$*"; }
fail() {
  printf 'FAIL  %s\n' "$*"
  failures=$((failures + 1))
}

# A throwaway CA pair. The renderer checks PEM markers, not validity.
write_ca() {
  printf -- '-----BEGIN CERTIFICATE-----\nTEST\n-----END CERTIFICATE-----\n' >"$1/ca.crt"
  printf -- '-----BEGIN PRIVATE KEY-----\nTEST\n-----END PRIVATE KEY-----\n' >"$1/ca.key"
}

# stage DIR FIXTURE: copy a fixture and the CA pair into DIR.
stage() {
  mkdir -p "$1"
  cp "${HERE}/$2" "$1/site-config.yaml"
  write_ca "$1"
}

# expect_error FIXTURE KEY: rendering must exit 1 and name KEY.
expect_error() {
  local fixture="$1" key="$2" dir out rc=0 err
  dir="${WORK}/${fixture%.yaml}"
  out="${dir}/out"
  stage "${dir}" "${fixture}"
  err="$(python3 "${RENDERER}" render --config "${dir}/site-config.yaml" \
    --out "${out}" --sbom-dir "${SBOM_DIR}" 2>&1 >/dev/null)" || rc=$?
  if [[ "${rc}" -ne 1 ]]; then
    fail "${fixture}: expected exit 1, got ${rc}: ${err}"
    return 0
  fi
  if [[ "${err}" != *"${key}"* ]]; then
    fail "${fixture}: error does not name ${key}: ${err}"
    return 0
  fi
  if [[ -e "${out}" ]]; then
    fail "${fixture}: out dir was created on error"
    return 0
  fi
  pass "${fixture}: exit 1, names ${key}"
}

expect_error missing-key.yaml nico.apiName
expect_error empty-value.yaml registry.token
expect_error null-value.yaml tenant.password
expect_error nested-non-scalar.yaml nico.endpoint
expect_error bad-arch.yaml site.arch

# A missing CA file is an error that names the key.
dir="${WORK}/missing-ca"
stage "${dir}" valid.yaml
rm "${dir}/ca.key"
rc=0
err="$(python3 "${RENDERER}" render --config "${dir}/site-config.yaml" \
  --out "${dir}/out" --sbom-dir "${SBOM_DIR}" 2>&1 >/dev/null)" || rc=$?
if [[ "${rc}" -eq 1 && "${err}" == *spire.caKeyFile* ]]; then
  pass "missing CA key file: exit 1, names spire.caKeyFile"
else
  fail "missing CA key file: rc=${rc}: ${err}"
fi

# The valid fixture through the stage script, run then check, no kubeconfig.
dir="${WORK}/valid"
stage "${dir}" valid.yaml
out="${dir}/out"
rc=0
log="$(cd "${dir}" && SITE_CONFIG="${dir}/site-config.yaml" OUT_DIR="${out}" SBOM_DIR="${SBOM_DIR}" \
  SITE_KUBECONFIG='' "${INSTALL_DIR}/03-describe-site.sh" run 2>&1)" || rc=$?
if [[ "${rc}" -eq 0 ]]; then
  pass "valid.yaml: run exits 0"
else
  fail "valid.yaml: run exited ${rc}: ${log}"
fi
if [[ "${log}" == *"db password generated"* ]]; then
  pass "valid.yaml: password generated on first run"
else
  fail "valid.yaml: first run did not report a generated password"
fi
if [[ "${log}" == *"nico-token"* || "${log}" == *"ghp_example"* || "${log}" == *"tenant-pass"* ]]; then
  fail "valid.yaml: summary leaks a credential"
else
  pass "valid.yaml: summary is redacted"
fi

rc=0
log="$(cd "${dir}" && OUT_DIR="${out}" SITE_KUBECONFIG='' "${INSTALL_DIR}/03-describe-site.sh" check 2>&1)" || rc=$?
if [[ "${rc}" -eq 0 && "${log}" == *"SKIP  live database password"* ]]; then
  pass "valid.yaml: check exits 0 and skips the live password"
else
  fail "valid.yaml: check rc=${rc}: ${log}"
fi

for f in bootstrap-config.json controller.yaml spire.yaml postgresql.yaml temporal.yaml db-password; do
  [[ -f "${out}/${f}" ]] || fail "valid.yaml: ${f} missing"
done
mode="$(stat -f '%Lp' "${out}" 2>/dev/null || stat -c '%a' "${out}")"
[[ "${mode}" == "700" ]] && pass "out dir is 0700" || fail "out dir mode is ${mode}"
mode="$(stat -f '%Lp' "${out}/spire.yaml" 2>/dev/null || stat -c '%a' "${out}/spire.yaml")"
[[ "${mode}" == "600" ]] && pass "spire.yaml is 0600" || fail "spire.yaml mode is ${mode}"

# Derived values.
expect_line() { # FILE PATTERN
  if grep -qE -- "$2" "${out}/$1"; then
    pass "$1 has $2"
  else
    fail "$1 lacks $2"
  fi
}
expect_line spire.yaml '^      checksum: "b69d0153c8241383f24da138bc85429a620d9f962a2f10abb314aa0e39046d16"$'
expect_line spire.yaml '^    trustDomain: "demo-site.nke.local"$'
expect_line spire.yaml '^    clusterName: "demo-site"$'
expect_line spire.yaml '^    - "spiffe://demo-site.nke.local/nke-service/cloud-workflow-engine"$'
expect_line spire.yaml '^    jwksUri: "https://192.0.2.10:30443/keys"$'
expect_line spire.yaml '^      - "nke.site-controller.internal"$'
expect_line spire.yaml '^      - "127.0.0.1"$'
expect_line spire.yaml '^      - "192.0.2.10"$'
expect_line spire.yaml '^      enabled: true$'
expect_line spire.yaml '^          certificate: "-----BEGIN CERTIFICATE-----'
expect_line spire.yaml '^          key: "-----BEGIN PRIVATE KEY-----'
expect_line spire.yaml '^          bundle: "-----BEGIN CERTIFICATE-----'
expect_line controller.yaml '^  repository: "registry.example.test:5000/nke/nke-controller"$'
expect_line controller.yaml '^  tag: "v0.1.1"$'
expect_line controller.yaml '^    repository: "cwe-profiles"$'
expect_line controller.yaml '^    tag: "demo"$'
expect_line controller.yaml '^  discoveryURL: "https://192.0.2.10:30443/.well-known/openid-configuration"$'
expect_line controller.yaml '^    serverAddress: "192.0.2.10"$'
expect_line bootstrap-config.json '"ClusterName": "demo-site"'
expect_line bootstrap-config.json '"APIName": "forge"'

# One password in five places.
password="$(cat "${out}/db-password")"
n="$(grep -c -- "\"${password}\"" "${out}/controller.yaml" "${out}/postgresql.yaml" "${out}/temporal.yaml" | awk -F: '{s+=$2} END {print s}')"
if [[ "${n}" -eq 5 ]]; then
  pass "password appears once in controller, twice in postgresql, twice in temporal"
else
  fail "password appears ${n} times across the values files, expected 5"
fi

# Rerun reuses the password.
rc=0
log="$(cd "${dir}" && SITE_CONFIG="${dir}/site-config.yaml" OUT_DIR="${out}" SBOM_DIR="${SBOM_DIR}" \
  SITE_KUBECONFIG='' "${INSTALL_DIR}/03-describe-site.sh" run 2>&1)" || rc=$?
if [[ "${rc}" -eq 0 && "${log}" == *"reusing"* && "$(cat "${out}/db-password")" == "${password}" ]]; then
  pass "rerun reuses the password"
else
  fail "rerun rc=${rc}, password reused: $([[ "$(cat "${out}/db-password")" == "${password}" ]] && echo yes || echo no): ${log}"
fi

# amd64 selects the other checksum.
dir="${WORK}/amd64"
stage "${dir}" valid.yaml
sed -i.bak 's/arch: arm64/arch: amd64/' "${dir}/site-config.yaml"
python3 "${RENDERER}" render --config "${dir}/site-config.yaml" --out "${dir}/out" --sbom-dir "${SBOM_DIR}" >/dev/null 2>&1
if grep -q '46d0caad8c25a027dd11c93e18b58a8bc6fbd9f1fe2e36fa2a0dd440986de4dc' "${dir}/out/spire.yaml"; then
  pass "amd64 selects the amd64 checksum"
else
  fail "amd64 checksum not selected"
fi

# Golden: the key set each file carries.
dir="${WORK}/golden"
stage "${dir}" valid.yaml
if python3 "${RENDERER}" keys --config "${dir}/site-config.yaml" --sbom-dir "${SBOM_DIR}" \
  | diff -u "${HERE}/valid.golden-keys" -; then
  pass "valid.yaml: key set matches valid.golden-keys"
else
  fail "valid.yaml: key set differs from valid.golden-keys"
fi

if [[ "${failures}" -eq 0 ]]; then
  printf '\nall tests passed\n'
else
  printf '\n%d test(s) failed\n' "${failures}"
  exit 1
fi
