#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Generate the site controller install stage from the deploy path, and refresh
# the command blocks the install guide extracts from the stage scripts.
#
# Usage: hack/site-install/regen.sh [--check | --out DIR] [--doc PATH]
#
#   (no flag)   write controller/deploy/nke-sbom/install/05-install-site-controller.sh
#               and refresh docs/site-bootstrap/install-site-controller.md
#   --check     generate into a temp dir and diff against the committed files.
#               Exit 1 on any difference. CI calls this.
#   --out DIR   write 05-install-site-controller.sh and install-site-controller.md
#               into DIR and leave the repository alone. Tests call this.
#   --doc PATH  read the guide from PATH instead of docs/site-bootstrap. Tests.
#
# Contributor tool. A customer never runs it.
#
# Source of truth: `build/use-nke-sbom.sh --dry-run` over controller/deploy/nke-sbom,
# rendered against fixture-bootstrap-config.json beside this script. The dry-run
# prints the exact helm and kubectl argv of the deploy path in list order. This
# script rewrites that argv into stage form:
#
#   kubectl --kubeconfig /dev/null ...      -> kc ...          (lib.sh: kubectl --kubeconfig "${SITE_KUBECONFIG}")
#   helm ... --kubeconfig /dev/null ...     -> hl ...          (lib.sh: helm --kubeconfig "${SITE_KUBECONFIG}")
#   helm --timeout 15m                      -> --timeout "${PHASE_TIMEOUT:-15m}"
#   kubectl wait --timeout=180s             -> --timeout="${REQUIRE_TIMEOUT:-180s}"
#   controller/deploy/...                   -> "${SBOM_DIR}/..."
#   HELM_REPOSITORY_*=<mktemp>/...          -> "${HELM_TMP}/...", created once, removed by a trap
#   template item apply lines               -> render at apply time into ${RENDER_TMP}, apply, remove
#   helm items deploy-sbom.sh gives --values-for -> --values "${OUT_DIR}/<item>.yaml" appended after
#                                              the item's own values files, so the site file wins
#   === headers                             -> comments, so the script reads as the item list
#
# Added before the items: the stage 2 image check, a helm version warning.
# Added before a `requires:` wait: the executor's existence pre-check.
# Added to the capnico item: an optional CAPNICO_IMAGE kustomize override.
#
# Every line of the result must be free of /dev/null, the temp dir and the raw
# --sbom path. The script fails if one survives, so a new argv shape in the
# executor shows up here before it reaches a customer.
#
# The guide marks where each stage's block lands:
#
#   <!-- stage:NN-name begin -->
#   <!-- stage:NN-name end -->
#
# Everything between the markers is replaced with a fenced bash block holding
# the lines between `# doc-begin` and `# doc-end` of NN-name.sh. A stage with no
# markers is reported and skipped. A marker with no stage is left alone.
#
# No `mapfile`: bash 3.2 on macOS leaves the array empty and exits 0.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${repo_root}"

here="hack/site-install"
fixture="${here}/fixture-bootstrap-config.json"
executor="build/use-nke-sbom.sh"
sbom_arg="controller/deploy"
target="nke-sbom"
install_dir="controller/deploy/nke-sbom/install"
stage_file="05-install-site-controller.sh"
doc_default="docs/site-bootstrap/install-site-controller.md"
capnico_manifest="${sbom_arg}/${target}/capnico/capnico.yaml"
controller_overlay="${sbom_arg}/${target}/controller/overlay.yaml"
template_dir="${sbom_arg}/${target}/nke-base-config"
worker_main="controller/cmd/worker/main.go"
deploy_sbom="controller/hack/ci/deploy-sbom.sh"
describe_site="${install_dir}/describe_site.py"
crd_warning="NicoMachine CRD not installed"
year="2026"

mode="write"
out_dir=""
doc_src="${doc_default}"

usage() {
  sed -n '/^# Usage:/,/^#$/p' "$0" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
  --check) mode="check"; shift ;;
  --out)
    [[ $# -ge 2 ]] || usage
    mode="out"
    out_dir="$2"
    shift 2
    ;;
  --doc)
    [[ $# -ge 2 ]] || usage
    doc_src="$2"
    shift 2
    ;;
  -h | --help) usage ;;
  *) usage ;;
  esac
done

die() {
  printf 'regen: %s\n' "$*" >&2
  exit 1
}
warn() { printf 'regen: WARN: %s\n' "$*" >&2; }

[[ -f "${fixture}" ]] || die "fixture not found: ${fixture}"
[[ -f "${executor}" ]] || die "executor not found: ${executor}"
[[ -f "${doc_src}" ]] || die "guide not found: ${doc_src}"

# macOS mktemp ignores TMPDIR, so name the directory in full.
work="$(mktemp -d "${TMPDIR:-/tmp}/regen.XXXXXX")"
trap 'rm -rf "${work}"' EXIT
mkdir -p "${work}/dry" "${work}/bin" "${work}/gen" "${work}/install" "${work}/blocks"

# ---------------------------------------------------------------------------
# 1. Run the executor's dry-run against the fixture.
# ---------------------------------------------------------------------------

# The executor renders the template item for real and calls `mktemp -d` twice.
# Route both into the work dir so the temp base is known and can be rewritten.
real_mktemp="$(command -v mktemp)"
cat >"${work}/bin/mktemp" <<EOF
#!/usr/bin/env bash
if [[ "\$#" -eq 1 && "\$1" == "-d" ]]; then
  exec "${real_mktemp}" -d "${work}/dry/tmp.XXXXXX"
fi
exec "${real_mktemp}" "\$@"
EOF
chmod 0755 "${work}/bin/mktemp"

raw="${work}/raw.txt"
# The executor logs headers to stderr and argv to stdout. Both are unbuffered
# and the dry-run is sequential, so one stream keeps them in order.
if ! PATH="${work}/bin:${PATH}" bash "${executor}" install \
  --sbom "${sbom_arg}" --target "${target}" --dry-run \
  --config "${fixture}" --kubeconfig /dev/null >"${raw}" 2>&1; then
  cat "${raw}" >&2
  die "dry-run failed"
fi

tmp_base="${work}/dry"

# ---------------------------------------------------------------------------
# 2. Facts the checks are generated from. Read from the tree, never typed.
# ---------------------------------------------------------------------------

crds="$(grep -E '^  name: [a-z]+\.infrastructure\.cluster\.x-k8s\.io$' "${capnico_manifest}" | awk '{print $2}')"
[[ -n "${crds}" ]] || die "no CRD names found in ${capnico_manifest}"

read -r capnico_deploy capnico_ns < <(awk '
  /^---/ { kind = ""; name = ""; ns = ""; inmeta = 0; next }
  /^kind: / { kind = $2 }
  /^metadata:/ { inmeta = 1; next }
  inmeta && /^  name: / { name = $2 }
  inmeta && /^  namespace: / { ns = $2 }
  inmeta && /^[^ ]/ { inmeta = 0 }
  kind == "Deployment" && name != "" && ns != "" { print name, ns; exit }
' "${capnico_manifest}")
[[ -n "${capnico_deploy:-}" && -n "${capnico_ns:-}" ]] || die "no Deployment with a namespace in ${capnico_manifest}"

capnico_image="$(awk '/^ *image: / { print $2; exit }' "${capnico_manifest}")"
[[ -n "${capnico_image}" ]] || die "no image: line in ${capnico_manifest}"

secret_ref() {
  local file="$1" name ns
  name="$(awk '/^  name: / { print $2; exit }' "${file}")"
  ns="$(awk '/^  namespace: / { print $2; exit }' "${file}")"
  [[ -n "${name}" && -n "${ns}" ]] || die "no name/namespace in ${file}"
  printf '%s %s\n' "${name}" "${ns}"
}
secrets=""
for tmpl in "${template_dir}"/*.yaml.tmpl; do
  grep -q '^kind: Secret$' "${tmpl}" || continue
  secrets="${secrets}$(secret_ref "${tmpl}")"$'\n'
done
[[ -n "${secrets}" ]] || die "no Secret templates in ${template_dir}"

cwe_deploy="$(awk '/^fullnameOverride: / { print $2; exit }' "${controller_overlay}")"
[[ -n "${cwe_deploy}" ]] || die "no fullnameOverride in ${controller_overlay}"

grep -qF "${crd_warning}" "${worker_main}" ||
  die "warning text '${crd_warning}' not found in ${worker_main}; update crd_warning"

# The helm items that take a site values file. deploy-sbom.sh names them with
# --values-for ITEM=...; stage 3 writes ${OUT_DIR}/ITEM.yaml for each.
site_values_items="$(sed -n 's/^[[:space:]]*--values-for "\([a-z]*\)=.*/\1/p' "${deploy_sbom}")"
[[ -n "${site_values_items}" ]] || die "no --values-for items in ${deploy_sbom}"
for item in ${site_values_items}; do
  grep -qF "\"${item}.yaml\"" "${describe_site}" ||
    die "${deploy_sbom} passes --values-for ${item}= but ${describe_site} does not write ${item}.yaml"
done

# site_values_arg prints the --values flag for an item, or nothing.
site_values_arg() {
  local candidate
  for candidate in ${site_values_items}; do
    if [[ "${candidate}" == "$1" ]]; then
      printf -- ' --values "${OUT_DIR}/%s.yaml"' "$1"
      return 0
    fi
  done
}

# ---------------------------------------------------------------------------
# 3. Normalise the dry-run into the stage_run body.
# ---------------------------------------------------------------------------

# paths rewrites the --sbom prefix. quoted=1 wraps the token for a command
# line; quoted=0 leaves it bare for a comment.
paths() {
  local line="$1" quoted="$2" out="" tok
  for tok in ${line}; do
    if [[ "${tok}" == "${sbom_arg}/"* ]]; then
      if [[ "${quoted}" -eq 1 ]]; then
        tok="\"\${SBOM_DIR}/${tok#"${sbom_arg}/"}\""
      else
        tok="\${SBOM_DIR}/${tok#"${sbom_arg}/"}"
      fi
    fi
    out="${out}${out:+ }${tok}"
  done
  printf '%s\n' "${out}"
}

# Replacement text holds braces, so it cannot sit inside ${var/pat/repl}.
phase_timeout='--timeout "${PHASE_TIMEOUT:-15m}"'
require_timeout='--timeout="${REQUIRE_TIMEOUT:-180s}"'

body="${work}/body.sh"
: >"${body}"
emit() { printf '%s\n' "$*" >>"${body}"; }

items=0
item=""
item_type=""
in_template=0
releases=""
cwe_ns=""
pending_blank=0

flush_blank() {
  if [[ "${pending_blank}" -eq 1 ]]; then
    emit ""
    pending_blank=0
  fi
}

while IFS= read -r line; do
  line="${line%"${line##*[![:space:]]}"}"
  if [[ -z "${line}" ]]; then
    [[ "${items}" -eq 0 ]] || pending_blank=1
    continue
  fi

  if [[ "${line}" == "=== item "* ]]; then
    rest="${line#=== item }"
    if [[ "${rest}" == *" -> release "* ]]; then
      # === item X -> release R (chart ver) in NS
      rel="${rest#* -> release }"
      rel_name="${rel%% *}"
      rel_ns="${rel##* in }"
      releases="${releases}${rel_name} ${rel_ns}"$'\n'
      [[ "${rest%% *}" != "controller" ]] || cwe_ns="${rel_ns}"
      in_template=0
      flush_blank
      emit "  # ${rest}"
      continue
    fi
    items=$((items + 1))
    item="${rest%% *}"
    item_type="${rest##* (}"
    item_type="${item_type%)}"
    flush_blank
    emit "  # item ${item} (${item_type})"
    case "${item_type}" in
    template)
      in_template=1
      emit "  RENDER_TMP=\"\$(mktemp -d \"\${TMPDIR:-/tmp}/nke-render.XXXXXX\")\""
      emit "  run python3 \"\${SBOM_DIR}/../../build/render-template.py\" \"\${OUT_DIR}/bootstrap-config.json\" \"\${SBOM_DIR}/${target}/${item}\" \"\${RENDER_TMP}\""
      emit "  for file in \"\${RENDER_TMP}\"/*.yaml; do"
      emit "    kc apply --server-side -f \"\${file}\""
      emit "  done"
      emit "  rm -rf \"\${RENDER_TMP}\""
      ;;
    manifest | helm) in_template=0 ;;
    *) die "item ${item} has type ${item_type}, which this generator does not handle" ;;
    esac
    continue
  fi

  if [[ "${line}" == "=== "* ]]; then
    in_template=0
    flush_blank
    emit "  # $(paths "${line#=== }" 0)"
    continue
  fi

  if [[ "${in_template}" -eq 1 ]]; then
    [[ "${line}" == "kubectl --kubeconfig /dev/null apply --server-side -f ${tmp_base}/"*.yaml ]] ||
      die "unexpected line under template item ${item}: ${line}"
    continue
  fi

  flush_blank
  case "${line}" in
  "kubectl --kubeconfig /dev/null "*wait*certificate/*)
    # The executor checks the object exists before waiting. Keep that.
    rest="${line#kubectl --kubeconfig /dev/null }"
    ns=""
    obj=""
    prev=""
    for tok in ${rest}; do
      [[ "${prev}" != "-n" ]] || ns="${tok}"
      [[ "${tok}" != */* || "${tok}" == -* ]] || obj="${tok}"
      prev="${tok}"
    done
    [[ -n "${ns}" && -n "${obj}" ]] || die "cannot parse wait line: ${line}"
    emit "  kc -n ${ns} get ${obj} >/dev/null 2>&1 ||"
    emit "    die \"phase ${item} requires ${obj} in namespace ${ns}, which does not exist."
    emit "It is not part of this chart -- an earlier item supplies it. In this repository"
    emit "$(paths "that is the nke-base-config item, from ${sbom_arg}/k8s/oidc-upstream-ca.yaml." 0)\""
    rest="${rest/--timeout=180s/${require_timeout}}"
    emit "  kc ${rest}"
    ;;
  "kubectl --kubeconfig /dev/null "*)
    rest="$(paths "${line#kubectl --kubeconfig /dev/null }" 1)"
    if [[ "${item}" == "capnico" && "${rest}" == "apply --server-side -f "* ]]; then
      manifest="${rest#apply --server-side -f }"
      emit "  if [[ -n \"\${CAPNICO_IMAGE:-}\" ]]; then"
      emit "    CAPNICO_TMP=\"\$(mktemp -d \"\${TMPDIR:-/tmp}/nke-capnico.XXXXXX\")\""
      emit "    cp ${manifest} \"\${CAPNICO_TMP}/capnico.yaml\""
      emit "    printf 'resources: [capnico.yaml]\\nimages:\\n  - name: %s\\n    newName: %s\\n    newTag: %s\\n' \\"
      emit "      '${capnico_image%%:*}' \"\${CAPNICO_IMAGE%:*}\" \"\${CAPNICO_IMAGE##*:}\" >\"\${CAPNICO_TMP}/kustomization.yaml\""
      emit "    run kubectl kustomize \"\${CAPNICO_TMP}\" | kc apply --server-side -f -"
      emit "    rm -rf \"\${CAPNICO_TMP}\""
      emit "  else"
      emit "    kc ${rest}"
      emit "  fi"
    else
      emit "  kc ${rest}"
    fi
    ;;
  "helm upgrade --install "*)
    rest="${line#helm }"
    rest="${rest/ --kubeconfig \/dev\/null/}"
    rest="${rest/--timeout 15m/${phase_timeout}}"
    emit "  hl $(paths "${rest}" 1)$(site_values_arg "${item}")"
    ;;
  "env HELM_REPOSITORY_CONFIG=${tmp_base}/"*)
    rest="${line#env }"
    rest="$(printf '%s' "${rest}" | sed -E "s#HELM_REPOSITORY_(CONFIG|CACHE)=${tmp_base}/tmp\.[A-Za-z0-9]+/#HELM_REPOSITORY_\1=\"\${HELM_TMP}/#g; s#(HELM_REPOSITORY_(CONFIG|CACHE)=\"[^ ]*) #\1\" #g")"
    emit "  run env $(paths "${rest}" 1)"
    ;;
  *)
    die "line resisted normalisation: ${line}"
    ;;
  esac
done <"${raw}"

[[ "${items}" -gt 0 ]] || die "dry-run produced no items"
[[ -n "${releases}" ]] || die "dry-run produced no helm releases"
[[ -n "${cwe_ns}" ]] || die "no controller release in the dry-run"

for leak in "/dev/null " "${tmp_base}" "${sbom_arg}/"; do
  # The Certificate pre-check redirects to /dev/null on purpose.
  if grep -F -- "${leak}" "${body}" | grep -vF '>/dev/null 2>&1' | grep -q .; then
    die "unnormalised text '${leak}' in the generated body:
$(grep -nF -- "${leak}" "${body}")"
  fi
done

# ---------------------------------------------------------------------------
# 4. Assemble the stage script.
# ---------------------------------------------------------------------------

gen_stage="${work}/gen/${stage_file}"
{
  cat <<EOF
#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) ${year} NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Code generated by ${here}/regen.sh. DO NOT EDIT.
#
# Install the site controller: apply the ${items} items of ${sbom_arg}/${target}
# in list order, then check the provider and the management plane.
#
# Usage: ./${stage_file} [run|check]
#
# Optional: CAPNICO_IMAGE=repository:tag repoints the CAPNICo manifest at
# another image, for a locally built provider.
#
# The guide docs/site-bootstrap/install-site-controller.md is the authority.
# Commands between \`# doc-begin\` and \`# doc-end\` are extracted verbatim into
# the guide. To change them, change the deploy path or ${here}/regen.sh
# and run the generator.
set -euo pipefail
. "\$(dirname "\${BASH_SOURCE[0]}")/lib.sh"

HELM_TMP=""
RENDER_TMP=""
CAPNICO_TMP=""

# cleanup_tmp removes the render and helm temp dirs, then hands the exit code
# on to lib.sh's on_exit.
cleanup_tmp() {
  local rc=\$?
  rm -rf "\${HELM_TMP}" "\${RENDER_TMP}" "\${CAPNICO_TMP}"
  return "\${rc}"
}

stage_run() {
  require_cmd kubectl helm python3
  for f in bootstrap-config.json$(for item in ${site_values_items}; do printf ' %s.yaml' "${item}"; done); do
    [[ -n "\${DRY_RUN}" || -f "\${OUT_DIR}/\${f}" ]] ||
      die "\${OUT_DIR}/\${f} not found. Run 03-describe-site.sh first."
  done
  helm_want="\$(sed -n 's/^HELM_VERSION[[:space:]]*?*=[[:space:]]*//p' "\${SBOM_DIR}/../../make/tool-versions.mk")"
  helm_have="\$(helm version --template '{{.Version}}' 2>/dev/null || true)"
  [[ -z "\${helm_want}" || "\${helm_have}" == "\${helm_want}" ]] ||
    warn "helm on PATH is \${helm_have:-unknown}; the repository pins \${helm_want} (make/tool-versions.mk)"
  trap 'cleanup_tmp; on_exit' EXIT

  # doc-begin
  # Imported images are eligible for kubelet garbage collection. Confirm they
  # are still present before anything schedules.
  if [[ -x "\${INSTALL_DIR}/02-stage-images.sh" ]]; then
    "\${INSTALL_DIR}/02-stage-images.sh" check ||
      die "staged images are missing. Rerun 02-stage-images.sh."
  fi

  HELM_TMP="\$(mktemp -d "\${TMPDIR:-/tmp}/nke-helm.XXXXXX")"

EOF
  cat "${body}"
  cat <<EOF
  # doc-end
}

stage_check() {
  if [[ -n "\${DRY_RUN}" ]]; then
    printf 'SKIP  provider and plane checks need a cluster (DRY_RUN)\n'
    return 0
  fi
  log "provider"
  for crd in $(printf '%s\n' "${crds}" | tr '\n' ' ' | sed 's/ *$//'); do
    if [[ "\$(kc get crd "\${crd}" -o jsonpath='{.status.conditions[?(@.type=="Established")].status}' 2>/dev/null)" == "True" ]]; then
      check_pass "CRD \${crd} Established"
    else
      check_fail "CRD \${crd} not Established"
    fi
  done
  if [[ "\$(kc -n ${capnico_ns} get deployment ${capnico_deploy} -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null)" == "True" ]]; then
    check_pass "Deployment ${capnico_ns}/${capnico_deploy} Available"
  else
    check_fail "Deployment ${capnico_ns}/${capnico_deploy} not Available"
  fi
EOF
  while read -r sname sns; do
    [[ -n "${sname}" ]] || continue
    cat <<EOF
  if kc -n ${sns} get secret ${sname} >/dev/null 2>&1; then
    check_pass "Secret ${sns}/${sname} present"
  else
    check_fail "Secret ${sns}/${sname} missing"
  fi
EOF
  done <<<"${secrets}"
  cat <<EOF

  log "plane"
  # Every release must be deployed. A failed release recovers by rerunning this
  # stage. A pending release needs \`helm uninstall\` first.
  releases="\$(hl list -A -o json 2>/dev/null |
    python3 -c 'import json, sys; [print(r["name"], r["namespace"], r["status"]) for r in json.load(sys.stdin)]' 2>/dev/null || true)"
  while read -r name ns status; do
    [[ -n "\${name}" ]] || continue
    case "\${status}" in
    deployed) check_pass "helm release \${ns}/\${name} deployed" ;;
    failed) check_fail "helm release \${ns}/\${name} is failed: rerun this stage" ;;
    pending-*) check_fail "helm release \${ns}/\${name} is \${status}: helm uninstall \${name} -n \${ns}, then rerun this stage" ;;
    *) check_fail "helm release \${ns}/\${name} is \${status}" ;;
    esac
  done <<<"\${releases}"
EOF
  printf '  for want in'
  while read -r rname rns; do
    [[ -n "${rname}" ]] || continue
    printf ' "%s %s"' "${rname}" "${rns}"
  done <<<"${releases}"
  cat <<EOF
; do
    set -- \${want}
    if ! printf '%s\n' "\${releases}" | awk -v n="\$1" -v s="\$2" '\$1 == n && \$2 == s { found = 1 } END { exit !found }'; then
      check_fail "helm release \$2/\$1 missing: rerun this stage"
    fi
  done
  if [[ "\$(kc -n ${cwe_ns} get deployment ${cwe_deploy} -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null)" == "True" ]]; then
    check_pass "Deployment ${cwe_ns}/${cwe_deploy} Available"
  else
    check_fail "Deployment ${cwe_ns}/${cwe_deploy} not Available"
  fi
  # /readyz reports the database and Temporal checks. The image ships curl.
  body="\$(kc -n ${cwe_ns} exec deploy/${cwe_deploy} -- curl -sS -m 5 http://127.0.0.1:8080/readyz 2>/dev/null || true)"
  if printf '%s' "\${body}" | python3 -c 'import json, sys; r = json.load(sys.stdin); c = r.get("checks", {}); sys.exit(0 if r.get("status") == "ok" and c.get("database") == "ok" and c.get("temporal") == "ok" else 1)' 2>/dev/null; then
    check_pass "/readyz: database ok, temporal ok"
  else
    check_fail "/readyz: \${body:-no response}"
  fi
  # The worker reads the NicoMachine CRD once at start. If it started before
  # CAPNICo, it skipped the NICo controllers and must be restarted.
  if kc -n ${cwe_ns} logs deploy/${cwe_deploy} --all-containers --tail=-1 2>/dev/null | grep -qF '${crd_warning}'; then
    check_fail "${cwe_deploy} logged '${crd_warning}': kubectl -n ${cwe_ns} rollout restart deployment/${cwe_deploy}"
  else
    check_pass "${cwe_deploy} log has no '${crd_warning}'"
  fi
}

stage_main "\$@"
EOF
} >"${gen_stage}"
chmod 0755 "${gen_stage}"

if ! bash -n "${gen_stage}"; then
  cat -n "${gen_stage}" >&2
  die "generated ${stage_file} does not parse"
fi

# ---------------------------------------------------------------------------
# 5. Extract the doc blocks from every stage script, with 05 as generated.
# ---------------------------------------------------------------------------

for f in "${install_dir}"/[0-9][0-9]-*.sh; do
  [[ -e "${f}" ]] || continue
  cp "${f}" "${work}/install/"
done
cp "${gen_stage}" "${work}/install/${stage_file}"

# doc_block prints the lines between `# doc-begin` and `# doc-end`, dedented
# by the indentation of the begin marker, inside a bash fence.
doc_block() {
  awk '
    /^[[:space:]]*# doc-begin$/ { match($0, /^[[:space:]]*/); indent = RLENGTH; inblock = 1; if (n++ > 0) print ""; next }
    /^[[:space:]]*# doc-end$/ { inblock = 0; next }
    inblock { print substr($0, indent + 1) }
  ' "$1"
}

stage_names=""
for f in "${work}/install"/[0-9][0-9]-*.sh; do
  name="$(basename "${f}" .sh)"
  block="$(doc_block "${f}")"
  [[ -n "${block}" ]] || continue
  stage_names="${stage_names}${name}"$'\n'
  {
    printf '```bash\n'
    printf '%s\n' "${block}"
    printf '```\n'
  } >"${work}/blocks/${name}.md"
done

gen_doc="${work}/gen/$(basename "${doc_src}")"
awk -v dir="${work}/blocks" -v found="${work}/found.txt" '
  /^<!-- stage:[0-9][0-9]-[a-z0-9-]+ begin -->$/ {
    name = $2; sub(/^stage:/, "", name)
    print
    file = dir "/" name ".md"
    if ((getline l < file) > 0) {
      print l
      while ((getline l < file) > 0) print l
      close(file)
      print name >> found
      skip = 1
    } else {
      printf "regen: WARN: marker for %s has no stage script with a doc block; left as is\n", name > "/dev/stderr"
    }
    next
  }
  /^<!-- stage:[0-9][0-9]-[a-z0-9-]+ end -->$/ { skip = 0; print; next }
  !skip { print }
' "${doc_src}" >"${gen_doc}"

while read -r name; do
  [[ -n "${name}" ]] || continue
  grep -qx "${name}" "${work}/found.txt" 2>/dev/null ||
    warn "${doc_src} has no <!-- stage:${name} begin --> marker; block not placed"
done <<<"${stage_names}"

# ---------------------------------------------------------------------------
# 6. Deliver.
# ---------------------------------------------------------------------------

case "${mode}" in
out)
  mkdir -p "${out_dir}"
  cp "${gen_stage}" "${out_dir}/${stage_file}"
  cp "${gen_doc}" "${out_dir}/$(basename "${doc_src}")"
  printf 'regen: wrote %s and %s to %s\n' "${stage_file}" "$(basename "${doc_src}")" "${out_dir}" >&2
  ;;
check)
  rc=0
  if ! diff -u "${install_dir}/${stage_file}" "${gen_stage}" --label "committed/${stage_file}" --label "generated/${stage_file}"; then
    rc=1
  fi
  if ! diff -u "${doc_src}" "${gen_doc}" --label "committed/$(basename "${doc_src}")" --label "generated/$(basename "${doc_src}")"; then
    rc=1
  fi
  if [[ "${rc}" -ne 0 ]]; then
    printf 'regen: generated output differs from the committed files. Run %s/regen.sh and commit.\n' "${here}" >&2
    exit 1
  fi
  printf 'regen: %s and %s are current\n' "${stage_file}" "$(basename "${doc_src}")" >&2
  ;;
write)
  if ! cmp -s "${gen_stage}" "${install_dir}/${stage_file}" 2>/dev/null; then
    cp "${gen_stage}" "${install_dir}/${stage_file}"
    printf 'regen: wrote %s/%s\n' "${install_dir}" "${stage_file}" >&2
  fi
  if ! cmp -s "${gen_doc}" "${doc_src}"; then
    cp "${gen_doc}" "${doc_src}"
    printf 'regen: updated %s\n' "${doc_src}" >&2
  fi
  ;;
esac
