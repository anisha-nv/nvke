#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

# Classify a pull request once, then let merge-gate.yml pass these outputs to
# the reusable workflows. Unknown and cross-cutting paths deliberately expand
# to the full suite so a new repository area cannot silently escape CI.

full=false
full_build=false
agent=false
cli=false
controller=false
controller_api=false
forge_vms=false
capd_reboot=false
nico_mock=false
e2e=false
chart=false
generated=false
shellcheck=false
shipped_licenses=false

if [[ "${1:-}" == "--all" ]]; then
  full=true
  shift
fi

# A dispatch, new branch, or malformed event with no usable diff fails safe.
if [[ "$#" -eq 0 ]]; then
  full=true
fi

for path in "$@"; do
  known=false

  case "$path" in
    docs/* | */docs/* | *.md | .github/ISSUE_TEMPLATE/* | .github/pull_request_template.md)
      known=true
      ;;
    .github/workflows/* | .github/scripts/* | .github/actionlint.yaml | .github/copy-pr-bot.yaml)
      # CI changes can affect any lane, including the classifier itself.
      full=true
      known=true
      ;;
    go.work | go.work.sum | vendor/* | Makefile | build/* | */go.mod | */go.sum)
      # The workspace and vendor tree are shared by every Go module.
      full=true
      known=true
      ;;
    LICENSE | NOTICE | controller/licenses/*)
      shipped_licenses=true
      known=true
      ;;
    controller/deploy/charts/*)
      chart=true
      known=true
      case "$path" in
        */LICENSE | */NOTICE)
          shipped_licenses=true
          ;;
      esac
      ;;
    agent/pkg/agent/proto/*)
      # The controller imports the agent's public protobuf module.
      agent=true
      controller=true
      known=true
      ;;
    agent/*)
      agent=true
      known=true
      ;;
    cli/*)
      cli=true
      known=true
      ;;
    controller/api/*)
      # API clients are consumed by the controller, CLI, and E2E module.
      controller_api=true
      controller=true
      cli=true
      e2e=true
      generated=true
      known=true
      ;;
    controller/forge-vms/*)
      forge_vms=true
      known=true
      ;;
    controller/hack/capd-reboot-controller/*)
      capd_reboot=true
      known=true
      ;;
    controller/hack/nico-mock/*)
      nico_mock=true
      known=true
      ;;
    controller/test/e2e/*)
      e2e=true
      known=true
      ;;
    controller/*)
      controller=true
      known=true
      ;;
  esac

  case "$path" in
    *.sh)
      shellcheck=true
      known=true
      ;;
  esac

  case "$path" in
    agent/cmd/* | cli/cmd/* | controller/cmd/* | controller/forge-vms/cmd/* | controller/hack/*/cmd/*)
      # These paths define shipped binaries and their version symbols. Preserve
      # the repository-wide version-check when they change.
      full_build=true
      ;;
  esac

  if [[ "$known" == false ]]; then
    full=true
  fi
done

if [[ "$full" == true ]]; then
  full_build=true
  agent=true
  cli=true
  controller=true
  controller_api=true
  forge_vms=true
  capd_reboot=true
  nico_mock=true
  e2e=true
  chart=true
  generated=true
  shellcheck=true
  shipped_licenses=true
fi

modules=()
lint_modules=()
unit_modules=()
compile_suites=()

if [[ "$agent" == true ]]; then
  modules+=("agent")
  lint_modules+=("agent")
fi
if [[ "$cli" == true ]]; then
  modules+=("cli")
  lint_modules+=("cli")
  unit_modules+=("cli")
fi
if [[ "$controller" == true ]]; then
  modules+=("controller")
  lint_modules+=("controller")
  unit_modules+=("controller")
  compile_suites+=("e2e-gofail" "teleport-integration" "integration-test")
fi
if [[ "$controller_api" == true ]]; then
  modules+=("controller/api")
fi
if [[ "$forge_vms" == true ]]; then
  modules+=("controller/forge-vms")
  lint_modules+=("controller/forge-vms")
  unit_modules+=("controller/forge-vms")
  compile_suites+=("forge-vms-integration")
fi
if [[ "$capd_reboot" == true ]]; then
  modules+=("controller/hack/capd-reboot-controller")
fi
if [[ "$nico_mock" == true ]]; then
  modules+=("controller/hack/nico-mock")
  lint_modules+=("controller/hack/nico-mock")
  unit_modules+=("controller/hack/nico-mock")
fi
if [[ "$e2e" == true ]]; then
  modules+=("controller/test/e2e")
  compile_suites+=("test-e2e")
fi

json_array() {
  jq -cn --args '$ARGS.positional' "$@"
}

modules_json="$(json_array "${modules[@]}")"
lint_modules_json="$(json_array "${lint_modules[@]}")"
unit_modules_json="$(json_array "${unit_modules[@]}")"
compile_suites_json="$(json_array "${compile_suites[@]}")"

go_checks=false
lint=false
controller_test=false
licenses=false
docs_only=false

if [[ "${#modules[@]}" -gt 0 ]]; then
  go_checks=true
  licenses=true
fi
if [[ "${#lint_modules[@]}" -gt 0 ]]; then
  lint=true
fi
if [[ "${#unit_modules[@]}" -gt 0 || "${#compile_suites[@]}" -gt 0 ]]; then
  controller_test=true
fi
if [[ "$shipped_licenses" == true ]]; then
  licenses=true
fi
if [[ "$go_checks" == false && "$chart" == false && "$shellcheck" == false && "$licenses" == false && "$full" == false ]]; then
  docs_only=true
fi

cat <<EOF
full=$full
full_build=$full_build
modules=$modules_json
lint_modules=$lint_modules_json
unit_modules=$unit_modules_json
compile_suites=$compile_suites_json
go=$go_checks
agent_test=$agent
controller_test=$controller_test
workspace=$full
chart=$chart
licenses=$licenses
shipped_licenses=$shipped_licenses
generated=$generated
actionlint=$full
shellcheck=$shellcheck
lint=$lint
govulncheck=$go_checks
docs_only=$docs_only
EOF

printf 'CI selection: full=%s modules=%s chart=%s docs_only=%s\n' \
  "$full" "$modules_json" "$chart" "$docs_only" >&2
