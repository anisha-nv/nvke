#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

log() { echo "[nke-start] $*"; }

ensure_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    log "docker not installed; skipping dockerd"
    return 0
  fi
  if docker info >/dev/null 2>&1 || sudo docker info >/dev/null 2>&1; then
    return 0
  fi
  if ! pgrep -x dockerd >/dev/null; then
    log "starting dockerd (fuse-overlayfs)"
    sudo dockerd --storage-driver=fuse-overlayfs --iptables=false >/tmp/dockerd.log 2>&1 &
    for _ in $(seq 1 30); do
      sudo docker info >/dev/null 2>&1 && return 0
      sleep 1
    done
    log "dockerd did not become ready; see /tmp/dockerd.log"
  fi
}

ensure_postgres() {
  if pg_isready -h 127.0.0.1 -p 5432 >/dev/null 2>&1; then
    log "PostgreSQL already accepting connections"
    return 0
  fi
  if command -v docker >/dev/null 2>&1 && sudo docker info >/dev/null 2>&1; then
    if ! sudo docker ps -a --format '{{.Names}}' | grep -qx cwe-postgres; then
      log "starting PostgreSQL container (public ECR mirror)"
      sudo docker run -d --name=cwe-postgres -p 5432:5432 \
        -e POSTGRES_PASSWORD=postgres \
        public.ecr.aws/docker/library/postgres:17.4
    else
      sudo docker start cwe-postgres >/dev/null 2>&1 || true
    fi
    for _ in $(seq 1 30); do
      pg_isready -h 127.0.0.1 -p 5432 >/dev/null 2>&1 && return 0
      sleep 1
    done
  fi
  if command -v pg_ctlcluster >/dev/null 2>&1; then
    log "starting system PostgreSQL"
    sudo service postgresql start >/dev/null 2>&1 || sudo pg_ctlcluster 16 main start >/dev/null 2>&1 || true
    sudo -u postgres psql -tAc "SELECT 1" >/dev/null 2>&1 || true
    sudo -u postgres psql -c "ALTER USER postgres PASSWORD 'postgres';" >/dev/null 2>&1 || true
  fi
  until pg_isready -h 127.0.0.1 -p 5432 >/dev/null 2>&1; do sleep 1; done
  log "PostgreSQL is ready"
}

ensure_temporal() {
  if pgrep -f "temporal server start-dev" >/dev/null; then
    log "Temporal dev server already running"
    return 0
  fi
  log "starting Temporal dev server"
  nohup temporal server start-dev >/tmp/temporal-dev.log 2>&1 &
  for _ in $(seq 1 30); do
    if grep -q "Server:  localhost:7233" /tmp/temporal-dev.log 2>/dev/null; then
      log "Temporal is ready on localhost:7233"
      return 0
    fi
    sleep 1
  done
  log "Temporal may still be starting; see /tmp/temporal-dev.log"
}

ensure_k3s() {
  export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
  if kubectl get nodes >/dev/null 2>&1; then
    log "Kubernetes API already reachable"
    return 0
  fi
  if ! command -v k3s >/dev/null 2>&1; then
    log "k3s not installed; set KUBECONFIG yourself for controller run-local"
    return 0
  fi
  if ! pgrep -f "k3s server" >/dev/null; then
    log "starting k3s (fuse-overlayfs, flannel disabled for nested VMs)"
    sudo mkdir -p /etc/rancher/k3s
    echo 'snapshotter: fuse-overlayfs' | sudo tee /etc/rancher/k3s/config.yaml >/dev/null
    sudo k3s server \
      --write-kubeconfig-mode 644 \
      --snapshotter fuse-overlayfs \
      --flannel-backend=none \
      --disable traefik,servicelb,metrics-server,coredns,network-policy,local-storage \
      >/tmp/k3s.log 2>&1 &
    for _ in $(seq 1 60); do
      kubectl get nodes >/dev/null 2>&1 && break
      sleep 2
    done
  fi
  if kubectl get nodes >/dev/null 2>&1; then
    log "Kubernetes API is ready (KUBECONFIG=${KUBECONFIG})"
  else
    log "k3s did not become ready; see /tmp/k3s.log"
  fi
}

ensure_docker
ensure_postgres
ensure_temporal
ensure_k3s

log "dev dependencies up; run controller with: cd controller && make run-local (or go run ./cmd/worker with KUBECONFIG set)"
