#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Tests for nke-deploy.sh against deploy/nico and the example release
# manifest, in --dry-run: the item order, where each chart comes from, and the
# flags that select a chart source. Needs python3 only.
#
# Usage: ./test_nke_deploy.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "${HERE}/.." && pwd)"
EXECUTOR="${DEPLOY_DIR}/nke-deploy.sh"
NICO="${DEPLOY_DIR}/nico"
MANIFEST="${DEPLOY_DIR}/release-manifest.example.yaml"
CONFIG="${HERE}/bootstrap-config.json"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/nke-deploy-test.XXXXXX")"
trap 'rm -rf "${WORK}"' EXIT

failures=0
pass() { printf 'PASS  %s\n' "$*"; }
fail() {
  printf 'FAIL  %s\n' "$*"
  failures=$((failures + 1))
}
expect() {
  local what="$1" pattern="$2" text="$3"
  if grep -qE -- "${pattern}" <<<"${text}"; then pass "${what}"; else fail "${what}: no line matches ${pattern}"; fi
}
reject() {
  local what="$1" pattern="$2" text="$3"
  if grep -qE -- "${pattern}" <<<"${text}"; then fail "${what}: a line matches ${pattern}"; else pass "${what}"; fi
}

# Without a release manifest every chart comes from the checkout or its
# repository, in list order.
out="$(bash "${EXECUTOR}" install --deploy-path "${NICO}" --config "${CONFIG}" --dry-run 2>&1)" || fail "plain dry run exited $?"
order="$(grep -oE '^=== item [a-z-]+ \(' <<<"${out}" | sed -E 's/^=== item ([a-z-]+) \(/\1/' | tr '\n' ' ')"
if [[ "${order}" == "nke-base-config spire-tpm-pubhashes spire-server-nodeport spire-oidc-provider-nodeport capnico nktp postgresql spire-crds temporal spire controller " ]]; then
  pass "items apply in nke-deploy.yaml order"
else
  fail "item order: ${order}"
fi
expect "nktp from the checkout" 'helm upgrade --install nktp .*/nktp/deploy/charts/nktp ' "${out}"
expect "controller from the checkout" 'helm upgrade --install nke-controller .*/controller/deploy/charts/controller ' "${out}"
expect "controller in its release.yaml namespace" 'helm upgrade --install nke-controller .* --namespace nke-controller ' "${out}"
expect "postgresql from its OCI repository" 'helm upgrade --install postgres oci://registry-1.docker.io/bitnamicharts/postgresql --version 18.5.15 ' "${out}"
expect "spire from its index repository" 'helm upgrade --install spire spire --repo https://spiffe.github.io/helm-charts-hardened/ --version 0.30.1 ' "${out}"
reject "no oras without a manifest" '^oras ' "${out}"

# With the release manifest, the four external charts are pulled by digest
# into the chart cache and installed from there; the checkout charts are not.
cache="${WORK}/charts"
out="$(bash "${EXECUTOR}" install --deploy-path "${NICO}" --release-manifest "${MANIFEST}" --chart-cache "${cache}" --config "${CONFIG}" --dry-run 2>&1)" || fail "manifest dry run exited $?"
for item in postgresql spire-crds temporal spire; do
  expect "${item} chart pulled by digest" "^oras blob fetch --output ${cache}/${item}/${item}-[0-9.]+\\.tgz ghcr\\.io/example/nke-recipes/${item}@sha256:" "${out}"
done
expect "postgres installed from the cache" "helm upgrade --install postgres ${cache}/postgresql/postgresql-18.5.15.tgz " "${out}"
reject "controller image entry is not treated as a chart" "oras .*nke-recipes/cwe" "${out}"
expect "controller still from the checkout" 'helm upgrade --install nke-controller .*/controller/deploy/charts/controller ' "${out}"

# A chart override wins over the manifest.
out="$(bash "${EXECUTOR}" install --deploy-path "${NICO}" --release-manifest "${MANIFEST}" --chart-cache "${cache}" --config "${CONFIG}" --chart-override "temporal=${WORK}/temporal.tgz" --dry-run 2>&1)" || fail "override dry run exited $?"
expect "--chart-override beats the manifest" "helm upgrade --install temporal ${WORK}/temporal.tgz " "${out}"
reject "overridden chart is not pulled" "oras blob fetch .*/temporal/" "${out}"

# A manifest override replaces a manifest item's directory: a kustomization
# is built, plain files are applied.
mkdir -p "${WORK}/capnico"
printf 'resources: [capnico.yaml]\n' >"${WORK}/capnico/kustomization.yaml"
cp "${NICO}/capnico/capnico.yaml" "${WORK}/capnico/"
out="$(bash "${EXECUTOR}" install --deploy-path "${NICO}" --config "${CONFIG}" --manifest-override "capnico=${WORK}/capnico" --dry-run 2>&1)" || fail "manifest-override dry run exited $?"
expect "--manifest-override builds the kustomization" "^kubectl kustomize ${WORK}/capnico" "${out}"

# Errors name their cause.
out="$(bash "${EXECUTOR}" install --config "${CONFIG}" --dry-run 2>&1)" && fail "no --deploy-path exited 0"
expect "missing --deploy-path is named" 'deploy-path is required' "${out}"
out="$(bash "${EXECUTOR}" install --deploy-path "${NICO}" --release-manifest "${WORK}/absent.yaml" --dry-run 2>&1)" && fail "absent manifest exited 0"
expect "absent manifest is named" 'no such file' "${out}"
out="$(bash "${EXECUTOR}" install --deploy-path "${NICO}" --dry-run 2>&1)" && fail "template item without --config exited 0"
expect "template item needs --config" 'needs --config' "${out}"

if [[ "${failures}" -eq 0 ]]; then
  echo "all nke-deploy tests passed"
else
  echo "${failures} nke-deploy test(s) failed"
  exit 1
fi
