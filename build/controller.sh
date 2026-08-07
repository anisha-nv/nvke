#!/usr/bin/env bash
#
# Build the NKE controller container image from the in-tree controller/ sources.
#
# Flow:
#   1. Resolve image name as ${IMAGE_REGISTRY}/controller:${IMAGE_TAG}.
#   2. Pass the monorepo revision as GIT_VERSION so the image identifies the
#      source commit that supplied controller/.
#   3. Detect the local Docker engine arch and map it to a linux/<arch> platform
#      (overridable with DOCKER_PLATFORM).
#   4. Unset GIT_CREDENTIALS so a legacy env var cannot leak into Docker build
#      args/logs. Private Go modules still authenticate via the BuildKit secret
#      GIT_CREDENTIALS_NETRC when the controller Dockerfile/Makefile require it.
#   5. Delegate to `make -C controller docker-build-local` with IMG_* / BUILD_*
#      overrides so the monorepo controls the published image coordinates.
#
# Optional follow-on steps:
#   - PUSH=true            docker push the built image
#   - KIND_CLUSTER_NAME=…  kind load the image into that cluster; if a Deployment
#                          named controller already exists in namespace controller,
#                          restart and wait for rollout (local-dev / Tilt loop)
#
# Prints the final image reference (name:tag) on stdout.
#
# Environment:
#   IMAGE_REGISTRY      (default: localhost:5000/nke)
#   IMAGE_TAG           (default: dev)
#   DOCKER_PLATFORM     (default: derived from `docker version` arch)
#   PUSH                true to push after build (default: false)
#   KIND_CLUSTER_NAME   if set, load into that Kind cluster after build
#   GIT_CREDENTIALS_NETRC   netrc contents for private gitlab-master modules
#
# Example:
#   IMAGE_REGISTRY=nke.local IMAGE_TAG=dev KIND_CLUSTER_NAME=nke \
#     bash build/controller.sh

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image_registry="${IMAGE_REGISTRY:-localhost:5000/nke}"
image_tag="${IMAGE_TAG:-dev}"
image_name="${image_registry%/}/controller"
source_revision="${SOURCE_REVISION:-$(git -C "${repo_root}" rev-parse HEAD)}"
docker_arch="$(docker version --format '{{.Server.Arch}}')"
case "${docker_arch}" in
  arm64|aarch64) docker_platform=linux/arm64 ;;
  amd64|x86_64) docker_platform=linux/amd64 ;;
  *) echo "unsupported Docker architecture: ${docker_arch}" >&2; exit 1 ;;
esac

# Prevent a legacy credential variable from reaching Docker build arguments or
# logs. Private modules use the BuildKit secret exposed through
# GIT_CREDENTIALS_NETRC instead.
unset GIT_CREDENTIALS

make -C "${repo_root}/controller" docker-build-local \
  BUILD_VERSION="${image_tag}" \
  DOCKER_PLATFORM="${DOCKER_PLATFORM:-${docker_platform}}" \
  GIT_VERSION="${source_revision}" \
  IMG_NAME="${image_name}" \
  IMG_TAG="${image_tag}" \
  IMG="${image_name}:${image_tag}"

if [[ "${PUSH:-false}" == "true" ]]; then
  docker push "${image_name}:${image_tag}"
fi

if [[ -n "${KIND_CLUSTER_NAME:-}" ]]; then
  kind load docker-image --name "${KIND_CLUSTER_NAME}" "${image_name}:${image_tag}"
  if kubectl --context "kind-${KIND_CLUSTER_NAME}" \
    --namespace controller \
    get deployment controller >/dev/null 2>&1; then
    kubectl --context "kind-${KIND_CLUSTER_NAME}" \
      --namespace controller \
      rollout restart deployment/controller
    kubectl --context "kind-${KIND_CLUSTER_NAME}" \
      --namespace controller \
      rollout status deployment/controller --timeout=180s
  fi
fi

printf '%s\n' "${image_name}:${image_tag}"
