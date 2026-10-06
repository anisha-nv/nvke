#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Tests for release_spec.py, nkd-release-to-nke.py and the release checks of
# lib.sh and 00-check-readiness.sh, against the fixtures in release-spec/. Needs
# python3 and git only.
#
# Usage: ./test_release_spec.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "${HERE}/.." && pwd)"
LIB_DIR="${DEPLOY_DIR}/lib"
REPO_ROOT="$(cd "${DEPLOY_DIR}/.." && pwd)"
FIXTURES="${HERE}/release-spec"
SPEC="${LIB_DIR}/release_spec.py"
CONVERT="${LIB_DIR}/nkd-release-to-nke.py"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/release-spec-test.XXXXXX")"
trap 'rm -rf "${WORK}"' EXIT

failures=0
pass() { printf 'PASS  %s\n' "$*"; }
fail() {
  printf 'FAIL  %s\n' "$*"
  failures=$((failures + 1))
}

# expect_error FIXTURE TEXT: parsing must exit 1 and the error must name TEXT.
expect_error() {
  local fixture="$1" text="$2" rc=0 err
  err="$(python3 "${SPEC}" meta "${FIXTURES}/${fixture}" 2>&1 >/dev/null)" || rc=$?
  if [[ "${rc}" -ne 1 ]]; then
    fail "${fixture}: expected exit 1, got ${rc}: ${err}"
  elif [[ "${err}" != *"${text}"* ]]; then
    fail "${fixture}: error does not say '${text}': ${err}"
  elif [[ "${err}" != *"${fixture}"* ]]; then
    fail "${fixture}: error does not name the file: ${err}"
  else
    pass "${fixture}: exit 1, ${text}"
  fi
}

expect_error bad-type.yaml ":12: item controller: type oci"
expect_error ref-without-digest.yaml ":12: item controller: ref must end in @sha256:<64 hex>"
expect_error chart-without-version.yaml ":12: item postgresql: chart items need version"
expect_error image-with-version.yaml ":12: item controller: only chart items carry version"
expect_error duplicate-name.yaml ":13: duplicate item name: controller"
expect_error unknown-key.yaml ":11: unrecognised line: checksums: none"
expect_error bad-indent.yaml ":12: unrecognised item line"
expect_error missing-source-tag.yaml "missing source.tag"

# The valid fixture: every subcommand.
valid="${FIXTURES}/valid.yaml"
zeros="$(printf '0%.0s' $(seq 1 64))"
aaaa="$(printf 'a%.0s' $(seq 1 64))"
meta="$(python3 "${SPEC}" meta "${valid}")"
expected_meta="RELEASE='registry.example.test:5000/nke/releases@sha256:${zeros}'
LANE='local-assembly'
SCOPE='none'
SOURCE_REPO='https://github.com/dsx-ai-factory/nke'
SOURCE_TAG='controller/v0.1.1'
SOURCE_COMMIT='0123456789abcdef0123456789abcdef01234567'"
if [[ "${meta}" == "${expected_meta}" ]]; then
  pass "valid.yaml: meta prints six single-quoted lines"
else
  fail "valid.yaml: meta differs: ${meta}"
fi
eval "${meta}"
[[ "${LANE}" == "local-assembly" && "${SOURCE_TAG}" == "controller/v0.1.1" ]] &&
  pass "valid.yaml: meta evals into the shell" || fail "valid.yaml: meta did not eval"

items="$(python3 "${SPEC}" items "${valid}")"
if [[ "$(printf '%s\n' "${items}" | wc -l | tr -d ' ')" -eq 6 ]]; then
  pass "valid.yaml: items lists six items"
else
  fail "valid.yaml: items: ${items}"
fi
if [[ "$(printf '%s\n' "${items}" | head -n 1)" == "$(printf 'controller\timage\tregistry.example.test:5000/nke/cwe@sha256:%s\t' "${aaaa}")" ]]; then
  pass "valid.yaml: items is TSV in file order with an empty version"
else
  fail "valid.yaml: first item line: $(printf '%s\n' "${items}" | head -n 1)"
fi
charts="$(python3 "${SPEC}" items "${valid}" --type chart | cut -f1,4 | tr '\t\n' '= ')"
if [[ "${charts}" == "postgresql=18.5.15 spire-crds=0.6.1 temporal=0.74.0 spire=0.30.1 " ]]; then
  pass "valid.yaml: items --type chart carries the versions"
else
  fail "valid.yaml: chart items: ${charts}"
fi
[[ "$(python3 "${SPEC}" items "${valid}" --type binary | cut -f1)" == "nkectl" ]] &&
  pass "valid.yaml: items --type binary" || fail "valid.yaml: items --type binary"
[[ "$(python3 "${SPEC}" ref "${valid}" controller)" == "registry.example.test:5000/nke/cwe@sha256:${aaaa}" ]] &&
  pass "valid.yaml: ref controller" || fail "valid.yaml: ref controller"
[[ "$(python3 "${SPEC}" digest "${valid}" controller)" == "sha256:${aaaa}" ]] &&
  pass "valid.yaml: digest controller" || fail "valid.yaml: digest controller"
rc=0
err="$(python3 "${SPEC}" ref "${valid}" missing 2>&1 >/dev/null)" || rc=$?
[[ "${rc}" -eq 1 && "${err}" == *"no item named missing"* ]] &&
  pass "valid.yaml: ref of an absent item exits 1" || fail "valid.yaml: ref missing rc=${rc}: ${err}"
rc=0
python3 "${SPEC}" items "${valid}" --type helm >/dev/null 2>&1 || rc=$?
[[ "${rc}" -ne 0 ]] && pass "items --type helm is rejected" || fail "items --type helm accepted"

# The converter: a nkd.dev/release/v3 description to the expected YAML.
out="${WORK}/converted.yaml"
rc=0
err="$(python3 "${CONVERT}" "${FIXTURES}/nkd-release.json" \
  --source-repo https://github.com/dsx-ai-factory/nke --source-tag controller/v0.1.1 \
  --versions postgresql=18.5.15,spire-crds=0.6.1,temporal=0.74.0,spire=0.30.1 2>&1 >"${out}")" || rc=$?
if [[ "${rc}" -eq 0 ]] && diff -u "${FIXTURES}/nkd-release.expected.yaml" "${out}"; then
  pass "converter: output matches nkd-release.expected.yaml"
else
  fail "converter: rc=${rc}: ${err}"
fi
[[ "${err}" == *"dropping apt-repository nke-os"* ]] &&
  pass "converter: notes the dropped apt-repository" || fail "converter: no note for the dropped kind: ${err}"
if python3 "${SPEC}" meta "${out}" >/dev/null 2>&1; then
  pass "converter: output parses"
else
  fail "converter: output does not parse"
fi
# stdin, and the cwe rename by component.
rc=0
sed 's/"name": "cwe"/"name": "nke-controller"/' "${FIXTURES}/nkd-release.json" |
  python3 "${CONVERT}" --source-repo r --source-tag t --source-commit abc \
    --versions postgresql=1,spire-crds=1,temporal=1,spire=1 >"${WORK}/stdin.yaml" 2>/dev/null || rc=$?
if [[ "${rc}" -eq 0 && "$(python3 "${SPEC}" ref "${WORK}/stdin.yaml" controller)" == "ghcr.io/example/nke-recipes/cwe@sha256:${aaaa}" ]]; then
  pass "converter: reads stdin, renames the cwe component to controller, keeps the reference"
else
  fail "converter: stdin rc=${rc}"
fi
grep -q '^  commit: abc$' "${WORK}/stdin.yaml" && pass "converter: writes source.commit" || fail "converter: source.commit missing"
# temporal has no reference in the fixture: built from the subject's parent.
[[ "$(python3 "${SPEC}" ref "${out}" temporal)" == "ghcr.io/example/nke-recipes/temporal@sha256:${zeros}" ]] &&
  pass "converter: builds a missing reference from the subject" || fail "converter: temporal ref: $(python3 "${SPEC}" ref "${out}" temporal)"
# Missing flags name what to pass.
rc=0
err="$(python3 "${CONVERT}" "${FIXTURES}/nkd-release.json" 2>&1 >/dev/null)" || rc=$?
[[ "${rc}" -eq 1 && "${err}" == *"--source-repo and --source-tag"* ]] &&
  pass "converter: missing source flags are named" || fail "converter: rc=${rc}: ${err}"
rc=0
err="$(python3 "${CONVERT}" "${FIXTURES}/nkd-release.json" --source-repo r --source-tag t 2>&1 >/dev/null)" || rc=$?
[[ "${rc}" -eq 1 && "${err}" == *"--versions postgresql=<version>,spire-crds=<version>,temporal=<version>,spire=<version>"* ]] &&
  pass "converter: missing chart versions are named" || fail "converter: rc=${rc}: ${err}"

# lib.sh: the release is required unless a stage opts out.
: >"${WORK}/kubeconfig"
rc=0
err="$(cd "${WORK}" && OUT_DIR="${WORK}/out" SITE_KUBECONFIG="${WORK}/kubeconfig" DRY_RUN=1 \
  "${DEPLOY_DIR}/stages/00-check-readiness.sh" check 2>&1 >/dev/null)" || rc=$?
[[ "${rc}" -eq 1 && "${err}" == *"neither RELEASE_FILE nor RELEASE_REF"* ]] &&
  pass "lib.sh: dies naming RELEASE_FILE and RELEASE_REF" || fail "lib.sh: rc=${rc}: ${err}"
rc=0
err="$(cd "${WORK}" && OUT_DIR="${WORK}/out" SITE_KUBECONFIG="${WORK}/kubeconfig" DRY_RUN=1 \
  RELEASE_REF="registry.example.test:5000/nke/releases@sha256:${zeros}" \
  "${DEPLOY_DIR}/stages/00-check-readiness.sh" check 2>&1 >/dev/null)" || rc=$?
[[ "${rc}" -eq 1 && "${err}" == *"RELEASE_SOURCE_REPO and RELEASE_SOURCE_TAG"* ]] &&
  pass "lib.sh: RELEASE_REF without a source pin names the variables" || fail "lib.sh: rc=${rc}: ${err}"
# A converted file in OUT_DIR for the same RELEASE_REF is reused without nkdctl.
mkdir -p "${WORK}/out"
cp "${valid}" "${WORK}/out/release-manifest.yaml"
rc=0
log="$(cd "${WORK}" && OUT_DIR="${WORK}/out" SITE_KUBECONFIG="${WORK}/kubeconfig" DRY_RUN=1 PATH="/usr/bin:/bin" \
  RELEASE_REF="registry.example.test:5000/nke/releases@sha256:${zeros}" \
  "${DEPLOY_DIR}/stages/00-check-readiness.sh" check 2>&1)" || rc=$?
# lib.sh prints the realpath; on macOS $TMPDIR resolves through /private, so match the suffix.
[[ "${log}" == *"PASS  release spec: "*"/out/release-manifest.yaml"* ]] &&
  pass "lib.sh: reuses the converted spec for the same RELEASE_REF" || fail "lib.sh: reuse: ${log}"

# 00-check-readiness.sh: the release checks against this checkout.
rc=0
log="$(cd "${WORK}" && OUT_DIR="${WORK}/out" SITE_KUBECONFIG="${WORK}/kubeconfig" DRY_RUN=1 \
  RELEASE_FILE="${valid}" "${DEPLOY_DIR}/stages/00-check-readiness.sh" check 2>&1)" || rc=$?
[[ "${log}" == *"INFO  release: registry.example.test:5000/nke/releases@sha256:${zeros} lane=local-assembly scope=none"* ]] &&
  pass "stage 0: prints lane and scope" || fail "stage 0: no INFO line: ${log}"
[[ "${log}" == *"WARN  release lane is local-assembly"* ]] &&
  pass "stage 0: warns on a lane other than pipeline-attested" || fail "stage 0: no lane warning"
[[ "${log}" == *"WARN  release source: checkout"*"(DRY_RUN)"* ]] &&
  pass "stage 0: the source tag check is a WARN under DRY_RUN" || fail "stage 0: source check: ${log}"
for name in postgresql spire-crds temporal spire; do
  [[ "${log}" == *"PASS  release versions: ${name} "* ]] || fail "stage 0: ${name} version not checked"
done
pass "stage 0: chart versions compared with release.yaml"
[[ "${log}" == *"SKIP  release registries: NODE_EXEC is not set"* ]] &&
  pass "stage 0: registries skip without NODE_EXEC" || fail "stage 0: registries: ${log}"
[[ "${log}" != *"spiffe.github.io"* && "${log}" != *"go.temporal.io"* && "${log}" != *"registry-1.docker.io"* ]] &&
  pass "stage 0: chart repositories are no longer probed" || fail "stage 0: a chart repository is still probed"
# A version mismatch and a bad spec fail.
sed 's/version: "0.74.0"/version: "0.73.0"/' "${valid}" >"${WORK}/mismatch.yaml"
rc=0
log="$(cd "${WORK}" && OUT_DIR="${WORK}/out" SITE_KUBECONFIG="${WORK}/kubeconfig" DRY_RUN=1 \
  RELEASE_FILE="${WORK}/mismatch.yaml" "${DEPLOY_DIR}/stages/00-check-readiness.sh" check 2>&1)" || rc=$?
[[ "${rc}" -ge 1 && "${log}" == *"FAIL  release versions: temporal is 0.73.0 in the spec and 0.74.0"* ]] &&
  pass "stage 0: a chart version mismatch fails" || fail "stage 0: mismatch rc=${rc}: ${log}"
rc=0
log="$(cd "${WORK}" && OUT_DIR="${WORK}/out" SITE_KUBECONFIG="${WORK}/kubeconfig" DRY_RUN=1 \
  RELEASE_FILE="${FIXTURES}/bad-type.yaml" "${DEPLOY_DIR}/stages/00-check-readiness.sh" check 2>&1)" || rc=$?
[[ "${rc}" -ge 1 && "${log}" == *"FAIL  release spec: release_spec: "*"bad-type.yaml:12"* ]] &&
  pass "stage 0: a parser error is a FAIL with its message" || fail "stage 0: bad spec rc=${rc}: ${log}"

if [[ "${failures}" -eq 0 ]]; then
  printf '\nall release spec tests passed\n'
else
  printf '\n%d test(s) failed\n' "${failures}"
  exit 1
fi
