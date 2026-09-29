#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Verify the shipped helper, not a host substitute. A killed kubectl command is
# a failed check: only the remote helper's own nonzero exit satisfies the test.
set -euo pipefail

output_dir="$(mktemp -d)"
trap 'rm -rf "${output_dir}"' EXIT

run_check() {
  local mode="$1" result
  set +e
  timeout 15s kubectl exec -i -n cloud-workflow-engine deployment/cloud-workflow-engine -- \
    /bin/sh -s -- "${mode}" >"${output_dir}/stdout" 2>"${output_dir}/stderr" <<'REMOTE'
set -eu
mode="$1"
helper=/opt/spiffe/bin/k8s-spiffe-workload-jwt-exec-auth
export SPIFFE_JWT_SOURCE=workload-api
export SPIFFE_ENDPOINT_SOCKET=unix:///tmp/site-health-intentionally-unavailable.sock
export SPIFFE_JWT_AUDIENCE=site-health-fixture
export SPIFFE_JWT_EXCHANGE_ENDPOINT=
if [ "${mode}" = diagnostics ]; then
  export SPIFFE_JWT_EXCHANGE_ENDPOINT='http://example.invalid/?token=SENTINEL-CREDENTIAL'
fi
set +e
# Match the fixed wrapper in clusterreachability/probe.go. exec leaves no
# background child behind and the helper owns its credential deadline.
/bin/sh -c 'exec "$@" 2>/dev/null' reachability-probe "${helper}" -timeout=5s
result=$?
set -e
if [ "${result}" -eq 0 ]; then
  exit 1
fi
# The pinned Go helper uses exit 1 for credential failures. Missing executable,
# signal termination, or shell errors must not satisfy this assertion.
if [ "${result}" -ne 1 ]; then
  exit 1
fi
printf 'HELPER_EXITED\n'
REMOTE
  result=$?
  set -e
  if [[ "${result}" -ne 0 ]] || [[ "$(cat "${output_dir}/stdout")" != HELPER_EXITED ]] || [[ -s "${output_dir}/stderr" ]]; then
    # Never print captured diagnostics: that would undo the probe's redaction.
    printf 'FAIL: probe helper %s check (runner exit %s)\n' "${mode}" "${result}" >&2
    exit 1
  fi
  printf 'PASS: probe helper %s check\n' "${mode}"
}

run_check timeout
run_check diagnostics
# A second timeout check proves the first invocation exited and subsequent
# credential attempts can proceed; each has an independent watchdog.
run_check timeout
