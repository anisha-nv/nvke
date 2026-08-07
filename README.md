# NVIDIA Kubernetes Engine

NVIDIA Kubernetes Engine (NKE) is a Kubernetes lifecycle management system. It
provides APIs and durable workflows for provisioning clusters, managing node
pools, upgrading Kubernetes, and coordinating node-level operations across
infrastructure providers.

## Repository components

- **Controller (`controller/`)** — exposes the NKE API and uses Temporal
  workflows to reconcile clusters, node pools, and nodes.
- **Node agent (`node-agent/`)** — runs on each managed node and performs
  privileged operations such as kubeadm lifecycle, package installation, CNI
  installation, and API encryption.
- **Deployment catalog (`deploy/`)** — defines management-stack components,
  versions, dependencies, values, and manifests.
- **Repository tooling (`build/`, `scripts/`, `Tiltfile`)** — supports
  component builds, local development, and catalog verification.

The quick-start management stack includes cert-manager, SPIRE, PostgreSQL,
Temporal, and the NKE controller.

## Public release assumptions

This repository is being structured around two public-release assumptions:

- all private Go dependencies are removed, replaced, or published so a clean
  public checkout can build the controller;
- NVIDIA publishes versioned, multi-architecture controller images to
  `ghcr.io/nvidia/nke-controller` and pins released image digests in the
  deployment catalog.

These conditions are not complete today. The `0.0.0-dev` controller image
reference models the intended release contract and will be updated by the
image publication pipeline. Until that image is available, developers with
private dependency access can build and load `nke.local/controller:dev` with
`build/controller.sh`, then override `controller:image.repository` and
`controller:image.tag` during AICR bundle generation. The Tilt workflow
automates this local path.

## Artifacts and consumption

This repository produces:

- a controller container image;
- node-agent binaries and packages;
- dependency-ordered Helm deployment bundles;
- optional Argo CD Application manifests.

NKE releases are expected to publish the controller image. Consumers may pull
it directly from GHCR or mirror it into an approved registry, then generate
Helm or Argo CD artifacts from the catalog. NKE does not require a specific
rollout system. Production credentials, environment-specific configuration,
mirroring, and promotion policy remain the responsibility of the operator.

## Deployment composition with AICR

[NVIDIA AI Cluster Runtime (AICR)](https://github.com/NVIDIA/aicr) resolves a
versioned component catalog into deployment-ready artifacts. It gives NKE one
model for component versions, dependencies, values, and multiple deployment
formats instead of separate hand-maintained Helm and GitOps stacks.

NKE currently demonstrates two criteria selections:

- **Quick start (`--service kind`)** — the minimal management stack used by
  the Kind example.
- **Observability (`--service kind --platform observability`)** — extends
  quick start with Prometheus Operator CRDs and kube-prometheus-stack.

The catalog is authored as AICR `RecipeMetadata`:

```text
deploy/
  registry.yaml
  overlays/
    base.yaml
    kind.yaml
    kind-observability.yaml
  components/
```

`aicr bundle` accepts a leaf overlay, resolves its inheritance chain into a
`RecipeResult`, and renders Helm or Argo CD output. `aicr verify` checks the
generated bundle. The `RecipeResult` is AICR's intermediate resolved model;
consumers normally install the deployer-specific output rather than consume
the result separately.

AICR remains intentionally narrow in this repository: NKE uses catalog
resolution, dependency ordering, deployer output, and bundle integrity. NKE
does not use AICR as its image builder or production orchestrator.

Potential extensions include signed and versioned bundles, deployment
validation evidence, and an NKE API contract for installing AICR-defined
runtime stacks on newly provisioned clusters. AICR may also describe and
version node-image definitions and artifacts i.e. NVIDIA Kubernetes
Definition (NKD); the node-image builder itself would remain separate.

## Quick start: install on Kind with Helm

### Requirements

- Docker, or Colima on macOS
- Kind
- Helm
- `curl`

Run these commands from the repository root.

```bash
# macOS only; Linux users can use an existing Docker daemon.
colima start --cpu 6 --memory 12 --disk 100
export DOCKER_HOST="unix://${HOME}/.colima/default/docker.sock"

kind create cluster --name nke
kubectl config use-context kind-nke

# Install the pinned AICR binary.
bash scripts/install-aicr

# Resolve and render the quick-start Helm bundle.
.bin/aicr bundle \
  --recipe deploy/overlays/kind.yaml \
  --data deploy \
  --deployer helm \
  --output generated/aicr/quickstart/helm

# Install the generated bundle.
generated/aicr/quickstart/helm/deploy.sh --retries 1
kubectl get pods -A
```

The generated installer waits for each Helm release before continuing.

To render the observability example, use
`--recipe deploy/overlays/kind-observability.yaml`. To generate Argo CD output,
pass `--deployer argocd` and a reachable `--repo` URL to `aicr bundle`, then
publish the generated directory at the root of that repository.

## Validation

After generating a bundle, use `aicr verify` to check its checksums and
closed-world file inventory:

```bash
.bin/aicr verify generated/aicr/quickstart/helm
```

This does not cover provider qualification, scale, upgrades, performance, or
production operations.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) and the
[Code of Conduct](CODE_OF_CONDUCT.md) before contributing.

Run component checks independently:

```bash
./build/node-agent.sh test
(cd controller && make lint && make test)
```

Use `aicr verify` to check generated bundles after catalog changes.

Controller contributors currently need `GIT_CREDENTIALS_NETRC` to resolve the
remaining private Go modules. That requirement is removed once the public
dependency assumption above is satisfied.

For a local development loop, create or select the `nke` Kind cluster and run:

```bash
export DOCKER_HOST="unix://${HOME}/.colima/default/docker.sock"
kubectl config use-context kind-nke
tilt up
```

Tilt rebuilds and reloads the controller when controller sources change.
Changes under `deploy/` regenerate and apply the quick-start Helm bundle.

## Security

Report vulnerabilities according to [SECURITY.md](SECURITY.md). Do not report
security vulnerabilities through public GitHub issues.

## License

This project is licensed under the [Apache License 2.0](LICENSE).
