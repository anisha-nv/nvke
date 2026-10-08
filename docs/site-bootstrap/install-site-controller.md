# Install the Site Controller

`nkectl bootstrap site` leaves a plain Kubernetes cluster. This page installs the management plane onto it: Postgres, Temporal, SPIRE, [CAPNICo](https://github.com/dsx-ai-factory/cluster-api-provider-nico), NKTP and the NKE controller.

The install takes three inputs.

| Input | Says |
|---|---|
| The deploy path, [`deploy/nico/`](https://github.com/dsx-ai-factory/nke/tree/main/deploy/nico) | What to install and in which order. See [Deploy Path](deploy-path.md) |
| `release-manifest.yaml` | Which published artifacts to use, each by OCI reference and digest. See [Release Manifest](#release-manifest) |
| `site-config.yaml` | What is specific to this site. See [Describe Your Site](#3-describe-your-site) |

`site-config.yaml` is the interim site input. Its contents belong in the site spec, and folding them in is the intended end state. Until then it is a separate file.

The install has seven stages, numbered 0 to 6. Each stage has a `run` step and a `check` step. Each `run` is safe to repeat. You can stop after any stage and resume later. The scripts live in [`deploy/`](https://github.com/dsx-ai-factory/nke/tree/main/deploy): `install.sh` runs every stage, `stages/0N-name.sh` runs one. The [deploy README](https://github.com/dsx-ai-factory/nke/blob/main/deploy/README.md) lists every environment variable they read.

```bash
export SITE_KUBECONFIG=./my-site.kubeconfig
export RELEASE_FILE=./release-manifest.yaml
export SITE_CONFIG=./site-config.yaml
deploy/install.sh
```

`--from NAME` resumes at a stage. `--until NAME` stops after one. `NAME` is the script name without its number, for example `--from install-cluster-api`. `--dry-run` prints every kubectl and helm argv and changes nothing.

## Release Manifest

`release-manifest.yaml` names the published artifacts the install pulls. It has the shape of `nke-deploy.yaml`: a flat `items` list, one entry per artifact, named after the deploy item it serves. `ref` carries the digest, so there is no separate checksum file.

```yaml
apiVersion: nke.nvidia.com/release/v1
release: ghcr.io/example/nke-recipes/releases@sha256:<index>
lane: pipeline-attested
scope: artifact-publication
source:
  repo: https://github.com/dsx-ai-factory/nke
  tag: controller/v0.1.1
  commit: <sha>
items:
  - {name: controller, type: image,  ref: ghcr.io/example/nke-recipes/cwe@sha256:<manifest>}
  - {name: postgresql, type: chart,  ref: ghcr.io/example/nke-recipes/postgresql@sha256:<manifest>, version: "18.5.15"}
  - {name: spire-crds, type: chart,  ref: ghcr.io/example/nke-recipes/spire-crds@sha256:<manifest>, version: "0.6.1"}
  - {name: temporal,   type: chart,  ref: ghcr.io/example/nke-recipes/temporal@sha256:<manifest>,   version: "0.74.0"}
  - {name: spire,      type: chart,  ref: ghcr.io/example/nke-recipes/spire@sha256:<manifest>,      version: "0.30.1"}
  - {name: nkectl,     type: binary, ref: ghcr.io/example/nke-recipes/nkectl@sha256:<manifest>}
```

| Field | Meaning |
|---|---|
| `release` | The OCI reference of the Release the manifest came from. Informational |
| `lane`, `scope` | How the artifacts were verified. Check Readiness prints both. A hand-written manifest says `lane: local-assembly` and `scope: none` |
| `source` | The repository, tag and commit the deploy path comes from. The checkout must be at `source.tag` |
| `items[].name` | The deploy item in `nke-deploy.yaml` the artifact serves. The names are equal |
| `items[].type` | `image`, `chart` or `binary` |
| `items[].ref` | Where to pull from, by digest. The registry can be public, a mirror or private. The installer does not distinguish |
| `items[].version` | Charts only. Must equal the pin in that item's `release.yaml` at `source.tag` |

The controller image and the four external charts are pinned here. The controller and NKTP charts are not: they are installed from the checkout at `source.tag`.

There are two ways to get the file.

- **From an NKD Release.** `nkdctl release verify` checks the signature and provenance of a Release and prints its artifacts with references. The converter `deploy/lib/nkd-release-to-nke.py` turns that output into this file. NKD's description carries no source pin and no chart versions, so you add them. With `RELEASE_REF` set instead of `RELEASE_FILE`, Check Readiness does both; it also needs `RELEASE_SOURCE_REPO`, `RELEASE_SOURCE_TAG` and `RELEASE_CHART_VERSIONS`.
- **By hand.** Build the artifacts, push them to a registry the nodes reach, and write the file with those refs. See [Artifacts](#artifacts). An air-gapped site points every ref at its own registry. [`deploy/release-manifest.example.yaml`](https://github.com/dsx-ai-factory/nke/blob/main/deploy/release-manifest.example.yaml) shows the shape.

## Before You Begin

| Requirement | Notes |
|---|---|
| The site cluster kubeconfig | Written by `nkectl bootstrap site`. Every stage takes it through `SITE_KUBECONFIG` |
| A release manifest | `release-manifest.yaml` through `RELEASE_FILE`, or an NKD Release through `RELEASE_REF` |
| An odd node count | One, three or five nodes, all control-plane |
| The bootstrap site spec | You need its `spire.trustDomain`, `spire.caCertFile` and `spire.caKeyFile` values. See [Configuration](configuration.md) |
| A full checkout of this repository | At `source.tag` from the manifest. The controller chart and the NKTP chart are installed from the tree |
| `helm` `v4.2.4` | The version `make/tool-versions.mk` pins. Helm 3 cannot install these charts |
| `oras` `1.3.3` | The version `make/tool-versions.mk` pins. Install the Site Controller pulls the external charts with it |
| `kubectl`, `clusterctl` `v1.12.3`, `python3` | `clusterctl` matches `CAPI_VERSION` in `make/tool-versions.mk` |
| Root shell access to every node | Stage Images pulls through each node's containerd |
| `nkectl` | For the final stage. See [Download and Install nkectl](installation.md) |
| Egress | The registries your refs point at, reachable from the nodes. The nodes also pull the images the charts, the CAPNICo manifest and the Cluster API providers run, and `clusterctl init` fetches from `github.com`. Install the Site Controller builds the NKTP chart dependencies on the machine running the install, from `kubernetes-sigs.github.io`, `open-telemetry.github.io` and `prometheus-community.github.io`. An air-gapped site mirrors those images and vendors `nktp/deploy/charts/nktp/charts/` first |

## Artifacts

Build three artifacts from the clone at `source.tag`.

| Artifact | How |
|---|---|
| NKE controller image | `make -C cli images` builds `controller/Dockerfile` for the host architecture. Push it to a registry the nodes reach. Its ref is the `controller` item in the manifest |
| Profiles image | Start from `controller/profiles/example`. See [Supply OS and config profiles](../../controller/profiles/README.md). Push it to a registry the nodes reach and name it in `images.profiles` |
| `nkectl` | `make build`, or [Download and Install nkectl](installation.md) |

Pull the four external charts at the versions each item's `release.yaml` pins, with `helm pull`, and push them to a registry the nodes reach with `helm push` or `oras push`. Put each ref and version in the manifest.

The cluster pulls the rest itself: the CAPNICo image the pinned manifest at `deploy/nico/capnico/capnico.yaml` names, cert-manager and Cluster API through `clusterctl init`, and the images the charts run.

## 0. Check Readiness

Confirm the tools, the checkout, the release manifest, egress, the node count and the architecture before anything changes. With `RELEASE_REF` set, the stage runs `nkdctl release verify` first and writes `release-manifest.yaml` into `OUT_DIR`. `run` and `check` are the same report.

```bash
deploy/stages/00-check-readiness.sh check
```

### Check

The manifest parses; its lane and scope are printed, and a lane other than `pipeline-attested` is a warning. The checkout is at the tag the manifest names, and every chart version in the manifest equals the pin in the deploy path's `release.yaml`. Each registry host in the manifest answers from a node when `NODE_EXEC` says how to reach one. `kubectl`, `helm`, `clusterctl`, `oras`, `python3` and `curl` are on `PATH`. Only `helm` is compared with its pin, and a mismatch is a warning. The checkout holds `deploy/nico/nke-deploy.yaml` and the controller chart. An egress host the runner cannot reach is a warning, because the nodes do the pulling. The node count is odd. Mixed node architectures are a warning. A warning names API authentication as off. With `NODE_EXEC` set, each node's image filesystem is measured, and less than 20% free is a warning: Stage Images pulls onto that filesystem, and the kubelet garbage-collects images under disk pressure.

### If It Fails

The report names the missing item. Fix it and run the stage again. Nothing on the cluster has changed. A version mismatch names the item, the version in the manifest and the pin in `release.yaml`. Move the checkout to `source.tag`, or fix the manifest.

## 1. Prepare the Site Cluster

Confirm the nodes are schedulable and give the cluster a default StorageClass.

```bash
deploy/stages/01-prepare-cluster.sh run
deploy/stages/01-prepare-cluster.sh check
```

### Choices

Every node is a control-plane node. `nkectl bootstrap site` removes the control-plane `NoSchedule` taint once every node has joined (`cluster.schedulableControlPlane`, default `true`). Nothing in later stages tolerates that taint, so the stage removes it from any node that still carries it. A cluster with worker nodes keeps its control-plane taint.

A bare site ships no StorageClass. Postgres and SPIRE need one. The stage installs rancher local-path-provisioner `v0.0.31`, fetched from its GitHub release; `LOCAL_PATH_VERSION` changes the pin. It stores data on one node's disk, so it is single-node durability. A site with real storage should use its own CSI driver instead: set `SKIP_STORAGECLASS=1` and mark your own StorageClass default before the check.

### Check

No node carries `node-role.kubernetes.io/control-plane:NoSchedule`, or the cluster has worker nodes. Exactly one StorageClass is marked default. When local-path-provisioner is installed, its Deployment is Available.

### If It Fails

Two default StorageClasses fail the check. Remove the `storageclass.kubernetes.io/is-default-class` annotation from one. The taint removal touches only nodes that still carry the taint, so a rerun is safe.

## 2. Stage Images

Pull the controller image onto every node before anything schedules, and pre-pull the external images as a best effort.

```bash
deploy/stages/02-stage-images.sh run
deploy/stages/02-stage-images.sh check
```

The controller image is pulled by digest from its ref in the manifest, through each node's containerd, into the `k8s.io` namespace, with `ctr -n k8s.io images pull --hosts-dir /etc/containerd/certs.d`. The node's `hosts.toml` mirrors and plain-HTTP registries apply. This pull is required; a failure ends the stage. The external images are pre-pulled the same way: the images the charts run, the profiles image among them, the CAPNICo image and the Cluster API provider images, as listed in `OUT_DIR/images.txt`. A failed external pull is a warning, because the kubelet pulls that image itself when the pod schedules. `PREPULL_EXTERNAL=0` skips the external pre-pull.

Only the node's containerd knows the site's mirrors, so the pull happens there and not on the machine running the install. The script reaches a node through `NODE_EXEC`, a command prefix with `{node}` in it: unset when the script runs on the node, `ssh root@{node}` for hosts, `nerdctl exec -i {node}` for container nodes. `NODE_MAP` renames nodes as `name=target` pairs when the node name is not the address.

Every pulled image lands on the node's image filesystem. Under the kubelet's eviction threshold the node goes `DiskPressure` and the kubelet garbage-collects unused images, the staged controller image among them. The stage reads each node's headroom first and warns under 20% free. Set `PREPULL_EXTERNAL=0` on such nodes.

`images.txt` is written by Describe Your Site, so a first run of this stage pulls only the controller image. `install.sh` runs this stage again after Describe Your Site to pre-pull the external images, and once more, controller image only, right before Install the Site Controller. Running the stages by hand, run `02-stage-images.sh run` again after Describe Your Site.

### Check

`ctr -n k8s.io images ls` on each node lists the controller image at the digest in the manifest and every pre-pulled image. The check prints each digest. A missing or stale controller image fails the check. A missing external image is a warning. Before `images.txt` exists, the external images are reported as not written yet.

### If It Fails

A failed controller pull names the registry that refused and the node. Make that registry reachable from the node, or point the ref at one that is. A failed external pull is a warning with the same names; fix the registry, or let the kubelet pull it when the pod schedules. The namespace is `k8s.io`, not `default`. An image in the wrong namespace is invisible to the kubelet. A disk warning names the node and its free share. Free space on the node or set `PREPULL_EXTERNAL=0`.

## 3. Describe Your Site

Write one `site-config.yaml` and render it into the values and configuration the install needs.

```bash
cp deploy/site-config.example.yaml ./site-config.yaml   # then fill in every REPLACE_* value
deploy/stages/03-describe-site.sh run
deploy/stages/03-describe-site.sh check
```

The file holds credentials. Keep it out of git. The stage runs `deploy/lib/describe_site.py`. It writes to `OUT_DIR` with mode `0700`, prints a redacted summary, and writes no Secret to disk. It needs no cluster. The schema is unstable and changes when the site description is folded into the site spec.

The stage writes one values file per chart (`controller.yaml`, `spire.yaml`, `postgresql.yaml`, `temporal.yaml`, and `nktp.yaml` when the config has an `nktp:` block), `bootstrap-config.json` for the template item, and `site.env` with the site name and VIP. It also writes two inventory files. `images.txt` lists every external image: the images the charts run with these values, the CAPNICo image and the Cluster API provider images. The chart images come from `helm template`, so this step needs `helm` and network access to the chart sources; `IMAGES_TXT_SKIP=1` skips it and lists only the static entries. `version-bom.cdx.json` is a CycloneDX inventory with one component per manifest item, keyed by deploy item and digest.

| Key | Feeds | Rule |
|---|---|---|
| `site.name` | Release labels, telemetry cluster name, `set-site` name | Required |
| `site.trustDomain` | SPIRE, controller `spiffe.trustDomain` | Required. Must equal `spire.trustDomain` in the bootstrap site spec. Never derived from the name |
| `site.vip` | SPIRE server address, OIDC discovery URL, IP SANs, the `set-site` URL | Required |
| `site.arch` | TPM attestor server checksum | Required. `amd64` or `arm64` |
| `spire.caCertFile`, `spire.caKeyFile` | SPIRE upstream authority | Required. The same files the bootstrap site spec names. Relative paths resolve from the config file |
| `db.password` | Postgres, both Temporal datastores, the controller database | Optional. Generated once and reused from `OUT_DIR` on rerun |
| `images.controller` | Controller chart | Optional. Defaults to the `controller` ref in the manifest. Set it only to override |
| `images.profiles` | Controller chart | Required. Your profiles image, in a registry the nodes reach |
| `nico.endpoint`, `nico.orgId`, `nico.token`, `nico.apiName` | `nico-credentials` Secret | Required. `nico.apiName` is the NICo API name, for example `forge` |
| `registry.server`, `registry.username`, `registry.token` | `ghcr-imagepull` Secret | Required. A fine-grained token with `read:packages` is enough |
| `registry.mirror` | Postgres, Temporal and SPIRE values; the CAPNICo kustomization | Optional. A registry host, with an optional path and no scheme, that serves the chart images under their upstream paths |
| `tenant.user`, `tenant.password` | `dgxc-tenant-credentials` Secret | Required. The login the controller renders into every tenant node's cloud-init |
| `nktp` | The `nktp` release values | Optional. Copied to helm as written. Without it telemetry exports to the chart's bundled development backend |

The trust domain and the CA are the two values that tie the permanent SPIRE server to the bootstrap. Every node's SPIRE agent already trusts the bootstrap CA and carries the bootstrap trust domain. Reusing both lets the agents move to the permanent server. Changing either changes every SPIFFE ID and strands the agents. See [A SPIFFE trust domain](../first-cluster.md#real-site).

### Check

The rendered files are complete. No referenced key is empty. When a Postgres Secret already exists on the cluster, the rendered password matches it.

### If It Fails

The renderer names the empty or missing key. A password mismatch means `OUT_DIR` was deleted after an install. The live value is key `postgres-password` of Secret `postgres-postgresql` in namespace `postgres`; the rendered one is `OUT_DIR/db-password`. Restore `OUT_DIR`, or set `db.password` to the live value.

## 4. Install Cluster API

Install cert-manager, Cluster API core and the kubeadm providers at the version this repository pins.

```bash
deploy/stages/04-install-cluster-api.sh run
deploy/stages/04-install-cluster-api.sh check
```

The stage runs `clusterctl init` with `--core`, `--bootstrap` and `--control-plane` pinned to `CAPI_VERSION` from `make/tool-versions.mk`, and `--wait-providers`. cert-manager comes with it; the `clusterctl` binary decides its version, so the binary must match `CAPI_VERSION`. Cluster API comes first because CAPNICo needs its CRDs, the SPIRE item waits on a cert-manager Certificate, and the controller reads the Machine CRDs once at start. [Infrastructure Providers](infrastructure-providers.md) explains the pin and the ordering.

### Check

The Deployments in `capi-system`, `capi-kubeadm-bootstrap-system`, `capi-kubeadm-control-plane-system` and `cert-manager` are Available. The `clusters.cluster.x-k8s.io` CRD is Established. The Cluster API image tag equals `CAPI_VERSION`. A cert-manager tag other than `CERT_MANAGER_VERSION` is a warning.

### If It Fails

A hang on cert-manager means the control-plane taint is still present on a cluster without worker nodes. Run Prepare the Site Cluster again. `clusterctl init` skips providers that are already installed, so a rerun is safe. A Cluster API already installed at another version is an error: `clusterctl init` does not upgrade. See [Tenant Cluster Kubernetes Upgrades](../operations/upgrade.md).

## 5. Install the Site Controller

Apply the deploy items in list order: base configuration, SPIRE node ports, CAPNICo, then the NKTP, Postgres, SPIRE CRDs, Temporal, SPIRE and controller releases.

```bash
deploy/stages/05-install-site-controller.sh run
deploy/stages/05-install-site-controller.sh check
```

The stage is one call to the executor, `deploy/nke-deploy.sh install`, with the deploy path, the release manifest, `--config OUT_DIR/bootstrap-config.json` and one `--values-for` per values file Describe Your Site rendered. The executor walks `nke-deploy.yaml` and applies each item in turn: manifests with `kubectl apply --server-side`, the `nke-base-config` templates rendered with `deploy/lib/render-template.py` and then applied, and each helm item as one release with `helm upgrade --install --wait`. The `spire` item waits on the `oidc-provider-upstream-ca` Certificate first. [Deploy Path](deploy-path.md) describes the format.

The four external charts, Postgres, SPIRE CRDs, Temporal and SPIRE, are pulled from their refs in the manifest with `oras` into `OUT_DIR/charts/<item>/<chart>-<version>.tgz`. The ref names a manifest; the sha256 of the pulled file must equal the digest of the chart layer in that manifest before the release is installed from the file. The controller and NKTP charts are installed from the tree; the NKTP chart's dependencies are built first with `helm dependency build`.

Before the install, the stage runs the Stage Images check once more. `install.sh` also pulls the controller image again, controller image only (`PREPULL_EXTERNAL=0`), right before this stage. Running the stages by hand, run `PREPULL_EXTERNAL=0 deploy/stages/02-stage-images.sh run` first.

### Choices

**CAPNICo route.** This stage applies the pinned manifest at `deploy/nico/capnico/capnico.yaml`. `CAPNICO_IMAGE=repository:tag` repoints it at a mirror or a locally built provider. The Helm and `clusterctl` routes in [Infrastructure Providers](infrastructure-providers.md) are whole replacements for this item, not alternatives inside it. Mixing routes gives two owners for one set of objects.

**Security posture.** The controller runs as root with a writable root filesystem. The `materialize-profiles` init container copies the profiles image into a shared volume and needs that. Restore a non-root design before production use.

### Check

Two checks run. The provider check: the four CAPNICo CRDs are Established, the `capnico-system/controller-manager` Deployment is Available, and the `nico-credentials`, `ghcr-imagepull` and `dgxc-tenant-credentials` Secrets exist. The plane check: every release in `helm list -A` is `deployed`, every helm item of the deploy path has its release, each chart the manifest pins runs the pinned version, the `nke-controller/nke-controller` Deployment is Available, `/readyz` reports the database and Temporal as ok, and the controller log has no `NicoMachine CRD not installed` warning. `/readyz` is read with `kubectl exec` inside the controller pod, on port 8080, not through the NodePort.

### If It Fails

Run `helm status <release> -n <namespace>` for the release the check names.

| Status | Action |
|---|---|
| `failed` | Run the stage again. `helm upgrade --install` recovers a failed release |
| `pending-install`, `pending-upgrade` | `helm uninstall <release> -n <namespace>`, then run the stage again |

A `--wait` timeout leaves the release `failed`. A killed helm process leaves it `pending-*`. The stage writes pods, events, `helm list` and controller logs to `OUT_DIR/diagnostics/05-install-site-controller/` on failure. `PHASE_TIMEOUT` sets the per-release wait; the default is 15 minutes.

A digest mismatch names the item, the digest in the manifest and the digest of the pulled chart. The registry does not hold what the manifest names. Nothing was installed for that item; fix the manifest or the registry and run the stage again.

A pull error on the controller pod means the image is missing from a node. Run `PREPULL_EXTERNAL=0 deploy/stages/02-stage-images.sh run`, then run this stage again. If the node reports `DiskPressure`, free space on the node first; the kubelet removes the image again otherwise.

`NicoMachine CRD not installed` in the controller log means the controller started before CAPNICo registered its CRDs. The controller reads them once at start. Run `kubectl -n nke-controller rollout restart deployment/nke-controller`, then run the check again.

## 6. Secure and Register the Site

Patch the Cluster API controllers to authenticate to tenant clusters with SPIFFE JWTs, then register the site with `nkectl`.

```bash
deploy/stages/06-secure-and-register.sh run
deploy/stages/06-secure-and-register.sh check
```

The stage runs `controller/hack/apply-capi-spiffe-exec-auth.sh`, then `nkectl config set-site --name SITE_NAME --url http://SITE_VIP:30080`. `SITE_NAME` and `SITE_VIP` come from `OUT_DIR/site.env`, written by Describe Your Site, else from `site.name` and `site.vip` in `site-config.yaml`. Setting either variable in the environment overrides both.

### Choices

**API authentication is off by default.** The controller API is plain HTTP on NodePort `30080` on every node IP, with no token required. Anyone who can reach a node can call every endpoint. Turn authentication on before exposing the site: [Configuring NKE API Authentication](../../controller/docs/configuring-api-authentication.md).

**The API is reachable from in-cluster pods only by default.** The chart's NetworkPolicy admits the API port only from pods in the cluster. Every other ingress is denied, so `nkectl` on an operator machine times out. Set `API_CLIENT_CIDRS` to the operator's network, space separated, before running this stage: `API_CLIENT_CIDRS="10.0.0.0/8" deploy/stages/06-secure-and-register.sh run`. The stage then applies a second NetworkPolicy, `nke-controller-api-clients`, that admits those CIDRs to the controller pod on port 8080. Server-side apply makes it safe to rerun with another list. Unset, nothing is applied and the API is unreachable from outside the cluster.

### Check

Each patched Cluster API Deployment mounts a `csi.spiffe.io` volume and finishes its rollout. `nkectl get providers` lists `nico` as `CONFIGURED`. `GET http://SITE_VIP:30080/api/version` answers `200` from the runner within 10 seconds. `nkectl get providers` reads the kubeconfig, not the API, so this request is the proof that the API answers off the nodes.

`CONFIGURED` means the CRDs are established, the controller runs and the credentials Secret exists. It does not prove the credentials work. CAPNICo reads the Secret on every reconcile, so a corrected Secret takes effect without a restart.

### If It Fails

The rollout waits for the `csi.spiffe.io` CSIDriver, which the SPIRE release installs. If it is missing, Install the Site Controller did not finish. `clusterctl upgrade` replaces the patched Deployments, so run this stage again after every Cluster API upgrade.

`/api/version` does not answer from the runner when the runner's network is not in `API_CLIENT_CIDRS`. Export it and run the stage again. `kubectl -n nke-controller get networkpolicy` lists the chart's policy and, when applied, `nke-controller-api-clients`.

## Remove

Uninstall in reverse item order, then remove Cluster API:

```bash
helm uninstall nke-controller -n nke-controller
helm uninstall spire -n spire-server
helm uninstall temporal -n temporal
helm uninstall spire-crds -n spire-server
helm uninstall postgres -n postgres
helm uninstall nktp -n nktp
kubectl delete -f deploy/nico/capnico/capnico.yaml
clusterctl delete --all
```

Helm leaves PersistentVolumeClaims behind. Delete the `postgres` and `spire-server` PVCs yourself when the data is no longer needed.

## Upgrade

1. Build and push the new controller image. Put its ref in the release manifest.
2. Run Stage Images to pull it on every node, then Describe Your Site to render.
3. Run `deploy/install.sh --from install-site-controller`.

Secure and Register runs again as part of that range. [Tenant Cluster Kubernetes Upgrades](../operations/upgrade.md) covers upgrades of tenant clusters.

## Extend to GitOps

The scripted install above is the baseline. Every item in `deploy/nico/nke-deploy.yaml` is a Helm chart plus values files, or plain YAML. A GitOps tool can deliver the same items without changing the deploy path.

- **Source.** The clone is a valid git source. The controller and NKTP charts are in the tree, at `controller/deploy/charts/controller` and `nktp/deploy/charts/nktp`. Postgres, SPIRE CRDs, Temporal and SPIRE come from the refs in the release manifest. An OCI registry is a valid Helm source, so the same refs serve Argo CD.
- **Order.** The item list order maps to sync waves, one wave per item.
- **The Certificate.** The `spire` item declares a `requires:` on the `oidc-provider-upstream-ca` Certificate. In GitOps that is a wave boundary or a health check on the Certificate, not a `kubectl wait`.
- **Secrets.** The `nke-base-config` item renders three Secrets: `nico-credentials`, `ghcr-imagepull` and `dgxc-tenant-credentials`. Under GitOps those come from the operator's secret store. The namespaces and the Certificate chain in the same item are plain YAML and stay in git.
- **Imperative steps.** `clusterctl init` and `controller/hack/apply-capi-spiffe-exec-auth.sh` stay imperative, outside GitOps. The other stages do not change.

The per-site values Describe Your Site renders belong in your own repository or your secret store. [Deploy Path](deploy-path.md) describes the `argocd` item type for a deploy path that carries Argo CD Applications.

The Application below delivers the `temporal` item. It is illustrative and untested. The chart source is the repository and version from `deploy/nico/temporal/release.yaml`; with a release manifest, `repoURL` points at the registry in the `temporal` ref instead. The values come from the two in-tree values files through the clone as a second source. The sync wave is the item's position in the list.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: temporal
  namespace: argocd
  annotations:
    argocd.argoproj.io/sync-wave: "8"
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: temporal
  sources:
    - repoURL: https://go.temporal.io/helm-charts
      chart: temporal
      targetRevision: 0.74.0
      helm:
        releaseName: temporal
        valueFiles:
          - $values/deploy/nico/temporal/values.yaml
          - $values/deploy/nico/temporal/overlay.yaml
    - repoURL: https://github.com/dsx-ai-factory/nke.git
      targetRevision: main
      ref: values
  syncPolicy:
    syncOptions:
      - CreateNamespace=true
```

## Next Steps

- [Your first cluster](../first-cluster.md#7-create-a-tenant-cluster) — create a tenant cluster on the registered site
- [Infrastructure Providers](infrastructure-providers.md) — prove the NICo credentials before committing hardware
