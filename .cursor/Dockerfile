FROM ubuntu:24.04

ARG K3S_VERSION=v1.31.5%2Bk3s1
ARG TEMPORAL_VERSION=1.5.1
ARG KIND_VERSION=0.32.0

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        docker.io \
        fuse-overlayfs \
        git \
        jq \
        postgresql \
        postgresql-client \
    && rm -rf /var/lib/apt/lists/*

# Temporal CLI (used by controller local dev; pinned near Flox temporal-cli).
RUN curl -fsSL "https://github.com/temporalio/cli/releases/download/v${TEMPORAL_VERSION}/temporal_cli_${TEMPORAL_VERSION}_linux_amd64.tar.gz" \
    | tar -xzf - -C /usr/local/bin temporal

# k3s provides kubectl via `k3s kubectl` when kind/Docker Hub pulls are unreliable.
RUN curl -fsSL -o /usr/local/bin/k3s "https://github.com/k3s-io/k3s/releases/download/${K3S_VERSION}/k3s" \
    && chmod 755 /usr/local/bin/k3s \
    && ln -sf /usr/local/bin/k3s /usr/local/bin/kubectl

RUN curl -fsSL "https://kind.sigs.k8s.io/dl/v${KIND_VERSION}/kind-linux-amd64" -o /usr/local/bin/kind \
    && chmod 755 /usr/local/bin/kind

RUN usermod -aG docker ubuntu || true

USER ubuntu
