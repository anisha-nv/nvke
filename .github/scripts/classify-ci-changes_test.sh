#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
classifier="${script_dir}/classify-ci-changes.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

value() {
  local output="$1"
  local key="$2"
  sed -n "s/^${key}=//p" "$output"
}

assert_case() {
  local name="$1"
  local expected="$2"
  shift 2

  local output="${tmp}/${name// /-}.out"
  "$classifier" "$@" >"$output"

  local assertion key want got
  IFS=';' read -r -a assertions <<<"$expected"
  for assertion in "${assertions[@]}"; do
    key="${assertion%%=*}"
    want="${assertion#*=}"
    got="$(value "$output" "$key")"
    if [[ "$got" != "$want" ]]; then
      printf 'FAIL %s: %s\n  want: %s\n   got: %s\n' "$name" "$key" "$want" "$got" >&2
      exit 1
    fi
  done

  printf 'PASS %s\n' "$name"
}

assert_case \
  "docs only" \
  'full=false;go=false;chart=false;docs_only=true;modules=[]' \
  docs/architecture.md agent/docs/troubleshooting.md README.md

assert_case \
  "agent only" \
  'full=false;agent_test=true;controller_test=false;modules=["agent"];lint_modules=["agent"]' \
  agent/internal/agent/agent.go

assert_case \
  "agent proto includes controller consumer" \
  'full=false;agent_test=true;controller_test=true;modules=["agent","controller"];unit_modules=["controller"]' \
  agent/pkg/agent/proto/node_agent.proto

assert_case \
  "cli only" \
  'full=false;agent_test=false;controller_test=true;modules=["cli"];unit_modules=["cli"];compile_suites=[]' \
  cli/internal/client/client.go

assert_case \
  "controller only" \
  'full=false;modules=["controller"];unit_modules=["controller"];compile_suites=["e2e-gofail","teleport-integration","integration-test"]' \
  controller/internal/server/server.go

assert_case \
  "api expands downstream consumers" \
  'full=false;generated=true;modules=["cli","controller","controller/api","controller/test/e2e"];unit_modules=["cli","controller"]' \
  controller/api/openapi.yaml

assert_case \
  "forge vms only" \
  'full=false;modules=["controller/forge-vms"];unit_modules=["controller/forge-vms"];compile_suites=["forge-vms-integration"]' \
  controller/forge-vms/internal/state/instances.go

assert_case \
  "chart only" \
  'full=false;go=false;chart=true;docs_only=false;modules=[]' \
  controller/deploy/charts/cloud-workflow-engine/values.yaml

assert_case \
  "module shell also runs shellcheck" \
  'full=false;shellcheck=true;modules=["controller"]' \
  controller/internal/runbook/scripts/example.sh

assert_case \
  "dependency change is cross cutting" \
  'full=true;workspace=true;modules=["agent","cli","controller","controller/api","controller/forge-vms","controller/hack/capd-reboot-controller","controller/hack/nico-mock","controller/test/e2e"]' \
  cli/go.mod

assert_case \
  "ci change is cross cutting" \
  'full=true;actionlint=true;workspace=true' \
  .github/workflows/merge-gate.yml

assert_case \
  "unknown path fails safe" \
  'full=true;workspace=true;docs_only=false' \
  new-component/config.yaml

assert_case \
  "empty diff fails safe" \
  'full=true;workspace=true;docs_only=false'
