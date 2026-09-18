#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Nix supplies host tools and deferred derivation references, never guest outputs.
set -euo pipefail
if [[ "${1:-}" == --help || "${1:-}" == -h ]]; then
  echo 'Usage: build-packages --output PATH [--source CHECKOUT]'
  exit 0
fi
plan=$(mktemp)
vm_pid=
runtime=
ready=0
stop_bootstrap() {
  [[ -n "$vm_pid" ]] || return 0
  if [[ "$ready" == 1 ]]; then
    timeout 15 "${ssh[@]}" poweroff >/dev/null 2>&1 || true
    for ((i=0; i<60; i++)); do
      kill -0 "$vm_pid" 2>/dev/null || break
      sleep 1
    done
  fi
  if kill -0 "$vm_pid" 2>/dev/null; then
    kill -TERM -- "-$vm_pid" 2>/dev/null || true
  fi
  wait "$vm_pid" 2>/dev/null || true
  vm_pid=
}
cleanup() {
  status=$?
  trap - EXIT INT TERM
  stop_bootstrap
  if [[ -n "$runtime" ]]; then rm -rf -- "$runtime"; fi
  rm -f "$plan"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

bootstrap=0
if [[ "$host_system" != "$guest_system" ]]; then
  # Nix checks the local store and configured caches before listing builds.
  # The pinned Nix CLI emits each pending derivation on its own indented line.
  status=0
  nix build --dry-run --no-link --log-format raw "$runner_drv^out" 2>"$plan" || status=$?
  cat "$plan" >&2
  [[ "$status" == 0 ]] || exit "$status"
  mapfile -t missing < <(sed -n 's/^  \(\/nix\/store\/[^ ]*\.drv\)$/\1/p' "$plan")
  if ((${#missing[@]})); then
    nix derivation show "${missing[@]}" >"$plan"
    needs_linux=$(jq -r --arg system "$guest_system" '.derivations | any(.[]; .system == $system)' "$plan")
    if [[ "$needs_linux" == true ]]; then bootstrap=1; fi
  fi
fi

build_options=()
if [[ "$bootstrap" == 1 ]]; then
  nix store info --json >"$plan"
  if ! jq -e '.trusted == true' "$plan" >/dev/null; then
    echo 'Automatic Linux bootstrap requires a trusted Nix user to select a temporary builder. See os-image/nix/README.md.' >&2
    exit 1
  fi
  state=${NKE_BUILDER_STATE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/nke/builders/$arch}/bootstrap
  mkdir -p "$state/keys"
  state=$(cd "$state" && pwd -P)
  exec 8>"$state/build.lock"
  flock -n 8 || { echo "Bootstrap state is already in use: $state" >&2; exit 1; }
  key=$state/client-key
  [[ -e "$key" ]] || ssh-keygen -q -t ed25519 -N '' -f "$key"
  # Store ownership makes the public-key share acceptable to sshd StrictModes.
  # Only the public key enters the store; the private key stays in user state.
  install -m 0644 "$key.pub" "$state/keys/builder_ed25519.pub"
  public_keys=$(nix-store --add "$state/keys")
  if [[ "$arch" == arm64 ]]; then default_port=31024; else default_port=31025; fi
  port=${NKE_BOOTSTRAP_PORT:-$default_port}
  printf '[127.0.0.1]:%s %s\n' "$port" "$bootstrap_host_key" >"$state/known_hosts"
  ssh=(ssh -F /dev/null -i "$key" -p "$port" -o IdentityAgent=none
    -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=2
    -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$state/known_hosts" builder@127.0.0.1)
  # This runner changes only host settings; its stock Linux closure is cached.
  bootstrap_path=$(nix build --no-link --print-out-paths "$bootstrap_drv^out")
  log=$state/console.log
  # The stock runner copies read-only certificates here; use fresh scratch space.
  runtime=$(mktemp -d "$state/runtime.XXXXXXXX")
  echo "Preparing the package VM with a temporary Linux builder; console: $log" >&2
  set -m
  KEYS="$public_keys" NIX_DISK_IMAGE="$state/root.qcow2" \
    TMPDIR="$runtime" USE_TMPDIR=1 \
    QEMU_NET_OPTS="hostfwd=tcp:127.0.0.1:$port-:22" \
    "$bootstrap_path/bin/$bootstrap_program" </dev/null >"$log" 2>&1 8>&- &
  vm_pid=$!
  deadline=$((SECONDS + ${NKE_BUILDER_BOOT_TIMEOUT:-900}))
  until "${ssh[@]}" true >/dev/null 2>&1; do
    if ! kill -0 "$vm_pid" 2>/dev/null || ((SECONDS >= deadline)); then
      echo "Bootstrap VM failed to become ready; see $log" >&2
      exit 1
    fi
    sleep 1
  done
  ready=1
  key_uri=$(jq -rn --arg key "$key" '$key | @uri')
  host_key=$(printf '%s\n' "$bootstrap_host_key" | base64 -w0)
  build_options=(--builders "ssh-ng://builder@127.0.0.1:$port?ssh-key=$key_uri $guest_system - 4 1 - - $host_key"
    --option builders-use-substitutes true)
fi
runner=$(nix build --no-link --print-out-paths "${build_options[@]}" "$runner_drv^out")
stop_bootstrap
if [[ -n "$runtime" ]]; then rm -rf -- "$runtime"; fi
exec 8>&-
rm -f "$plan"
trap - EXIT INT TERM
exec "$runner/bin/build-packages-$arch" "$@"
