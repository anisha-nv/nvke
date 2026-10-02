# Deploy Path Format

A deploy path describes the NKE management plane as an ordered list of items. This page is the format. It does not cover the install procedure around a path; for that, see the [`nke-sbom` README](../../controller/deploy/nke-sbom/README.md).

> **Note:** A deploy path is a post-bootstrap concern. `nkectl bootstrap site` produces a vanilla Kubernetes cluster. A deploy path is what puts the NKE services onto it.

## A deploy path

A deploy path is a directory. It holds an `nke-deploy.yaml` and one subdirectory per item. Nothing else at its root is read.

```text
nke-sbom/
├── nke-deploy.yaml        # the item list; its order is the install order
├── nke-base-config/       # template: Secrets rendered from a BootstrapConfig
├── spire-server-nodeport/ # manifest: plain YAML
├── postgresql/            # helm: release.yaml plus values files
└── ...
```

## `nke-deploy.yaml`

The file has one key, `items`. Each entry is a flow mapping with a `name` and a `type`.

```yaml
items:
  - {name: nke-base-config, type: template}
  - {name: spire-tpm-pubhashes, type: manifest}
  - {name: capnico, type: manifest}
  - {name: postgresql, type: helm}
  - {name: spire-crds, type: helm}
  - {name: temporal, type: helm}
  - {name: spire, type: helm}
  - {name: controller, type: helm}
```

| Field | Description |
|-------|-------------|
| `name` | The item's subdirectory. For a `helm` item it is also the key an installer uses for per-item options. |
| `type` | One of `manifest`, `template`, `helm`, `argocd`. |

List order is install order. There are no phases, no enable flags and no dependency graph. An item that must come first is listed first. An item whose directory is missing, or whose type is unknown, is an error.

## Item types

### `manifest`

A directory of `.yaml` files. Each file is applied with `kubectl apply --server-side`, in lexicographic order. Numeric prefixes such as `00-` and `40-` set the order, so a Namespace lands before anything in it. A manifest item with no `.yaml` file is an error.

### `template`

A manifest item whose `.yaml.tmpl` files are rendered first. The context is a [BootstrapConfig](#bootstrapconfig-fields). Plain `.yaml` files in the same directory are copied unchanged. The rendered set is then applied like a manifest item.

The supported template syntax is field lookup only: `{{ .Path.To.Field }}`.

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

Rendering refuses rather than guesses. Each of these stops the install:

- a field the config does not define
- a field whose value is empty, or is a list or an object rather than a scalar
- any other template action: `range`, `if`, `with`, `template`, or a function call
- a file that renders to nothing
- a template item that holds no `.yaml.tmpl` file

Rendered output is written to a temporary directory, never over the source, and is removed once applied. It holds credentials.

### `helm`

One chart installed as one release. The item directory holds a `release.yaml` and the values files it names.

```yaml
chart:
  name: temporal
  version: "0.74.0"
  repository: https://go.temporal.io/helm-charts
release:
  name: temporal
  namespace: temporal
values:
  - values.yaml
  - overlay.yaml
```

| Field | Required | Description |
|-------|----------|-------------|
| `chart.name` | yes | Chart name. |
| `chart.version` | yes | Chart version, quoted so YAML keeps it a string. |
| `chart.repository` | one of the two | Where the chart is published: an `oci://` reference or a classic chart repository URL. |
| `chart.path` | one of the two | A chart directory, relative to the deploy path root. |
| `release.name` | yes | Helm release name. |
| `release.namespace` | yes | Namespace the release installs into. |
| `values` | no | Values files, relative to the item directory, in the order Helm applies them. |
| `requires` | no | Objects that must be ready before the release installs. |

`release.yaml` has exactly these four sections. A line in any other shape is an error, so a typo cannot pass as an item that does less than it claims.

**Chart source.** The source is resolved in this order:

1. An override the installer accepts for this item, for a chart the repository does not publish.
2. `chart.path`. If the chart has a `Chart.lock`, its dependencies are built before install.
3. `chart.repository` starting with `oci://`: the reference is `<repository>/<chart.name>` at `chart.version`.
4. Any other `chart.repository`: the chart is fetched by name with `--repo <repository> --version <chart.version>`.

A `release.yaml` that names neither `repository` nor `path`, or names both, is an error.

**Values layering.** The files in `values` are passed in order. Any values the installer supplies for this item come after them. Helm merges in that order, so a later file wins on every key it names. Values are per item: one release holds one chart, so a key is never prefixed with a component name.

**`requires`.** Each entry names a Kubernetes object the chart does not own and a condition it must meet. The installer checks that the object exists, then waits for the condition, then installs the release.

```yaml
requires:
  - {kind: certificate, name: oidc-provider-upstream-ca, namespace: spire-server, condition: Ready}
```

An object in `requires` is created by an earlier item in the same path. A missing object is an error that names the item which should have supplied it.

The release is installed with `helm upgrade --install <release.name> <source> --namespace <release.namespace> --wait`, with the values files appended.

### `argocd`

A directory holding an `application.yaml`: an `argoproj.io/v1alpha1` Application. It is applied with `kubectl apply --server-side`. The type is valid, and none of the deploy paths in this repository use it.

## BootstrapConfig fields

A `template` item renders against a BootstrapConfig. The installer receives it as JSON. These are the fields the templates in this repository reference. A template that names any other field fails at render.

| Field | Type | Description |
|-------|------|-------------|
| `.ClusterName` | string | The site's name, exported as `k8s.cluster.name` by site telemetry. |
| `.Nico.Endpoint` | string | NICo API base URL. |
| `.Nico.OrgID` | string | NICo organization ID. |
| `.Nico.Token` | string | NICo API token. |
| `.Nico.APIName` | string | NICo API name, for example `forge`. |
| `.Registry.Server` | string | Registry host for the CAPNICo image pull Secret. |
| `.Registry.Username` | string | Registry username. |
| `.Registry.Token` | string | Registry token or password. |
| `.Tenant.User` | string | Login the controller writes into every tenant node's cloud-init. |
| `.Tenant.Password` | string | Password for that login. |

## The deploy paths in this repository

All three live under `controller/deploy/`.

| Path | Purpose |
|------|---------|
| `nke-sbom` | A NICo site controller on real hardware, with CAPNICo, SPIRE and site telemetry. |
| `capnico-e2e` | The CAPNICo end-to-end lane. CAPNICo itself is installed outside the path. |
| `capd-e2e` | The CAPD end-to-end lane, with the test doubles that lane needs. |

## How a path is applied

`build/use-nke-sbom.sh` walks `nke-deploy.yaml` and applies each item in order, using `kubectl` and `helm`. A `template` item needs `--config <BootstrapConfig.json>`.

```bash
build/use-nke-sbom.sh install --sbom controller/deploy --target nke-sbom --config site.json
```

The script is transitional. `nkectl` is to install a deploy path directly, from a local directory or an OCI registry; see [issue #873](https://github.com/dsx-ai-factory/nke/issues/873).

The format does not fix the delivery mechanism. An operator can apply the items with Helm and `kubectl` directly, or have a GitOps tool consume the same items. What the format fixes is the content: which charts, which values, which Secrets, in which order.
