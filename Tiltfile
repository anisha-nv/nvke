# Copyright (c) 2026, NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Local contribution loop on Kind. Controller changes rebuild only the image;
# deployment catalog changes regenerate and re-apply the quick-start recipe.

cluster_name = os.getenv("KIND_CLUSTER_NAME", "nke")
expected_context = "kind-" + cluster_name
image_registry = os.getenv("CONTROLLER_IMAGE_REGISTRY", "nke.local")
image_repository = image_registry + "/controller"
image_tag = os.getenv("CONTROLLER_IMAGE_TAG", "dev")

if k8s_context() != expected_context:
    fail("expected Kubernetes context %s, got %s" % (expected_context, k8s_context()))

allow_k8s_contexts(expected_context)

local_resource(
    "controller-image",
    cmd="IMAGE_REGISTRY=%s IMAGE_TAG=%s KIND_CLUSTER_NAME=%s bash build/controller.sh" % (
        image_registry,
        image_tag,
        cluster_name,
    ),
    deps=[
        "build/controller.sh",
        "controller/cmd",
        "controller/internal",
        "controller/api",
        "controller/profiles",
        "controller/ui",
        "controller/go.mod",
        "controller/go.sum",
        "controller/Dockerfile",
    ],
    labels=["contrib"],
)

local_resource(
    "quickstart-bundle",
    cmd="""bash scripts/install-aicr &&
rm -rf generated/aicr/tilt/helm &&
.bin/aicr bundle --recipe deploy/overlays/kind.yaml --data deploy \
  --deployer helm --output generated/aicr/tilt/helm \
  --set controller:image.repository=%s \
  --set controller:image.tag=%s &&
.bin/aicr verify generated/aicr/tilt/helm""" % (
        image_repository,
        image_tag,
    ),
    deps=[
        "deploy",
        "scripts/install-aicr",
    ],
    labels=["aicr"],
)

local_resource(
    "nke-stack",
    cmd="KUBECONFIG_FLAG='--kube-context %s' $PWD/generated/aicr/tilt/helm/deploy.sh --retries 1" % expected_context,
    deps=["generated/aicr/tilt/helm"],
    resource_deps=["quickstart-bundle"],
    labels=["aicr"],
)
