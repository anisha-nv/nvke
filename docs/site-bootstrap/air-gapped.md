# Air-gapped bootstrap

`nkectl bootstrap site` fetches from the internet by default: four Helm charts,
the Cluster API provider manifests, cert-manager, and the container images all
of those run. Every one of them can instead be read from a directory or a
registry you control. This page is the list of what to stage, the variables
that point the bootstrap at it, and what still has to come from somewhere.

The host prerequisites — `nkectl`, `helm`, `kubectl`, `clusterctl`, `kind`,
`nerdctl` — are yours to install; nothing here fetches a binary.

## What the bootstrap pulls, and from where

| Artifact | Fetched from, by default | Staged with |
|---|---|---|
| Postgres chart | `oci://registry-1.docker.io/bitnamicharts/postgresql` | `hack/stage-bootstrap-artifacts.sh` |
| Temporal chart | `go.temporal.io/helm-charts` index → `github.com` release asset | `hack/stage-bootstrap-artifacts.sh` |
| SPIRE and SPIRE CRDs charts | `spiffe.github.io/helm-charts-hardened` index → `github.com` release assets | `hack/stage-bootstrap-artifacts.sh` |
| Cluster API core and kubeadm providers | `github.com/kubernetes-sigs/cluster-api` releases, via `clusterctl init` | `hack/stage-bootstrap-artifacts.sh` |
| cert-manager | `github.com/cert-manager/cert-manager` releases, via `clusterctl init` | `hack/stage-bootstrap-artifacts.sh` |
| Images those releases run | Docker Hub, `ghcr.io`, `registry.k8s.io`, `quay.io`, `cgr.dev` | `hack/mirror-bootstrap-images.sh` |
| CWE, profiles and provisioned-provider images | side-loaded from the host's image store | pull them on the host |
| CWE chart | the checkout, or `NKECTL_CWE_CHART_PATH` | already local |

The Temporal and SPIRE rows are why a mirror of the chart *repository* is not
enough: the repository index is readable from anywhere it is mirrored to, but
every entry in it names a `github.com` download URL for the chart itself.
Helm reads the index, then fetches the tarball from there. So the charts have
to be staged as files, and the bootstrap pointed at the files.

## Stage

On a host with internet access:

```bash
hack/stage-bootstrap-artifacts.sh ./bootstrap-artifacts
hack/mirror-bootstrap-images.sh mirror.example.com:5000
```

The first writes the charts and a clusterctl local repository under
`./bootstrap-artifacts` and prints the variables to export. The second copies
every image the releases run into your registry, each under the same path it
has upstream — `ghcr.io/spiffe/spire-server` becomes
`mirror.example.com:5000/spiffe/spire-server` — which is the layout the
bootstrap cluster's mirror configuration expects. Move the directory across the
gap; the registry is reachable from the air-gapped side by construction.

The chart versions, and the Cluster API version, are read from the tree by the
staging script, so they match the `nkectl` built from the same checkout. Helm
does not compare a local tarball against the pinned version, so a tarball
staged from a different checkout installs without complaint.

## Point the bootstrap at it

```bash
export NKECTL_CWE_POSTGRES_CHART=/path/to/bootstrap-artifacts/charts/postgresql-<version>.tgz
export NKECTL_CWE_TEMPORAL_CHART=/path/to/bootstrap-artifacts/charts/temporal-<version>.tgz
export NKECTL_CWE_SPIRE_CHART=/path/to/bootstrap-artifacts/charts/spire-<version>.tgz
export NKECTL_CWE_SPIRE_CRDS_CHART=/path/to/bootstrap-artifacts/charts/spire-crds-<version>.tgz
export NKECTL_CAPI_LOCAL_REPO=/path/to/bootstrap-artifacts/capi
export NKECTL_BOOTSTRAP_REGISTRY_MIRROR=mirror.example.com:5000
```

The staging script prints these with the versions filled in. Then run
`nkectl bootstrap site` as in [first-cluster.md](../first-cluster.md).

Each chart variable is an OCI reference or a local path, so a registry that
serves charts can stand in for the tarballs. `NKECTL_CAPI_LOCAL_REPO` is a
[clusterctl local repository](https://cluster-api.sigs.k8s.io/clusterctl/configuration):
`nkectl` writes a clusterctl configuration pointing every provider and
cert-manager at `file://` URLs inside it and passes it as `--config`, so
`clusterctl init` reads from disk and still resolves the `${...}` placeholders
in the provider manifests, which a plain `kubectl apply` would leave as text.
`NKECTL_BOOTSTRAP_REGISTRY_MIRROR` writes a containerd `hosts.toml` for each
upstream registry on the bootstrap cluster's node, so every pull goes to the
mirror; it is a bare `host[:port]`, and a mirror that does not answer fails the
pull rather than falling back upstream.

## What it does not cover

- **The site's own nodes.** This page is about the ephemeral bootstrap cluster.
  The hosts it provisions have their own package and image needs; see
  [pre-provisioned-nodes.md](pre-provisioned-nodes.md).
- **Anything reached over plain HTTP.** The mirror is dialled as
  `https://<mirror>`. A registry on loopback over HTTP suits
  `hack/mirror-bootstrap-images.sh`'s default but not the bootstrap cluster.
- **Binaries.** `clusterctl`, `helm`, `kind` and the rest are pulled by you.
