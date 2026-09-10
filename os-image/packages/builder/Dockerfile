# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Multi-platform index: native linux/amd64 and linux/arm64 use the same recipe.
FROM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36
ARG CRANE_VERSION
ARG NFPM_VERSION
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl dpkg-dev gnupg gzip make tar \
    && rm -rf /var/lib/apt/lists/*
# Tool versions come from make/tool-versions.mk. They live only in this builder.
RUN GOBIN=/opt/nke/bin go install github.com/google/go-containerregistry/cmd/crane@v${CRANE_VERSION} \
    && GOBIN=/opt/nke/bin go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v${NFPM_VERSION}
ENV PATH="/opt/nke/bin:${PATH}" GOTOOLCHAIN=local
# Docker initializes a fresh named volume with these permissions, allowing the
# caller's uid to own its cache without a privileged setup container.
RUN mkdir -m 1777 /cache
