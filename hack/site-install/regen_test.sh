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

# 3. The stage runs in dry-run mode, prints argv only, and leaves no temp dir.
mkdir -p "${work}/install"
cp "${repo_root}/controller/deploy/nke-sbom/install/lib.sh" "${work}/install/"
cp "${work}/out/05-install-site-controller.sh" "${work}/install/"
: >"${work}/kubeconfig"
if ! (cd "${work}" && DRY_RUN=1 SITE_KUBECONFIG="${work}/kubeconfig" OUT_DIR="${work}/site-out" \
  SBOM_DIR="${repo_root}/controller/deploy" TMPDIR="${work}" \
  ./install/05-install-site-controller.sh run >"${work}/dry.out" 2>"${work}/dry.err"); then
  cat "${work}/dry.err" >&2
  error "dry-run of the generated stage failed"
fi
grep -q '^helm --kubeconfig .* upgrade --install controller ' "${work}/dry.out" ||
  error "dry-run did not print the controller release"
grep -q '/dev/null' "${work}/dry.out" &&
  error "dry-run argv still names /dev/null"
if ls -d "${work}"/nke-* >/dev/null 2>&1; then
  error "dry-run left temp dirs behind: $(ls -d "${work}"/nke-*)"
fi

if [[ "${fail}" -ne 0 ]]; then
  exit 1
fi
printf 'regen_test: PASS\n'
