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
DEPLOY_DIR="$(cd "${HERE}/.." && pwd)"
LIB_DIR="${DEPLOY_DIR}/lib"
DEPLOY_PATH="${DEPLOY_DIR}/nico"
REPO_ROOT="$(cd "${DEPLOY_DIR}/.." && pwd)"
RENDERER="${LIB_DIR}/describe_site.py"
SPEC="${HERE}/release-spec/valid.yaml"
TOOL_VERSIONS="${REPO_ROOT}/make/tool-versions.mk"
CAPI_VERSION="$(sed -n 's/^CAPI_VERSION[[:space:]]*?=[[:space:]]*//p' "${TOOL_VERSIONS}")"
CERT_MANAGER_VERSION="$(sed -n 's/^CERT_MANAGER_VERSION[[:space:]]*?=[[:space:]]*//p' "${TOOL_VERSIONS}")"

# images.txt needs helm and the network for the chart images. The tests stay
# offline: the static entries and the skip note are asserted instead.
export IMAGES_TXT_SKIP=1
# The extraction tests import describe_site; leave no __pycache__ behind.
export PYTHONDONTWRITEBYTECODE=1

WORK="$(mktemp -d "${TMPDIR:-/tmp}/describe-site-test.XXXXXX")"
trap 'rm -rf "${WORK}"' EXIT

failures=0
pass() { printf 'PASS  %s\n' "$*"; }
fail() {
  printf 'FAIL  %s\n' "$*"
  failures=$((failures + 1))
}

# A throwaway CA pair. The renderer checks PEM markers, not validity. The
# markers are assembled here so no file in the tree holds a key-shaped block
# for a secret scanner to flag.
write_ca() {
  local begin='-----BEGIN ' end='-----END ' tail=' KEY-----'
  printf -- '%sCERTIFICATE-----\nTEST\n%sCERTIFICATE-----\n' "${begin}" "${end}" >"$1/ca.crt"
  printf -- '%sPRIVATE%s\nTEST\n%sPRIVATE%s\n' "${begin}" "${tail}" "${end}" "${tail}" >"$1/ca.key"
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
    --out "${out}" --deploy-path "${DEPLOY_PATH}" 2>&1 >/dev/null)" || rc=$?
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
expect_error empty-nktp.yaml nktp

# A missing CA file is an error that names the key.
dir="${WORK}/missing-ca"
stage "${dir}" valid.yaml
rm "${dir}/ca.key"
rc=0
err="$(python3 "${RENDERER}" render --config "${dir}/site-config.yaml" \
  --out "${dir}/out" --deploy-path "${DEPLOY_PATH}" 2>&1 >/dev/null)" || rc=$?
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
log="$(cd "${dir}" && SITE_CONFIG="${dir}/site-config.yaml" OUT_DIR="${out}" DEPLOY_PATH="${DEPLOY_PATH}" \
  SITE_KUBECONFIG='' RELEASE_FILE="${SPEC}" "${DEPLOY_DIR}/stages/03-describe-site.sh" run 2>&1)" || rc=$?
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
if [[ "${log}" == *"images.controller from site config"* ]]; then
  pass "valid.yaml: summary says images.controller is from the site config"
else
  fail "valid.yaml: summary does not name the images.controller origin: ${log}"
fi
if [[ "${log}" == *"nico-token"* || "${log}" == *"ghp_example"* || "${log}" == *"tenant-pass"* ]]; then
  fail "valid.yaml: summary leaks a credential"
else
  pass "valid.yaml: summary is redacted"
fi

rc=0
log="$(cd "${dir}" && OUT_DIR="${out}" SITE_KUBECONFIG='' "${DEPLOY_DIR}/stages/03-describe-site.sh" check 2>&1)" || rc=$?
if [[ "${rc}" -eq 0 && "${log}" == *"SKIP  live database password"* ]]; then
  pass "valid.yaml: check exits 0 and skips the live password"
else
  fail "valid.yaml: check rc=${rc}: ${log}"
fi

for f in bootstrap-config.json controller.yaml spire.yaml postgresql.yaml temporal.yaml site.env db-password images.txt version-bom.cdx.json; do
  [[ -f "${out}/${f}" ]] || fail "valid.yaml: ${f} missing"
done
if [[ -e "${out}/nktp.yaml" ]]; then
  fail "valid.yaml: nktp.yaml written without an nktp block"
else
  pass "valid.yaml: no nktp.yaml without an nktp block"
fi
if [[ "${log}" == *"nktp.yaml absent"* ]]; then
  pass "valid.yaml: check reports nktp.yaml absent"
else
  fail "valid.yaml: check does not report nktp.yaml absent: ${log}"
fi
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
expect_line spire.yaml '^    - "spiffe://demo-site.nke.local/nke-service/nke-controller"$'
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
if grep -q 'digest:' "${out}/controller.yaml"; then
  fail "valid.yaml: images.controller is explicit, yet the spec digest was rendered"
else
  pass "valid.yaml: explicit images.controller wins over the spec"
fi
if grep -q 'imageRegistry\|image:' "${out}/postgresql.yaml" "${out}/temporal.yaml" || grep -q 'registry:' "${out}/spire.yaml"; then
  fail "valid.yaml: mirror keys rendered without registry.mirror"
else
  pass "valid.yaml: no mirror keys without registry.mirror"
fi
if [[ -e "${out}/capnico-kustomization.yaml" ]]; then
  fail "valid.yaml: capnico-kustomization.yaml written without registry.mirror"
else
  pass "valid.yaml: no capnico-kustomization.yaml without registry.mirror"
fi
expect_line controller.yaml '^    repository: "cwe-profiles"$'
expect_line controller.yaml '^    tag: "demo"$'
expect_line controller.yaml '^  discoveryURL: "https://192.0.2.10:30443/.well-known/openid-configuration"$'
expect_line controller.yaml '^    serverAddress: "192.0.2.10"$'
expect_line bootstrap-config.json '"ClusterName": "demo-site"'
expect_line bootstrap-config.json '"APIName": "forge"'
expect_line site.env '^SITE_NAME=demo-site$'
expect_line site.env '^SITE_VIP=192.0.2.10$'
expect_line site.env '^SITE_IMAGE_CONTROLLER=registry.example.test:5000/nke/nke-controller:v0.1.1$'
expect_line site.env '^SITE_IMAGE_PROFILES=cwe-profiles:demo$'
if [[ "$(grep -c . "${out}/site.env")" -eq 4 ]]; then
  pass "site.env holds four lines and no credential"
else
  fail "site.env holds $(grep -c . "${out}/site.env") lines, expected 4"
fi

# normalize_image: ctr needs fully qualified references; the kubelet does not.
norm() { (cd "${LIB_DIR}" && python3 -c 'import sys, describe_site; print(describe_site.normalize_image(sys.argv[1]))' "$1"); }
[[ "$(norm busybox)" == "docker.io/library/busybox:latest" ]] && pass "normalize: short library name" || fail "normalize: busybox -> $(norm busybox)"
[[ "$(norm busybox:1.37.0-uclibc)" == "docker.io/library/busybox:1.37.0-uclibc" ]] && pass "normalize: library name with a tag" || fail "normalize: busybox:tag -> $(norm busybox:1.37.0-uclibc)"
[[ "$(norm grafana/otel-lgtm:0.32.0)" == "docker.io/grafana/otel-lgtm:0.32.0" ]] && pass "normalize: short org name" || fail "normalize: grafana"
[[ "$(norm quay.io/prometheus/node-exporter:v1.12.1)" == "quay.io/prometheus/node-exporter:v1.12.1" ]] && pass "normalize: qualified unchanged" || fail "normalize: quay"
[[ "$(norm 'cgr.dev/chainguard/bash:latest@sha256:90041f375e30f41aa7e0390075d8a69dc61900771d52fa98d63ee5d03d866a58')" == 'cgr.dev/chainguard/bash:latest@sha256:90041f375e30f41aa7e0390075d8a69dc61900771d52fa98d63ee5d03d866a58' ]] && pass "normalize: digest ref unchanged" || fail "normalize: digest"
[[ "$(norm 'localhost:5000/x/y')" == "localhost:5000/x/y:latest" ]] && pass "normalize: port host gets :latest" || fail "normalize: localhost -> $(norm localhost:5000/x/y)"

# images.txt: the skip note and the static entries, read from the pins.
expect_line images.txt '^# skipped: helm template not run \(IMAGES_TXT_SKIP=1\)'
expect_line images.txt "^ghcr.io/dsx-ai-factory/cluster-api-provider-nico/controller:v[0-9.]+\$"
expect_line images.txt "^registry.k8s.io/cluster-api/cluster-api-controller:${CAPI_VERSION}\$"
expect_line images.txt "^registry.k8s.io/cluster-api/kubeadm-bootstrap-controller:${CAPI_VERSION}\$"
expect_line images.txt "^registry.k8s.io/cluster-api/kubeadm-control-plane-controller:${CAPI_VERSION}\$"
expect_line images.txt "^quay.io/jetstack/cert-manager-controller:${CERT_MANAGER_VERSION}\$"
expect_line images.txt "^quay.io/jetstack/cert-manager-cainjector:${CERT_MANAGER_VERSION}\$"
expect_line images.txt "^quay.io/jetstack/cert-manager-webhook:${CERT_MANAGER_VERSION}\$"
if [[ "$(grep -vc '^#' "${out}/images.txt")" -eq 7 ]]; then
  pass "images.txt holds the seven static entries under IMAGES_TXT_SKIP"
else
  fail "images.txt holds $(grep -vc '^#' "${out}/images.txt") entries, expected 7"
fi
if [[ "$(sort -u "${out}/images.txt" | wc -l | tr -d ' ')" -eq "$(wc -l <"${out}/images.txt" | tr -d ' ')" ]]; then
  pass "images.txt has no duplicate line"
else
  fail "images.txt has duplicate lines"
fi
if [[ "${log}" == *"WARN  images.txt has 7 images and a skip note"* ]]; then
  pass "check warns about the skip note"
else
  fail "check does not warn about the skip note: ${log}"
fi

# version-bom.cdx.json: CycloneDX 1.5, one component per spec item, digests as hashes.
if python3 - "${out}/version-bom.cdx.json" <<'EOF'
import json, sys
b = json.load(open(sys.argv[1]))
assert b["bomFormat"] == "CycloneDX" and b["specVersion"] == "1.5", "format"
root = b["metadata"]["component"]
assert root["type"] == "application" and root["name"] == "nico", "root"
assert root["version"] == "controller/v0.1.1", "root version"
props = {p["name"]: p["value"] for p in root["properties"]}
assert props["nke:lane"] == "local-assembly", "lane"
assert props["nke:controller-image"].endswith(":v0.1.1"), "controller image is the site config override"
c = {x["name"]: x for x in b["components"]}
assert set(c) == {"controller", "postgresql", "spire-crds", "temporal", "spire", "nkectl"}, set(c)
assert c["controller"]["type"] == "container" and c["controller"]["hashes"][0]["content"] == "a" * 64, "controller"
assert c["postgresql"]["type"] == "application" and c["postgresql"]["version"] == "18.5.15", "postgresql"
assert c["postgresql"]["hashes"] == [{"alg": "SHA-256", "content": "0" * 64}], "hash"
assert {p["name"]: p["value"] for p in c["temporal"]["properties"]}["nke:deploy-item"] == "temporal", "property"
assert "version" not in c["nkectl"], "binary items carry no version"
EOF
then
  pass "version-bom.cdx.json is CycloneDX 1.5 with the six spec items"
else
  fail "version-bom.cdx.json does not match the spec items"
fi

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
log="$(cd "${dir}" && SITE_CONFIG="${dir}/site-config.yaml" OUT_DIR="${out}" DEPLOY_PATH="${DEPLOY_PATH}" \
  SITE_KUBECONFIG='' "${DEPLOY_DIR}/stages/03-describe-site.sh" run 2>&1)" || rc=$?
if [[ "${rc}" -eq 0 && "${log}" == *"reusing"* && "$(cat "${out}/db-password")" == "${password}" ]]; then
  pass "rerun reuses the password"
else
  fail "rerun rc=${rc}, password reused: $([[ "$(cat "${out}/db-password")" == "${password}" ]] && echo yes || echo no): ${log}"
fi

# amd64 selects the other checksum.
dir="${WORK}/amd64"
stage "${dir}" valid.yaml
sed -i.bak 's/arch: arm64/arch: amd64/' "${dir}/site-config.yaml"
python3 "${RENDERER}" render --config "${dir}/site-config.yaml" --out "${dir}/out" --deploy-path "${DEPLOY_PATH}" >/dev/null 2>&1
if grep -q '46d0caad8c25a027dd11c93e18b58a8bc6fbd9f1fe2e36fa2a0dd440986de4dc' "${dir}/out/spire.yaml"; then
  pass "amd64 selects the amd64 checksum"
else
  fail "amd64 checksum not selected"
fi

# The optional nktp block is passed through as written, dedented, mode 0600.
dir="${WORK}/with-nktp"
stage "${dir}" with-nktp.yaml
out="${dir}/out"
rc=0
log="$(python3 "${RENDERER}" render --config "${dir}/site-config.yaml" --out "${out}" --deploy-path "${DEPLOY_PATH}" 2>&1)" || rc=$?
if [[ "${rc}" -eq 0 && -f "${out}/nktp.yaml" ]]; then
  pass "with-nktp.yaml: render exits 0 and writes nktp.yaml"
else
  fail "with-nktp.yaml: rc=${rc}, nktp.yaml present: $([[ -f "${out}/nktp.yaml" ]] && echo yes || echo no): ${log}"
fi
if [[ "${log}" == *"nktp-backend-token"* ]]; then
  fail "with-nktp.yaml: summary leaks the nktp block"
else
  pass "with-nktp.yaml: summary names top-level keys only"
fi
mode="$(stat -f '%Lp' "${out}/nktp.yaml" 2>/dev/null || stat -c '%a' "${out}/nktp.yaml")"
[[ "${mode}" == "600" ]] && pass "nktp.yaml is 0600" || fail "nktp.yaml mode is ${mode}"
expect_line nktp.yaml '^otel-lgtm:$'
expect_line nktp.yaml '^  enabled: false$'
expect_line nktp.yaml '^    headers:$'
expect_line nktp.yaml '^      Authorization: "Bearer nktp-backend-token"$'
expect_line nktp.yaml '^      ca_pem_files: \[ca.pem\]$'
expect_line nktp.yaml '^# Point the exporters'
if [[ "$(head -c 1 "${out}/nktp.yaml")" != " " ]]; then
  pass "nktp.yaml is dedented"
else
  fail "nktp.yaml keeps the section indentation"
fi
rc=0
log="$(python3 "${RENDERER}" check --out "${out}" 2>&1)" || rc=$?
if [[ "${rc}" -eq 0 && "${log}" == *"nktp.yaml has keys otel-lgtm, exporters"* ]]; then
  pass "with-nktp.yaml: check lists the nktp.yaml keys"
else
  fail "with-nktp.yaml: check rc=${rc}: ${log}"
fi
if python3 "${RENDERER}" keys --config "${dir}/site-config.yaml" --deploy-path "${DEPLOY_PATH}" | grep -qx 'nktp.yaml exporters'; then
  pass "with-nktp.yaml: keys lists nktp.yaml top-level keys"
else
  fail "with-nktp.yaml: keys does not list nktp.yaml exporters"
fi
# Dropping the block removes the file on the next render.
cp "${HERE}/valid.yaml" "${dir}/site-config.yaml"
rc=0
log="$(python3 "${RENDERER}" render --config "${dir}/site-config.yaml" --out "${out}" --deploy-path "${DEPLOY_PATH}" 2>&1)" || rc=$?
if [[ "${rc}" -eq 0 && ! -e "${out}/nktp.yaml" && "${log}" == *"nktp.yaml removed"* ]]; then
  pass "rerun without the nktp block removes nktp.yaml"
else
  fail "rerun without the nktp block: rc=${rc}, nktp.yaml present: $([[ -e "${out}/nktp.yaml" ]] && echo yes || echo no): ${log}"
fi

# images.controller absent: the controller item's ref is the default, rendered
# as repository plus digest, and the summary says so.
dir="${WORK}/no-controller"
stage "${dir}" no-controller.yaml
out="${dir}/out"
rc=0
log="$(cd "${dir}" && SITE_CONFIG="${dir}/site-config.yaml" OUT_DIR="${out}" DEPLOY_PATH="${DEPLOY_PATH}" \
  SITE_KUBECONFIG='' RELEASE_FILE="${SPEC}" "${DEPLOY_DIR}/stages/03-describe-site.sh" run 2>&1)" || rc=$?
if [[ "${rc}" -eq 0 && "${log}" == *"images.controller from the release spec"* ]]; then
  pass "no-controller.yaml: controller image defaults to the spec ref"
else
  fail "no-controller.yaml: rc=${rc}: ${log}"
fi
expect_line controller.yaml '^  repository: "registry.example.test:5000/nke/cwe"$'
expect_line controller.yaml '^  digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"$'
if grep -q '^  tag:' "${out}/controller.yaml"; then
  fail "no-controller.yaml: a digest ref rendered a tag"
else
  pass "no-controller.yaml: digest ref renders repository and digest only"
fi
expect_line site.env '^SITE_IMAGE_CONTROLLER=registry.example.test:5000/nke/cwe@sha256:a{64}$'
if python3 -c 'import json, sys; b = json.load(open(sys.argv[1])); p = {x["name"]: x["value"] for x in b["metadata"]["component"]["properties"]}; sys.exit(0 if p["nke:controller-image"].endswith("@sha256:" + "a" * 64) else 1)' "${out}/version-bom.cdx.json"; then
  pass "no-controller.yaml: BOM records the spec controller image"
else
  fail "no-controller.yaml: BOM controller image is not the spec ref"
fi
rc=0
log="$(cd "${dir}" && OUT_DIR="${out}" SITE_KUBECONFIG='' "${DEPLOY_DIR}/stages/03-describe-site.sh" check 2>&1)" || rc=$?
[[ "${rc}" -eq 0 ]] && pass "no-controller.yaml: check exits 0" || fail "no-controller.yaml: check rc=${rc}: ${log}"

# Neither images.controller nor a release spec: an error naming both.
rc=0
err="$(cd "${dir}" && SITE_CONFIG="${dir}/site-config.yaml" OUT_DIR="${dir}/out2" DEPLOY_PATH="${DEPLOY_PATH}" \
  SITE_KUBECONFIG='' RELEASE_FILE='' RELEASE_REF='' "${DEPLOY_DIR}/stages/03-describe-site.sh" run 2>&1 >/dev/null)" || rc=$?
if [[ "${rc}" -eq 1 && "${err}" == *"images.controller is missing"* && "${err}" == *"RELEASE_CONTROLLER_REF"* ]]; then
  pass "no-controller.yaml without a spec: exit 1, names images.controller and RELEASE_CONTROLLER_REF"
else
  fail "no-controller.yaml without a spec: rc=${rc}: ${err}"
fi

# Without a spec and with an explicit controller: no BOM, a note, check warns.
dir="${WORK}/no-spec"
stage "${dir}" valid.yaml
out="${dir}/out"
rc=0
log="$(cd "${dir}" && SITE_CONFIG="${dir}/site-config.yaml" OUT_DIR="${out}" DEPLOY_PATH="${DEPLOY_PATH}" \
  SITE_KUBECONFIG='' RELEASE_FILE='' RELEASE_REF='' "${DEPLOY_DIR}/stages/03-describe-site.sh" run 2>&1)" || rc=$?
if [[ "${rc}" -eq 0 && ! -e "${out}/version-bom.cdx.json" && "${log}" == *"version-bom.cdx.json not written: no release spec"* ]]; then
  pass "valid.yaml without a spec: renders, no BOM, says why"
else
  fail "valid.yaml without a spec: rc=${rc}, BOM present: $([[ -e "${out}/version-bom.cdx.json" ]] && echo yes || echo no): ${log}"
fi

# registry.mirror: the chart keys that repoint images, the CAPNICo override,
# the mirrored capnico line in images.txt; dropping the key removes the override.
dir="${WORK}/with-mirror"
stage "${dir}" with-mirror.yaml
out="${dir}/out"
rc=0
log="$(cd "${dir}" && SITE_CONFIG="${dir}/site-config.yaml" OUT_DIR="${out}" DEPLOY_PATH="${DEPLOY_PATH}" \
  SITE_KUBECONFIG='' RELEASE_FILE="${SPEC}" "${DEPLOY_DIR}/stages/03-describe-site.sh" run 2>&1)" || rc=$?
[[ "${rc}" -eq 0 ]] && pass "with-mirror.yaml: run exits 0" || fail "with-mirror.yaml: rc=${rc}: ${log}"
expect_line postgresql.yaml '^  imageRegistry: "registry.site.example:5000"$'
expect_line postgresql.yaml '^    allowInsecureImages: true$'
expect_line temporal.yaml '^    repository: "registry.site.example:5000/temporalio/server"$'
expect_line temporal.yaml '^    repository: "registry.site.example:5000/temporalio/admin-tools"$'
expect_line temporal.yaml '^    repository: "registry.site.example:5000/temporalio/ui"$'
expect_line spire.yaml '^      registry: "registry.site.example:5000"$'
expect_line capnico-kustomization.yaml '^  - name: "ghcr.io/dsx-ai-factory/cluster-api-provider-nico/controller"$'
expect_line capnico-kustomization.yaml '^    newName: "registry.site.example:5000/dsx-ai-factory/cluster-api-provider-nico/controller"$'
expect_line capnico-kustomization.yaml '^    newTag: "v[0-9.]+"$'
expect_line images.txt '^registry.site.example:5000/dsx-ai-factory/cluster-api-provider-nico/controller:v[0-9.]+$'
expect_line images.txt "^registry.k8s.io/cluster-api/cluster-api-controller:${CAPI_VERSION}\$"
expect_line images.txt '^# registry.mirror repoints'
if grep -q '^ghcr.io/dsx-ai-factory' "${out}/images.txt"; then
  fail "with-mirror.yaml: images.txt keeps the upstream capnico image"
else
  pass "with-mirror.yaml: images.txt lists the mirrored capnico image only"
fi
mode="$(stat -f '%Lp' "${out}/capnico-kustomization.yaml" 2>/dev/null || stat -c '%a' "${out}/capnico-kustomization.yaml")"
[[ "${mode}" == "600" ]] && pass "capnico-kustomization.yaml is 0600" || fail "capnico-kustomization.yaml mode is ${mode}"
rc=0
log="$(cd "${dir}" && OUT_DIR="${out}" SITE_KUBECONFIG='' "${DEPLOY_DIR}/stages/03-describe-site.sh" check 2>&1)" || rc=$?
if [[ "${rc}" -eq 0 && "${log}" == *"capnico-kustomization.yaml has keys resources, images"* ]]; then
  pass "with-mirror.yaml: check lists the kustomization keys"
else
  fail "with-mirror.yaml: check rc=${rc}: ${log}"
fi
cp "${HERE}/valid.yaml" "${dir}/site-config.yaml"
rc=0
log="$(cd "${dir}" && SITE_CONFIG="${dir}/site-config.yaml" OUT_DIR="${out}" DEPLOY_PATH="${DEPLOY_PATH}" \
  SITE_KUBECONFIG='' RELEASE_FILE="${SPEC}" "${DEPLOY_DIR}/stages/03-describe-site.sh" run 2>&1)" || rc=$?
if [[ "${rc}" -eq 0 && ! -e "${out}/capnico-kustomization.yaml" && "${log}" == *"capnico-kustomization.yaml removed"* ]]; then
  pass "rerun without registry.mirror removes capnico-kustomization.yaml"
else
  fail "rerun without registry.mirror: rc=${rc}, file present: $([[ -e "${out}/capnico-kustomization.yaml" ]] && echo yes || echo no): ${log}"
fi

# A mirror with a scheme or a trailing slash is refused by name.
for bad in 'https://registry.site.example:5000' 'registry.site.example:5000/'; do
  sed "s|mirror: registry.site.example:5000|mirror: ${bad}|" "${HERE}/with-mirror.yaml" >"${dir}/site-config.yaml"
  rc=0
  err="$(RELEASE_CONTROLLER_REF="registry.example.test:5000/nke/cwe@sha256:$(printf 'a%.0s' $(seq 1 64))" \
    python3 "${RENDERER}" render --config "${dir}/site-config.yaml" --out "${dir}/out3" --deploy-path "${DEPLOY_PATH}" 2>&1 >/dev/null)" || rc=$?
  if [[ "${rc}" -eq 1 && "${err}" == *registry.mirror* ]]; then
    pass "registry.mirror ${bad}: exit 1, names the key"
  else
    fail "registry.mirror ${bad}: rc=${rc}: ${err}"
  fi
done

# Image extraction from rendered manifests: image: fields, list items, flags,
# duplicates folded, prose ignored.
expected='busybox:1.37.0-uclibc
cgr.dev/chainguard/bash:latest@sha256:90041f375e30f41aa7e0390075d8a69dc61900771d52fa98d63ee5d03d866a58
ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-k8s:0.127.0
ghcr.io/open-telemetry/opentelemetry-operator/opentelemetry-operator:0.127.0
ghcr.io/open-telemetry/opentelemetry-operator/target-allocator:0.127.0
ghcr.io/spiffe/spire-agent:1.15.3
registry-1.docker.io/bitnami/postgresql@sha256:b74b23f091dcc87041bcae557d840c36458af46b955279cb13aedec4c32d7f71
temporalio/server:1.30.3'
got="$(cd "${LIB_DIR}" && python3 -c 'import sys, describe_site; print("\n".join(describe_site.extract_images(open(sys.argv[1]).read())))' "${HERE}/rendered-images.yaml")"
if [[ "${got}" == "${expected}" ]]; then
  pass "extract_images finds every image shape in rendered-images.yaml"
else
  fail "extract_images: $(diff <(printf '%s\n' "${expected}") <(printf '%s\n' "${got}") || true)"
fi

# mirror_ref keeps the upstream path under the mirror; a short name has no registry to drop.
got="$(cd "${LIB_DIR}" && python3 -c 'import describe_site as d; m = "r.example:5000"; print(d.mirror_ref("busybox:1.37", m), d.mirror_ref("temporalio/server:1.30.3", m), d.mirror_ref("registry-1.docker.io/bitnami/postgresql@sha256:" + "0" * 64, m), d.mirror_ref("localhost:5000/x/y:1", m))')"
if [[ "${got}" == "r.example:5000/busybox:1.37 r.example:5000/temporalio/server:1.30.3 r.example:5000/bitnami/postgresql@sha256:$(printf '0%.0s' $(seq 1 64)) r.example:5000/x/y:1" ]]; then
  pass "mirror_ref keeps the upstream path under the mirror"
else
  fail "mirror_ref: ${got}"
fi

# Golden: the key set each file carries.
dir="${WORK}/golden"
stage "${dir}" valid.yaml
if python3 "${RENDERER}" keys --config "${dir}/site-config.yaml" --deploy-path "${DEPLOY_PATH}" \
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
