# nkectl

NKE control-plane CLI. Bootstraps site controllers and manages tenant cluster lifecycle.

**Module:** `github.com/NVIDIA/nke/cli`  
**Binary:** `nkectl`  
**Releases:** `cli/v*` tags

## What it does

- **Site bootstrap** — `nkectl bootstrap site` creates an ephemeral Kind cluster, deploys CWE onto it, then drives kubeadm init/join across pre-provisioned nodes via `nke-agent` gRPC; no SSH required
- **Cluster lifecycle** — `nkectl get/create/delete/describe clusters` manages tenant clusters through the CWE API
- **Config** — `nkectl config set-site` points the CLI at a site controller; `nkectl config validate` validates a bootstrap inventory file

## Installation

```bash
# linux/amd64
curl -fsSL -o nkectl \
  https://github.com/NVIDIA/nke/releases/latest/download/nkectl-linux-amd64
chmod +x nkectl && sudo mv nkectl /usr/local/bin/

# linux/arm64
curl -fsSL -o nkectl \
  https://github.com/NVIDIA/nke/releases/latest/download/nkectl-linux-arm64
chmod +x nkectl && sudo mv nkectl /usr/local/bin/

# macOS (amd64 / arm64)
curl -fsSL -o nkectl \
  https://github.com/NVIDIA/nke/releases/latest/download/nkectl-darwin-arm64
chmod +x nkectl && sudo mv nkectl /usr/local/bin/
```

```bash
nkectl version
```

## Quick reference

```bash
# Bootstrap a new site controller
NKECTL_CWE_IMAGE=nvcr.io/<org>/controller:edge \
NKECTL_CWE_PROFILES_IMAGE=nvcr.io/<org>/profiles:edge \
  nkectl bootstrap site -i inventory.yaml
nkectl bootstrap site -i inventory.yaml --dry-run              # validate only
nkectl bootstrap site -i inventory.yaml --bootstrap-provider docker  # use Docker instead of nerdctl
nkectl bootstrap site -i inventory.yaml --keep-bootstrap-cluster     # leave Kind cluster for debugging

# Point the CLI at a site controller
nkectl config set-site --url https://cwe.example.com --name default

# Tenant cluster lifecycle
nkectl get clusters                    # table with AGE and status
nkectl get clusters -o json            # JSON output
nkectl describe cluster <name>
nkectl create cluster [spec-file]      # or: cat spec.yaml | nkectl create cluster -
nkectl delete cluster <name>           # prompts [y/N]; use --yes to skip
```

## Configuration

What a bootstrap is allowed to trust describes the *site*, so it comes from the
inventory file rather than from flags or the environment. See
[`docs/inventory-spec.md`](docs/inventory-spec.md).

| Field | |
|---|---|
| `cluster.controlPlaneVIP` | Stable VIP for the Kubernetes API and the temporary SPIRE route used while nodes join. |
| `cluster.*.tpmEkThumbprint` | Which nodes may attest to SPIRE. No node listed, no node admitted; an inventory with none deploys no TPM attestor at all. |
| `spire.caCertFile` / `caKeyFile` | A CA for SPIRE to chain its own to, so a node given that certificate when it was provisioned verifies the server on first contact. Without it SPIRE is self-signed. |

### What gets installed

`bootstrap site` deploys a SPIRE server, so nodes with a hardware identity have
something to attest to and CWE gets a workload identity of its own. It comes
from the SPIFFE project's own hardened charts, pinned alongside the Postgres and
Temporal pins in [`internal/cwedeploy/cwedeploy.go`](internal/cwedeploy/cwedeploy.go)
and installed with `helm --repo`, so nothing is added to your global Helm
repository list. Everything it names is public, so a bootstrap needs no registry
credential.

Everything static about the deployment lives in
[`internal/cwedeploy/manifests/spire-values.yaml`](internal/cwedeploy/manifests/spire-values.yaml).
`nkectl` sets only what it cannot know ahead of time: the trust domain and
cluster name, CWE's admin identity, the CA to chain to, and the TPM thumbprints
allowed to attest.

Two of the values it sets have to agree with CWE and are wrong by default on
both sides, so both halves are set from one constant in
[`internal/cwedeploy/cwedeploy.go`](internal/cwedeploy/cwedeploy.go):

- **the controller-manager class.** CWE stamps its `ClusterStaticEntry` objects
  with its configured class and the SPIRE controller manager only acts on
  objects carrying its own. The chart derives `<namespace>-<release>` when the
  class is unset; CWE defaults to `spire-mgmt-spire`. Left alone they differ,
  and CWE writes entries nobody reads — with both components healthy.
- **the server port.** The chart's Service defaults to 443, while CWE dials
  `spire-server.spire-server.svc.cluster.local:8081` for join tokens. The
  Service is moved to 8081 so that default is right without configuring CWE.
  The agent's server port has to move with it: the chart keeps that as its own
  value, also defaulting to 443, and does not derive it from the Service. Move
  one without the other and the agent dials a port the Service does not
  publish, which a ClusterIP blackholes rather than refuses — so it surfaces as
  a node-attestation `i/o timeout` that names nothing useful.

The external SPIRE port is configured once. `NKECTL_SPIRE_PORT` flows to the
Kind host mapping, bootstrap NodePort, tenant LoadBalancer Service, and
EndpointSlice. The default is `30081`.

| Variable | Default | Purpose |
|---|---|---|
| `NKECTL_SPIFFE_TRUST_DOMAIN` | `<cluster>.nke.local` | Trust domain for the identities issued |

The cluster name SPIRE is told is the ephemeral cluster's own name, from
`NKECTL_BOOTSTRAP_CLUSTER_NAME` — there is no separate SPIFFE variable for it,
because the bootstrap cluster has one name and two ways to say it could only
ever disagree.

### Bootstrap cluster and CWE

| Variable | Default | Purpose |
|---|---|---|
| `NKECTL_BOOTSTRAP_PROVIDER` | `nerdctl` | Container runtime front end for the ephemeral cluster: `nerdctl` or `docker` |
| `NKECTL_BOOTSTRAP_CLUSTER_NAME` | `nkectl-bootstrap` | Name of the ephemeral cluster |
| `NKECTL_CWE_IMAGE` | required | CWE image to deploy. Side-loaded from the local image store, so it must already be pulled. |
| `NKECTL_CWE_PROFILES_IMAGE` | required | Image holding the OS profiles tree at `/profiles` |
| `NKECTL_CWE_CHART_PATH` | discovered | Path to the CWE chart |
| `NKECTL_CWE_PORT` | `30080` | CWE Service, NodePort, Kind mapping, and local client port |
| `NKECTL_SPIRE_PORT` | `30081` | SPIRE NodePort, Kind mapping, LoadBalancer Service, and EndpointSlice port |
| `NKECTL_CWE_HELM_TIMEOUT` | `5m` | Bounds each `helm upgrade --install --wait` |
| `NKECTL_CWE_READY_TIMEOUT` | `5m` | Bounds the wait for CWE's `/readyz` |
| `NKECTL_BOOTSTRAP_DIAGNOSTICS_DIR` | `cluster-diagnostics` | Where a failed bootstrap writes a cluster dump before tearing the cluster down (`--diagnostics-dir`) |

### Staged control-plane creation

`bootstrap site` creates the control plane in this order:

1. Send the complete host inventory to the provisioned infrastructure provider.
2. Create one control-plane Machine. The provider binds it to `cluster.initNode`.
3. Wait for the cluster to report `Provisioned`.
4. Route the control-plane VIP and SPIRE port to the init node with a
   selectorless LoadBalancer Service and EndpointSlice.
5. Create a second control-plane node pool for all `cluster.joinNodes`.
6. Wait until every requested control-plane node is Kubernetes-ready.
7. Delete the temporary SPIRE Service and EndpointSlice.
8. Delete the bootstrap Kind cluster.

Run `nkectl` on the init node. The command checks that `cluster.initNode.ip` is
assigned to a local interface before it creates the bootstrap Kind cluster.

## When a bootstrap fails

If bootstrap fails before site-cluster creation starts, `bootstrap site`
collects diagnostics and removes the ephemeral Kind cluster. If bootstrap
fails after site-cluster creation starts, it collects diagnostics and retains
the Kind cluster for recovery. The error shows the cleanup command.

It lands in `cluster-diagnostics/`, overridable with `--diagnostics-dir` or
`NKECTL_BOOTSTRAP_DIAGNOSTICS_DIR`. A successful run writes nothing. CI uploads
the directory as an artifact when the bootstrap E2E fails.

## Build from source

```bash
# From monorepo root
make build          # builds all binaries including cli/nkectl into bin/

# Or from this directory
make build
```

## Platform support

| OS | amd64 | arm64 |
|---|---|---|
| Linux | ✓ | ✓ |
| macOS | ✓ | ✓ |

## Related docs

- [Site Controller Bootstrap](../docs/site-bootstrap/overview.md)
- [Architecture](../docs/architecture.md)
