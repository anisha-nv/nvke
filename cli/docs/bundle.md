# The bootstrap bundle

> **Status: surface only.** `bootstrapBundleFile` is loaded, validated and resolved.
> `nkectl bootstrap site --dry-run` prints where every artifact would come
> from. The deploy does not pull through it yet. Without `--dry-run`,
> `bootstrap site` stops at a not-implemented error when `bootstrapBundleFile` is set.
> Leave it out of the site spec to bootstrap.

`nkectl bootstrap site` creates an ephemeral Kind cluster. It installs
Postgres, Temporal, SPIRE, Cluster API and the controller onto it. Two
questions about those artifacts have two owners:

| Question | Owner | Where the answer lives |
|---|---|---|
| *What* is pulled: every chart and image, with version and digest | `nkectl` | the **manifest**. nkectl embeds it. `nkectl get bootstrap-manifest` prints it. |
| *Where from*: your registry, and the two images you build | you | the **bootstrap bundle**. `sitespec.bootstrapBundleFile` names it. |

The site spec describes the cluster. The bootstrap bundle describes your side
of the supply chain, and nothing else: your registry, your two images, and any
override. It carries nothing you do not edit. An nkectl upgrade does not
change it.

## The manifest

```bash
nkectl get bootstrap-manifest
```

This prints every chart and image the bootstrap pulls, with the version and
digest nkectl was tested with. Use it to populate your registry. Use it to
find the path an override names: `<component>.chart` or
`<component>.images.<name>`.

The manifest lists what is *fetched*. The controller chart and the Postgres
chart ship inside `nkectl`. Nothing fetches them, so they have no entry. The
three upstream charts (Temporal, SPIRE, SPIRE CRDs) and every image are
fetched, and are listed.

## The bootstrap bundle

```bash
nkectl template bootstrap-bundle > bootstrap-bundle.yaml
# fill in the three values, then
nkectl validate bootstrap-bundle -f bootstrap-bundle.yaml
```

The template has three values to fill in: `registryMirror.endpoint`,
`images.controller` and `images.provisionedProvider`. Every other block is a
commented example. [examples/bootstrap-bundle.yaml](../examples/bootstrap-bundle.yaml)
is the template with the three values filled in. What your registry must hold
is the manifest: `nkectl get bootstrap-manifest` prints it.
`validate bootstrap-bundle` runs the checks `bootstrap site` runs. It names
every field that is wrong. On success it prints where every artifact would
come from. It needs no site spec and reaches no registry. Name the file in the
site spec:

```yaml
bootstrapBundleFile: "bootstrap-bundle.yaml"   # relative to the site spec file
```

### `registryMirror`: your registry

```yaml
registryMirror:
  endpoint: "registry.customer.internal"
```

With the block present, nkectl pulls **every** artifact from that registry,
the two images you build included. It pulls nothing from a public registry. A
pull the registry cannot serve fails there and names the artifact. It never
falls back to the internet.

The layout is a convention:

| Artifact | Upstream | Pulled from |
|---|---|---|
| image | `<registry>/<path>:<tag>` | `<endpoint>/<registry>/<path>:<tag>` |
| chart | `<name> <version>` from a Helm repository | `oci://<endpoint>/charts/<name>` at `--version <version>` |

So nkectl pulls `docker.io/library/postgres:18.6` as
`registry.customer.internal/docker.io/library/postgres:18.6`. It pulls `spire
0.30.1` as `oci://registry.customer.internal/charts/spire --version 0.30.1`.
Copy the manifest's entries into your registry under those names. Nothing
else needs declaring.

A registry with a different layout, such as Harbor projects per upstream,
overrides the convention:

```yaml
registryMirror:
  endpoint: "harbor.customer.internal"
  ociNamespaces:
    - registry: "docker.io"
      namespace: "dockerhub"
    - registry: "ghcr.io"
      namespace: "ghcr"
  chartsNamespace: "helm/nke"
  caCertFile: "harbor-ca.crt"
```

| Field | Required | Meaning |
|---|---|---|
| `endpoint` | yes | Your registry, as `host[:port]`. |
| `ociNamespaces[].registry` | no | An upstream registry with a non-default path. `docker.io` covers `registry-1.docker.io`. |
| `ociNamespaces[].namespace` | no | The path under `endpoint` for that registry's images. Empty means the registry root. |
| `chartsNamespace` | no | The path under `endpoint` for charts. Default `charts`. |
| `caCertFile` | no | A PEM file with the CA that signed your registry's certificate, relative to the bundle file. It must contain certificates only. |
| `plainHTTP` | no | Reach the registry over `http`. Rejected with a credential, unless `endpoint` is a loopback address. |
| `insecureSkipTLSVerify` | no | Reach the registry over `https` without verifying its certificate. Cannot be combined with `plainHTTP`. |

**One registry, one credential.** There is no per-namespace credential. The
identity you give `nkectl` must have pull access to every namespace the
manifest needs, and to the chart namespace. On the registries this targets,
identity is per host and authorization is per project. One robot account with
read across the relevant projects is the normal setup.

The registry belongs to your environment, not to a site. Use one bundle file
per environment and one site spec per site.

### Without `registryMirror`: public registries, on purpose

Delete the `registryMirror` and `images` blocks, or set no
`bootstrapBundleFile`. nkectl then pulls every artifact from its public
registry. It needs no registry of yours and no credential. This is the
development form. It reaches the internet for everything, so `bootstrap site`
takes **`--public`** to confirm that:

```bash
nkectl bootstrap site -f sitespec.yaml --public
```

nkectl refuses `--public` when the bootstrap bundle declares a registry. The
two contradict. The error names the file and the endpoint. Today the flag is
optional. A later change requires it whenever nothing declares a registry.
Pulling from the internet then always needs an explicit flag.

### `images`: the two you build

```yaml
images:
  controller: "registry.example/nke/controller:1.4.0"
  provisionedProvider: "registry.example/nke/cluster-api-provider-provisioned:1.4.0"
```

You build two images in the manifest: the controller
(`controller/Dockerfile`) and the provisioned provider
(`cluster-api-provider-provisioned/Dockerfile`).

These two images follow the same rule as every other image. With a
`registryMirror` block, nkectl pulls every image from your registry. So push
the two images there and name them here. A reference that already names your
registry is used as written. A reference with another host is pulled through
your registry by the same convention. A hostless reference is refused, by
name: it would be side-loaded, not pulled, and nothing would reference the
registry you declared.

Without a `registryMirror` block, the two default to `cwe-controller:local`
and `cluster-api-provider-provisioned:local`. Those names have no registry.
nkectl side-loads them from the local image store on the machine that runs
`bootstrap site`. For development, build the two images under those tags.
That is the whole job. A reference with a host is pulled as written. To pin
one by digest, put the digest in the reference:
`registry.example/nke/controller:1.4.0@sha256:…`.

### `overrides`: deploying something else, on purpose

Sometimes you must run an artifact nkectl was not built with: a security fix
that has not shipped, or a component your own team builds. Name the manifest
entry, give a reason, and give the replacement:

```yaml
overrides:
  spire.images.server:
    reason: "CVE-2026-1234; 1.15.4 not shipped by NKE yet"
    ref: ghcr.io/spiffe/spire-server:1.15.4
    digest: sha256:…                      # of 1.15.4, not the one it replaced
  temporal.chart:
    reason: "internal chart build"
    ref: oci://registry.example/charts/temporal   # or repo, name and version
```

An image takes `ref` and, optionally, `digest`. A chart takes `repo`, `name`
and `version` together, or a `ref`. A chart `ref` is an OCI reference or a
path relative to the bundle file. Three rules apply. nkectl refuses each
violation by name:

- **Give a reason.** An empty `reason` is refused. The reason travels with the
  file into git.
- **Change something.** An override that names what the manifest already
  names is refused.
- **Do not keep the old digest.** A changed `ref` with the old `digest` or
  `sha256` is refused. The digest would describe something no longer pulled.

An override cannot name the two images you build. Those are yours under
`images`. It cannot name the charts nkectl carries. They are not in the
manifest. `--dry-run` prints every override under `overridden (N; …)`. It
marks the row `[overridden]`. `bootstrap site` logs one warning per override.
A bootstrap bundle with no override pulls exactly what nkectl was built with.

## What the bootstrap checks

nkectl rejects unknown fields, as the site spec does. A bootstrap bundle in
an earlier shape is refused, not half-read. Then nkectl reports every problem
together. Each names its field:

```
bootstrap bundle ./bootstrap-bundle.yaml: validation failed:
  - images.controller is required with registryMirror: push the image to your registry and name it here
  - overrides.spire.images.server.reason must say why this differs from what nkectl was built with
  - registryMirror.endpoint is required: nkectl pulls nothing from public registries, so the registry the bundle's artifacts were copied into has to be named
```

## Seeing what would be pulled

```bash
nkectl bootstrap site -f sitespec.yaml --dry-run
```

This prints the site spec first. Then it prints your registry, with one line
per upstream registry and where its images live. Without a registry, it
prints one line saying so. Then it prints one line per manifest entry: the
upstream reference, the resolved location, and a note (`via mirror`, `public`,
or `built by you`). It never prints a credential. This is all the bundle does
today.

## What is not in the manifest yet

Two things are not listed yet. nkectl pulls them from public registries in
both forms: the Kind node image (`kindest/node`, from Docker Hub), and the
Cluster API core, kubeadm and cert-manager components that `clusterctl`
installs (from `registry.k8s.io` and GitHub releases). Until they are listed,
a `registryMirror` block does not mean a fully air-gapped bootstrap. They are
tracked separately.
