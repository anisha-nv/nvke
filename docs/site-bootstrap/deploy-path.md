# Deploy Path

A deploy path is a directory that says what to install on a site cluster and in which order. `deploy/nke-deploy.sh` reads it and applies it. [Install the Site Controller](install-site-controller.md) runs that executor as its stage 5; this page is the format reference.

Installing the deploy path is a post-bootstrap step. `nkectl bootstrap site` brings up the Kubernetes cluster. The deploy path puts the NKE management plane onto it.

## Where the deploy paths live

| Path | For |
|---|---|
| [`deploy/nico/`](https://github.com/dsx-ai-factory/nke/tree/main/deploy/nico) | Customers. A NICo site controller on real hardware, with SPIRE. Its [README](https://github.com/dsx-ai-factory/nke/blob/main/deploy/nico/README.md) explains the item order |
| `controller/deploy/capd-e2e/` | The CAPD end-to-end lane. Internal |
| `controller/deploy/capnico-e2e/` | The CAPNICo end-to-end lane against the NICo simulator. Internal |

All three are installed by the same executor.

## Layout

```text
deploy/nico/
├── nke-deploy.yaml              # the item list; the order is the install order
├── nke-base-config/             # template: namespaces, Secrets, the OIDC CA chain
│   ├── 00-namespaces.yaml
│   ├── 20-nico-credentials.yaml.tmpl
│   ├── 30-ghcr-imagepull.yaml.tmpl
│   ├── 35-dgxc-tenant-credentials.yaml.tmpl
│   ├── 40-oidc-upstream-ca.yaml
│   └── 45-nktp-cluster.yaml.tmpl
├── spire-tpm-pubhashes/         # manifest
│   └── spire-tpm-pubhashes.yaml
├── capnico/                     # manifest: the pinned CAPNICo release asset
│   └── capnico.yaml
├── postgresql/                  # helm
│   ├── release.yaml
│   └── values.yaml
├── spire/                       # helm
│   ├── release.yaml
│   ├── values.yaml
│   └── overlay.yaml
└── ...
```

Each item is a directory with the item's name, beside `nke-deploy.yaml`. Files at the root other than `nke-deploy.yaml` are not read.

## `nke-deploy.yaml`

```yaml
items:
  - {name: nke-base-config, type: template}
  - {name: spire-tpm-pubhashes, type: manifest}
  - {name: capnico, type: manifest}
  - {name: nktp, type: helm}
  - {name: postgresql, type: helm}
  - {name: spire-crds, type: helm}
  - {name: temporal, type: helm}
  - {name: spire, type: helm}
  - {name: controller, type: helm}
```

| Field | Required | Description |
|---|---|---|
| `name` | Yes | The item's directory name |
| `type` | Yes | `manifest`, `template`, `helm` or `argocd` |

The list is the order. There are no phases and no enable flags. Each entry is written in flow style on one line, as above; the parser, `deploy/lib/nke-deploy-items.py`, accepts nothing else and fails on any line it does not recognise. Comments and blank lines are fine.

## Item types

### `manifest`

Every `.yaml` file in the directory is applied with `kubectl apply --server-side`, in name order. Use numeric prefixes (`00-`, `10-`) to order files within the item. A directory with no `.yaml` file is an error.

### `template`

Like `manifest`, with `.yaml.tmpl` files rendered first. The executor renders them with `deploy/lib/render-template.py` against the BootstrapConfig JSON given as `--config`, into a temporary directory, and applies the result. Plain `.yaml` files in the same directory are applied as they are. The rendered files hold credentials, so nothing is written back into the deploy path and the temporary directory is removed after the apply.

This is how a Secret with a real credential is created without committing it.

#### Template syntax

The templates use Go `text/template` field syntax. The renderer supports field lookups only: `{{ .Path.To.Field }}`, with or without `-` trim markers. `range`, `if`, `with` and function calls are refused by name. A field that is missing from the config, empty, or not a scalar is an error that names the file and the field. A file that renders to nothing is an error.

#### BootstrapConfig fields

`deploy/lib/describe_site.py` writes `bootstrap-config.json` from `site-config.yaml`. These are its fields, and the templates in `deploy/nico/nke-base-config/` use all of them.

| Expression | From `site-config.yaml` | Used by |
|---|---|---|
| `.ClusterName` | `site.name` | `45-nktp-cluster.yaml.tmpl`, the `nktp-cluster` ConfigMap NKTP labels telemetry with |
| `.Nico.Endpoint` | `nico.endpoint` | `20-nico-credentials.yaml.tmpl`, the Secret CAPNICo authenticates to the NICo API with |
| `.Nico.OrgID` | `nico.orgId` | same |
| `.Nico.Token` | `nico.token` | same |
| `.Nico.APIName` | `nico.apiName` | same |
| `.Registry.Server` | `registry.server` | `30-ghcr-imagepull.yaml.tmpl`, the pull secret for the CAPNICo image |
| `.Registry.Username` | `registry.username` | same |
| `.Registry.Token` | `registry.token` | same |
| `.Tenant.User` | `tenant.user` | `35-dgxc-tenant-credentials.yaml.tmpl`, the login the controller renders into every tenant node's cloud-init |
| `.Tenant.Password` | `tenant.password` | same |

A template that names another field needs that field in the JSON. Add it to the config the executor is given, or the render fails.

Example, `20-nico-credentials.yaml.tmpl`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: nico-credentials
  namespace: capnico-system
type: Opaque
stringData:
  endpoint: "{{ .Nico.Endpoint }}"
  orgID: "{{ .Nico.OrgID }}"
  token: "{{ .Nico.Token }}"
  apiName: "{{ .Nico.APIName }}"
```

### `helm`

One chart installed as one release with `helm upgrade --install --wait`. The directory holds `release.yaml` and the values files it names.

```yaml
chart:
  name: spire
  version: "0.30.1"
  repository: https://spiffe.github.io/helm-charts-hardened/
release:
  name: spire
  namespace: spire-server
values:
  - values.yaml
  - overlay.yaml
requires:
  - {kind: certificate, name: oidc-provider-upstream-ca, namespace: spire-server, condition: Ready}
```

| Field | Required | Description |
|---|---|---|
| `chart.name` | Yes | The chart name |
| `chart.version` | Yes | The chart version. Quoted. Reported, and compared with the release manifest by Check Readiness |
| `chart.repository` | One of the two | Where the chart is published: an `oci://` reference or a classic `index.yaml` repository URL |
| `chart.path` | One of the two | A chart directory, relative to the deploy path. Its `Chart.lock` dependencies are built before the install |
| `release.name` | Yes | The Helm release name |
| `release.namespace` | Yes | The release namespace. The namespace must exist; an earlier item creates it |
| `values` | No | Values files in the item directory, applied in order |
| `requires` | No | Objects that must exist and reach a condition before the install: `kind`, `name`, `namespace`, `condition`. The executor runs `kubectl wait` on each. `helm --wait` waits on workloads only, so a Certificate needs this |

Values are applied in order: the item's `values` files, then every `--values-for ITEM=FILE` the caller passes for that item. Helm merges `-f` in order, so the caller's file wins on any key it names. This is how per-site values reach one chart without touching the deploy path.

### `argocd`

The directory holds `application.yaml`, an `argoproj.io/v1alpha1` Application. The executor applies it with `kubectl apply --server-side`. Nothing in the three deploy paths uses this type; it is for a deploy path that hands items to Argo CD. See [Extend to GitOps](install-site-controller.md#extend-to-gitops).

## How the executor processes a deploy path

```text
nke-deploy.sh install --deploy-path DIR [--release-manifest PATH] [--chart-cache DIR]
    [--config BootstrapConfig.json] [--values-for ITEM=FILE]... [--chart-override ITEM=PATH]...
    [--manifest-override ITEM=DIR]... [--kubeconfig PATH] [--timeout 15m] [--dry-run] [-- helm args]
```

1. Parse `nke-deploy.yaml`. Every item must have a directory.
2. For each item, in list order:
   - `manifest`: apply the directory's `.yaml` files. With `--manifest-override ITEM=DIR`, apply `DIR` instead: `kubectl kustomize DIR` when it holds a `kustomization.yaml`, else its `.yaml` files.
   - `template`: render against `--config`, apply, remove the rendered files.
   - `argocd`: apply `application.yaml`.
   - `helm`: resolve the chart, wait for `requires`, then `helm upgrade --install`.
3. Stop at the first failure. Items already applied stay; a rerun applies every item again, and each apply is idempotent.

The chart for a `helm` item comes from the first of these that applies:

1. `--chart-override ITEM=PATH`. The caller built or fetched the chart.
2. The release manifest item of the same name, with `--release-manifest`. The chart is pulled from the item's OCI reference with `oras` into the chart cache, and the sha256 of the file is compared with the chart layer digest in that reference's manifest. A mismatch is an error and nothing is installed for the item.
3. `chart.path` in the deploy path, with `helm dependency build` first.
4. `chart.repository`.

`--dry-run` prints every kubectl and helm argv instead of running it, so the full install can be read before it is run. `--timeout` is the `helm --wait` timeout per release; the default is 15 minutes. Everything after `--` is passed to every `helm upgrade --install` verbatim.

## Adding an item

1. Create a directory under the deploy path with the item's name.
2. Add its files: manifests, templates, or `release.yaml` plus values.
3. Add `- {name: <name>, type: <type>}` to `nke-deploy.yaml` at the position the install order needs.
4. For a `helm` item that the release manifest should pin, add an item of the same name to `release-manifest.yaml` with the same `version`.
