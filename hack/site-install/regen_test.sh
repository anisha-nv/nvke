#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Golden test for regen.sh. Generates into a temp dir and compares with
# testdata/expected-05.sh, then runs the result in dry-run mode and checks that
# nothing raw survived normalisation.
#
# Usage: hack/site-install/regen_test.sh
#
# After an intended change: run regen.sh, then copy the new
# controller/deploy/nke-sbom/install/05-install-site-controller.sh over
# testdata/expected-05.sh.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${here}/../.." && pwd)"

fail=0
error() {
  printf 'regen_test: FAIL: %s\n' "$*" >&2
  fail=1
}

work="$(mktemp -d "${TMPDIR:-/tmp}/regen-test.XXXXXX")"
trap 'rm -rf "${work}"' EXIT

# 1. The generated stage matches the golden file.
"${here}/regen.sh" --out "${work}/out" 2>"${work}/regen.err" ||
  { cat "${work}/regen.err" >&2; error "regen.sh exited non-zero"; }
if ! diff -u "${here}/testdata/expected-05.sh" "${work}/out/05-install-site-controller.sh" \
  --label testdata/expected-05.sh --label generated/05-install-site-controller.sh; then
  error "generated stage differs from testdata/expected-05.sh"
fi

# 2. The guide gets the block between its markers, keeps a marker it cannot
# fill, and a second pass changes nothing.
cat >"${work}/guide.md" <<'EOF'
# Guide

<!-- stage:05-install-site-controller begin -->
stale
<!-- stage:05-install-site-controller end -->

<!-- stage:99-no-such-stage begin -->
kept
<!-- stage:99-no-such-stage end -->
EOF
"${here}/regen.sh" --out "${work}/doc1" --doc "${work}/guide.md" 2>"${work}/doc1.err" ||
  error "regen.sh --doc exited non-zero"
grep -q 'hl upgrade --install controller ' "${work}/doc1/guide.md" ||
  error "guide block does not hold the controller release"
grep -q '^```bash$' "${work}/doc1/guide.md" ||
  error "guide block is not fenced"
grep -q '^stale$' "${work}/doc1/guide.md" &&
  error "guide block was not replaced"
grep -q '^kept$' "${work}/doc1/guide.md" ||
  error "marker without a stage script was not preserved"
grep -q 'WARN: marker for 99-no-such-stage' "${work}/doc1.err" ||
  error "no warning for the marker without a stage script"
"${here}/regen.sh" --out "${work}/doc2" --doc "${work}/doc1/guide.md" 2>/dev/null ||
  error "second regen.sh --doc exited non-zero"
cmp -s "${work}/doc1/guide.md" "${work}/doc2/guide.md" ||
  error "guide extraction is not idempotent"

# 3. The published charts come from the release spec: one oras pull per item
# into its own directory, and no repository reference at install time.
for item in postgresql spire-crds temporal spire; do
  grep -qF "chart_dir=\"\${OUT_DIR}/charts/${item}\"" "${work}/out/05-install-site-controller.sh" ||
    error "generated stage does not pull ${item} into \${OUT_DIR}/charts/${item}"
done
if [[ "$(grep -cF 'run oras blob fetch $(oras_flags "${chart_ref}") --output "${chart_file}" "${chart_ref%%@*}@${chart_digest}"' "${work}/out/05-install-site-controller.sh")" -ne 4 ]]; then
  error "generated stage does not hold four oras blob fetch lines"
fi
grep -E 'hl upgrade --install .* (--repo |oci://)' "${work}/out/05-install-site-controller.sh" >/dev/null &&
  error "generated stage still installs a chart from a repository reference"
grep -q 'hl upgrade --install controller "${SBOM_DIR}/' "${work}/out/05-install-site-controller.sh" ||
  error "generated stage does not install the controller chart from the tree"
grep -q 'hl upgrade --install nktp "${SBOM_DIR}/' "${work}/out/05-install-site-controller.sh" ||
  error "generated stage does not install the nktp chart from the tree"

# 4. The stage runs in dry-run mode, prints argv only, and leaves no temp dir.
mkdir -p "${work}/install"
cp "${repo_root}/controller/deploy/nke-sbom/install/lib.sh" "${work}/install/"
cp "${repo_root}/controller/deploy/nke-sbom/install/release_spec.py" "${work}/install/"
# lib.sh needs a release spec; the example has the right shape.
release_file="${repo_root}/controller/deploy/nke-sbom/install/nke-release.example.yaml"
cp "${work}/out/05-install-site-controller.sh" "${work}/install/"
: >"${work}/kubeconfig"
if ! (cd "${work}" && DRY_RUN=1 SITE_KUBECONFIG="${work}/kubeconfig" OUT_DIR="${work}/site-out" \
  SBOM_DIR="${repo_root}/controller/deploy" TMPDIR="${work}" RELEASE_FILE="${release_file}" \
  ./install/05-install-site-controller.sh run >"${work}/dry.out" 2>"${work}/dry.err"); then
  cat "${work}/dry.err" >&2
  error "dry-run of the generated stage failed"
fi
grep -q '^helm --kubeconfig .* upgrade --install controller ' "${work}/dry.out" ||
  error "dry-run did not print the controller release"
grep -q '/dev/null' "${work}/dry.out" &&
  error "dry-run argv still names /dev/null"
if [[ "$(grep -c '^oras blob fetch --output .*ghcr.io/example/nke-recipes/[a-z-]*@sha256:' "${work}/dry.out")" -ne 4 ]]; then
  error "dry-run did not print four oras pull lines from the release spec"
fi
grep -E '^helm .* upgrade --install temporal [^ ]*/site-out/charts/temporal/temporal-0.74.0.tgz --namespace temporal ' "${work}/dry.out" >/dev/null ||
  error "dry-run does not install temporal from the pulled chart file"
grep -E '^helm .* upgrade --install postgres [^ ]*/site-out/charts/postgresql/postgresql-18.5.15.tgz --namespace postgres ' "${work}/dry.out" >/dev/null ||
  error "dry-run does not install postgres from the pulled chart file"
if [[ "$(grep -c '^SKIP  chart .*(DRY_RUN)$' "${work}/dry.out")" -ne 4 ]]; then
  error "dry-run did not skip the four digest checks with a SKIP line"
fi
# The nktp release takes ${OUT_DIR}/nktp.yaml only when stage 3 wrote it.
grep -E '^helm .* upgrade --install nktp .*--values [^ ]*nke-sbom/nktp/values.yaml *$' "${work}/dry.out" >/dev/null ||
  error "dry-run without nktp.yaml did not end the nktp release at the committed values"
mkdir -p "${work}/site-out"
printf 'otel-lgtm:\n  enabled: false\n' >"${work}/site-out/nktp.yaml"
if ! (cd "${work}" && DRY_RUN=1 SITE_KUBECONFIG="${work}/kubeconfig" OUT_DIR="${work}/site-out" \
  SBOM_DIR="${repo_root}/controller/deploy" TMPDIR="${work}" RELEASE_FILE="${release_file}" \
  ./install/05-install-site-controller.sh run >"${work}/dry2.out" 2>"${work}/dry2.err"); then
  cat "${work}/dry2.err" >&2
  error "dry-run of the generated stage with nktp.yaml failed"
fi
grep -E '^helm .* upgrade --install nktp .*--values [^ ]*nke-sbom/nktp/values.yaml --values [^ ]*/site-out/nktp.yaml *$' "${work}/dry2.out" >/dev/null ||
  error "dry-run with nktp.yaml did not append it as the last --values of the nktp release"
if ls -d "${work}"/nke-* >/dev/null 2>&1; then
  error "dry-run left temp dirs behind: $(ls -d "${work}"/nke-*)"
fi

if [[ "${fail}" -ne 0 ]]; then
  exit 1
fi
printf 'regen_test: PASS\n'
