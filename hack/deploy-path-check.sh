#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Check that a deploy path is installable, self-contained and free of anything
# that only works inside NVIDIA.
#
# WHY THIS EXISTS. controller/deploy/tenant-runtime is maintained by hand today
# against the versions a GPU fleet is known to run. It may later be generated
# and delivered by whoever owns that runtime upstream, as a pull request or a
# published artifact. build/use-nke-sbom.sh reads a directory and cannot tell
# which of those produced it -- that indifference is what makes the source
# swappable, and it is also what makes an unreviewed change dangerous. This
# script is the contract that holds either way: it says what a tenant-runtime
# deploy path must be true of, so a machine-authored one can be gated on the
# same terms as a hand-written one.
#
# It is therefore also the specification to hand an upstream that offers to
# generate this. "Produce a directory that passes hack/deploy-path-check.sh" is
# a complete and testable statement of what we need.
#
# Deliberately scoped to one target rather than every deploy path in the
# repository. The site paths beside it install internal-only components on
# purpose and would fail checks 3 and 4 by design.
set -euo pipefail

SBOM="${SBOM:-controller/deploy}"
TARGET="${TARGET:-tenant-runtime}"
ONLINE=""

usage() {
  cat >&2 <<'USAGE'
usage: deploy-path-check.sh [--sbom DIR] [--target NAME] [--online]

  --sbom DIR      directory holding deploy paths (default: controller/deploy)
  --target NAME   deploy path inside it (default: tenant-runtime)
  --online        additionally resolve and render every chart against its
                  upstream repository. Needs network, so it is opt-in: the
                  offline checks must stay runnable on a laptop and in a
                  pipeline that has no egress.
USAGE
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --sbom) SBOM="$2"; shift 2 ;;
    --target) TARGET="$2"; shift 2 ;;
    --online) ONLINE=1; shift ;;
    -h | --help) usage ;;
    *) printf 'deploy-path-check: unknown argument: %s\n' "$1" >&2; usage ;;
  esac
done

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

ITEMS_PY="build/nke-sbom-items.py"
DEPLOY_PATH="${SBOM}/${TARGET}"
FAILED=0

pass() { printf '  ok    %s\n' "$*"; }
fail() { printf '  FAIL  %s\n' "$*"; FAILED=1; }
check() { printf '\n%s\n' "$*"; }

[[ -d "${DEPLOY_PATH}" ]] || { printf 'deploy-path-check: no such deploy path: %s\n' "${DEPLOY_PATH}" >&2; exit 1; }

printf 'deploy-path-check: %s\n' "${DEPLOY_PATH}"

# ---------------------------------------------------------------------------
check "1. the installer's own parser accepts it"
# Using nke-sbom-items.py rather than a second parser is the point: a check
# that reads the files differently from the installer can pass on something
# the installer then rejects.
if ! ITEMS="$(python3 "${ITEMS_PY}" items "${DEPLOY_PATH}/nke-deploy.yaml" 2>&1)"; then
  fail "nke-deploy.yaml does not parse: ${ITEMS}"
  ITEMS=""
else
  pass "nke-deploy.yaml parses, $(printf '%s\n' "${ITEMS}" | grep -c .) items"
fi

# ---------------------------------------------------------------------------
check "2. items and directories agree, in both directions"
# The installer only checks that every listed item has a directory. The
# reverse matters too: a directory nobody lists is content that ships in the
# repository and never installs, which is how a half-applied change hides.
declare -a HELM_ITEMS=()
LISTED=""
while IFS=$'\t' read -r name type; do
  [[ -n "${name}" ]] || continue
  LISTED="${LISTED} ${name}"
  if [[ -d "${DEPLOY_PATH}/${name}" ]]; then
    pass "item ${name} (${type}) has a directory"
  else
    fail "item ${name} (${type}) has no directory"
  fi
  [[ "${type}" == "helm" ]] && HELM_ITEMS+=("${name}")
done <<<"${ITEMS}"

for dir in "${DEPLOY_PATH}"/*/; do
  [[ -d "${dir}" ]] || continue
  base="$(basename "${dir}")"
  if [[ " ${LISTED} " == *" ${base} "* ]]; then continue; fi
  fail "directory ${base} is not listed in nke-deploy.yaml, so it never installs"
done

# ---------------------------------------------------------------------------
check "3. nothing in the tree is internal-only"
# Each of these has a specific failure attached to it, not just an optics
# concern. The node-group and dgxc label selectors are how a fleet pins these
# pods to its own node classes; NKE applies none of those labels, so they do
# not merely leak a name, they leave every pod in the chart unschedulable.
# imagePullSecrets means the path stopped being reproducible by someone
# outside NVIDIA, which is the property this whole lane is built to have.
declare -a MARKERS=(
  'gitlab-master'       # an internal git host; nothing here may depend on one
  'dgxc'                # fleet-specific node labels, repos and overlays
  'nodeGroup'           # fleet node-class selector, unschedulable here
  'isInternalVersion'   # marks an image built from an internal source
  'imagePullSecrets'    # this path installs from public registries only
  'jirasw'              # internal tracker host
)
for marker in "${MARKERS[@]}"; do
  if hits="$(grep -rIn -- "${marker}" "${DEPLOY_PATH}" 2>/dev/null)"; then
    fail "found ${marker}:"
    printf '        %s\n' "${hits}"
  else
    pass "no ${marker}"
  fi
done

# ---------------------------------------------------------------------------
check "4. every chart comes from a public repository, pinned to an exact version"
# The allowlist is short on purpose. Adding to it is a real decision -- it says
# a new party can now determine what a qualification run installs -- so it
# should show up in review as an edit to this list.
declare -a PUBLIC_REPOS=(
  'https://helm.ngc.nvidia.com/'
  'oci://registry-1.docker.io/'
)
for item in ${HELM_ITEMS[@]+"${HELM_ITEMS[@]}"}; do
  release_file="${DEPLOY_PATH}/${item}/release.yaml"
  if ! line="$(python3 "${ITEMS_PY}" release "${release_file}" 2>&1)"; then
    fail "${item}: release.yaml does not parse: ${line}"
    continue
  fi
  IFS=$'\x1f' read -r _ chart version repository path _ namespace _ _ <<<"${line}"

  if [[ -n "${path}" ]]; then
    pass "${item}: chart from a path inside the deploy path (${path})"
  else
    ok=""
    for prefix in "${PUBLIC_REPOS[@]}"; do
      [[ "${repository}" == "${prefix}"* ]] && ok=1
    done
    if [[ -n "${ok}" ]]; then
      pass "${item}: ${repository} is on the public allowlist"
    else
      fail "${item}: ${repository} is not an allowlisted public chart repository"
    fi
  fi

  # An exact version, not a range and not a moving tag. A qualification lane
  # whose dependencies move on their own cannot attribute a failure.
  if [[ "${version}" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.]+)?$ ]]; then
    pass "${item}: pinned to ${version}"
  else
    fail "${item}: version ${version:-<empty>} is not an exact pin"
  fi
done

# ---------------------------------------------------------------------------
check "5. each release namespace is created before the chart that needs it"
# install_helm_item passes --namespace but not --create-namespace, so a helm
# item whose namespace no earlier manifest item created fails on its first
# apply. That is a dry-run-invisible failure: the plan looks perfect.
declare -a CREATED=()
while IFS=$'\t' read -r name type; do
  [[ -n "${name}" ]] || continue
  case "${type}" in
    manifest | template)
      if ! found="$(python3 hack/deploy-path-namespaces.py "${DEPLOY_PATH}/${name}" 2>&1)"; then
        fail "${name}: could not read its manifests: ${found}"
        continue
      fi
      while IFS= read -r ns; do
        [[ -n "${ns}" ]] && CREATED+=("${ns}")
      done <<<"${found}"
      ;;
    helm)
      line="$(python3 "${ITEMS_PY}" release "${DEPLOY_PATH}/${name}/release.yaml")"
      IFS=$'\x1f' read -r _ _ _ _ _ _ namespace _ _ <<<"${line}"
      created=""
      for ns in ${CREATED[@]+"${CREATED[@]}"}; do
        [[ "${ns}" == "${namespace}" ]] && created=1
      done
      if [[ -n "${created}" ]]; then
        pass "${name}: namespace ${namespace} created by an earlier item"
      else
        fail "${name}: namespace ${namespace} is never created before this item"
      fi
      ;;
  esac
done <<<"${ITEMS}"

# ---------------------------------------------------------------------------
check "6. the install plan renders"
if plan="$(build/use-nke-sbom.sh install --sbom "${SBOM}" --target "${TARGET}" --dry-run 2>&1)"; then
  pass "dry-run produced $(printf '%s\n' "${plan}" | grep -cE '^(helm|kubectl) ') commands"
else
  fail "dry-run failed:"
  printf '        %s\n' "${plan}"
fi

# ---------------------------------------------------------------------------
if [[ -n "${ONLINE}" ]]; then
  check "7. every chart resolves and renders against its upstream repository"
  # Helm writes to a cache under \$HOME by default, which a hermetic runner may
  # not give us; point it somewhere disposable.
  scratch="$(mktemp -d)"
  trap 'rm -rf "${scratch}"' EXIT
  export HELM_CACHE_HOME="${scratch}/cache" HELM_CONFIG_HOME="${scratch}/config" HELM_DATA_HOME="${scratch}/data"

  for item in ${HELM_ITEMS[@]+"${HELM_ITEMS[@]}"}; do
    line="$(python3 "${ITEMS_PY}" release "${DEPLOY_PATH}/${item}/release.yaml")"
    IFS=$'\x1f' read -r _ chart version repository path release namespace values _ <<<"${line}"
    [[ -n "${repository}" ]] || { pass "${item}: local chart, nothing to resolve"; continue; }

    args=("${chart}" --repo "${repository}" --version "${version}")
    [[ "${repository}" == oci://* ]] && args=("${repository%/}/${chart}" --version "${version}")
    for file in ${values//,/ }; do
      args+=(--values "${DEPLOY_PATH}/${item}/${file}")
    done

    if out="$(helm template "${release}" "${args[@]}" --namespace "${namespace}" 2>&1)"; then
      pass "${item}: ${chart} ${version} renders $(printf '%s\n' "${out}" | grep -c '^kind:') objects"
    else
      fail "${item}: ${chart} ${version} did not render:"
      printf '        %s\n' "${out}" | head -20
    fi
  done
fi

printf '\n'
if [[ "${FAILED}" -ne 0 ]]; then
  printf 'deploy-path-check: FAILED\n'
  exit 1
fi
printf 'deploy-path-check: %s is installable and self-contained\n' "${DEPLOY_PATH}"
