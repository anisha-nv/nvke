# deploy

Installs the NKE management plane onto a site cluster that `nkectl bootstrap site` left behind: Postgres, Temporal, SPIRE, CAPNICo, NKTP and the NKE controller. One scenario: NKE with CAPNICo on a NICo site.

The scripts are plain bash and Python. Read them before you run them. The guide [docs/site-bootstrap/install-site-controller.md](../docs/site-bootstrap/install-site-controller.md) explains each stage.

## Inputs

| Input | From | Says |
|---|---|---|
| The deploy path, [`nico/`](nico/) | This checkout | What to install and in which order. `nke-deploy.yaml` lists the items. Each item directory holds its chart pin (`release.yaml`), values and overlay, or its manifests. See [deploy-path.md](../docs/site-bootstrap/deploy-path.md) |
| `release-manifest.yaml` | `nkdctl`, or the converter `lib/nkd-release-to-nke.py`, or written by hand | Which published artifacts to use, each by OCI reference and digest: the controller image and the four external charts (postgresql, spire-crds, temporal, spire), plus the NKE source tag and commit. [release-manifest.example.yaml](release-manifest.example.yaml) shows the shape |
| `site-config.yaml` | You | What is specific to this site: name, trust domain, VIP, SPIRE CA files, credentials, images. [site-config.example.yaml](site-config.example.yaml) lists every key |

`site-config.yaml` is the interim site input. The sitespec is its intended home. Until then it is a separate file that holds credentials; keep it out of git.

The release manifest and the deploy path are joined by item name. The checkout must be at the manifest's `source.tag`: the controller and NKTP charts are installed from the tree, not from a registry.

## Run

```bash
export SITE_KUBECONFIG=~/.kube/site.kubeconfig
export RELEASE_FILE=./release-manifest.yaml
export SITE_CONFIG=./site-config.yaml
deploy/install.sh                                # every stage: run, then check; stop on the first failure
deploy/install.sh --from install-cluster-api     # resume at a stage
deploy/install.sh --until stage-images           # stop after a stage
deploy/install.sh --dry-run                      # print the kubectl and helm argv only
deploy/stages/04-install-cluster-api.sh run      # one stage
deploy/stages/04-install-cluster-api.sh check
```

A stage name is the script name without its number: `--from install-cluster-api` selects `stages/04-install-cluster-api.sh`.

`RELEASE_REF` is the alternative to `RELEASE_FILE`. It names an NKD Release. `lib.sh` runs `nkdctl release verify`, saves the output to `$OUT_DIR/nkd-release.json` and converts it into `$OUT_DIR/release-manifest.yaml`. The description carries no source pin and no chart versions, so also set `RELEASE_SOURCE_REPO`, `RELEASE_SOURCE_TAG` and `RELEASE_CHART_VERSIONS`. Setting neither variable is an error.

## Stages

| # | Stage | Reads | Result | Check |
|---|---|---|---|---|
| 0 | `check-readiness` | manifest, checkout, tools, cluster | nothing changes | manifest parses; checkout at `source.tag`; chart versions match `release.yaml`; tools on `PATH`; odd node count; egress; node disk headroom |
| 1 | `prepare-cluster` | kubeconfig | control-plane taint removed; default StorageClass | no taint, or worker nodes present; exactly one default StorageClass |
| 2 | `stage-images` | manifest, `images.txt` | controller image in each node's containerd; external images pre-pulled | controller digest present on every node (required); external images present (warning) |
| 3 | `describe-site` | `site-config.yaml`, manifest, deploy path | values files, `bootstrap-config.json`, `site.env`, `images.txt`, `version-bom.cdx.json` in `OUT_DIR` | files complete; no empty value; password matches the live Secret |
| 4 | `install-cluster-api` | `make/tool-versions.mk` | cert-manager, Cluster API core, kubeadm providers | Deployments Available; CRD Established; versions match |
| 5 | `install-site-controller` | deploy path, manifest, `OUT_DIR` | every item of `nke-deploy.yaml` | CAPNICo CRDs and Deployment; Secrets present; releases `deployed` at the pinned versions; `/readyz` ok; no CRD warning in the controller log |
| 6 | `secure-and-register` | kubeconfig, `site.env` | exec-auth patch on the Cluster API controllers; `nkectl` site; API client NetworkPolicy when `API_CLIENT_CIDRS` is set | rollouts complete; `nkectl get providers` shows `nico` CONFIGURED; `/api/version` answers 200 |

`install.sh` runs stage 2 again after stage 3, so the external images listed in `images.txt` are pre-pulled. It runs stage 2 once more, controller image only, right before stage 5. The kubelet garbage-collects unused images under disk pressure, and the re-pull restores the controller image if that happened.

## How the stages work

**0, check-readiness.** Reports and changes nothing. `run` and `check` are the same. It parses the manifest, prints its lane and scope, compares the checkout with `source.tag`, and compares each chart version in the manifest with the pin in the deploy path's `release.yaml`. A mismatch fails. With `NODE_EXEC` set, it probes each registry host from a node.

**1, prepare-cluster.** Removes the control-plane `NoSchedule` taint from nodes that still carry it. A cluster with worker nodes keeps the taint. Installs rancher local-path-provisioner when no StorageClass is default. `SKIP_STORAGECLASS=1` leaves storage alone for a site with its own CSI driver.

**2, stage-images.** Pulls the controller image by digest on every node, with `ctr -n k8s.io images pull --hosts-dir`, through the node's containerd. The node's `hosts.toml` mirrors apply. External images from `images.txt` are pre-pulled the same way, as a best effort. `NODE_EXEC` says how to reach a node.

**3, describe-site.** Runs `lib/describe_site.py`. It renders `site-config.yaml` into one values file per chart, the BootstrapConfig JSON the template item renders against, `site.env`, `images.txt` and a CycloneDX BOM. No cluster access. `helm template` needs the chart sources; `IMAGES_TXT_SKIP=1` skips it.

**4, install-cluster-api.** Runs `clusterctl init` with the core and kubeadm providers at `CAPI_VERSION` from `make/tool-versions.mk`. cert-manager comes with it. An installed Cluster API at another version is an error, because `clusterctl init` does not upgrade.

**5, install-site-controller.** One call to `nke-deploy.sh install` with the deploy path, the release manifest, `--config $OUT_DIR/bootstrap-config.json` and one `--values-for` per rendered values file. Charts named in the release manifest are pulled by digest and verified. The controller and NKTP charts come from the checkout. `CAPNICO_IMAGE` repoints the CAPNICo manifest through a `--manifest-override`.

**6, secure-and-register.** Runs `controller/hack/apply-capi-spiffe-exec-auth.sh`, then `nkectl config set-site` with the name and VIP from `site.env`. `clusterctl upgrade` drops the patch; rerun this stage after it. The chart's NetworkPolicy admits the API from in-cluster pods only. `API_CLIENT_CIDRS` adds a policy, `nke-controller-api-clients`, for the operator's networks.

## The executor: nke-deploy.sh

```text
nke-deploy.sh install --deploy-path DIR [--release-manifest PATH] [--chart-cache DIR]
    [--config BootstrapConfig.json] [--values-for ITEM=FILE]... [--chart-override ITEM=PATH]...
    [--manifest-override ITEM=DIR]... [--kubeconfig PATH] [--timeout 15m] [--dry-run] [-- helm args]
```

It walks `nke-deploy.yaml` and applies the items in list order. The list is the order. There are no phases and no enable flags.

| Type | What happens |
|---|---|
| `manifest` | Every `.yaml` in the item directory, `kubectl apply --server-side`, in name order |
| `template` | `.yaml.tmpl` files are rendered with `lib/render-template.py` against the `--config` JSON, into a temporary directory, then applied like a manifest item |
| `helm` | One chart, one release, `helm upgrade --install --wait`. `release.yaml` names the chart, version, source, release name, namespace, values files and `requires` preconditions |
| `argocd` | `application.yaml` in the item directory, `kubectl apply --server-side` |

Chart source for a `helm` item, first match wins: `--chart-override`; the release manifest item of the same name, pulled by digest with `oras` into the chart cache and its chart layer digest verified; `chart.path` in the deploy path, with `helm dependency build` first; `chart.repository`.

Values are applied in order: the item's own files, then every `--values-for` for that item. Helm merges `-f` in order, so the caller's file wins.

`--dry-run` prints every kubectl and helm argv. `PLAIN_HTTP_REGISTRIES` lists `host:port` registries that `oras` must reach without TLS.

## Environment

| Variable | Default | Purpose |
|---|---|---|
| `SITE_KUBECONFIG` | required | Site cluster kubeconfig. Every kubectl and helm call uses it. Stage 3 does not need it |
| `RELEASE_FILE` | required unless `RELEASE_REF` | Path of `release-manifest.yaml`. Optional for stage 3, which then needs `images.controller` and writes no BOM |
| `RELEASE_REF` | unset | NKD Release reference. Converted into `$OUT_DIR/release-manifest.yaml` |
| `RELEASE_VERIFY_ARGS` | unset | Extra `nkdctl release verify` arguments |
| `RELEASE_SOURCE_REPO`, `RELEASE_SOURCE_TAG`, `RELEASE_SOURCE_COMMIT` | required with `RELEASE_REF`; commit optional | The NKE repository, tag and commit the Release was pinned from |
| `RELEASE_CHART_VERSIONS` | required with `RELEASE_REF` | `name=version,...` for every chart item |
| `SITE_CONFIG` | `./site-config.yaml` | Site description. Holds credentials |
| `OUT_DIR` | `./.site-install` | Rendered files, generated password, chart cache, diagnostics. Mode 0700 |
| `DEPLOY_PATH` | `deploy/nico` | The deploy path the stages install |
| `CONTROLLER_NAMESPACE` | from `controller/release.yaml` in the deploy path | Namespace the checks read the controller in. `nke-controller` |
| `PHASE_TIMEOUT` | `15m` | `helm --wait` timeout per release |
| `REQUIRE_TIMEOUT` | `180s` | `kubectl wait` and rollout timeout |
| `DRY_RUN` | unset | Print kubectl and helm argv instead of running them |
| `NODE_EXEC` | unset | Stages 0 and 2: command prefix that reaches a node, with `{node}` in it. Unset when the script runs on the node |
| `NODE_MAP` | unset | `name=target` pairs renaming nodes for `NODE_EXEC` |
| `NODE` | first node | Stage 0: the node that probes the registry hosts |
| `MIN_DISK_GB` | `10` | Stage 0: free space wanted under `OUT_DIR` |
| `HOSTS_DIR` | `/etc/containerd/certs.d` | Stage 2: the node's `hosts.toml` directory |
| `EXTRA_IMAGES` | unset | Stage 2: more images to pre-pull, space separated |
| `PREPULL_EXTERNAL` | `1` | Stage 2: `0` skips the external pre-pull; the kubelet pulls on demand |
| `IMAGEFS_MIN_FREE_PCT` | `20` | Stages 0 and 2: warn when a node's image filesystem has less than this percent free |
| `CONTAINERD_ROOT` | `/var/lib/containerd` | Stages 0 and 2: the path measured when `crictl imagefsinfo` is not on the node |
| `IMAGES_TXT_SKIP` | unset | Stage 3: `1` skips `helm template`; `images.txt` lists only the static entries |
| `LOCAL_PATH_VERSION` | `v0.0.31` | Stage 1: local-path-provisioner release |
| `SKIP_STORAGECLASS` | unset | Stage 1: any value leaves StorageClasses alone |
| `CAPI_VERSION`, `CERT_MANAGER_VERSION` | from `make/tool-versions.mk` | Stage 4 pins |
| `CAPNICO_IMAGE` | unset | Stage 5: `repository:tag` the CAPNICo manifest is repointed at |
| `SITE_NAME`, `SITE_VIP` | from `site.env`, else `site-config.yaml` | Stage 6 overrides |
| `API_CLIENT_CIDRS` | unset | Stage 6: client networks the API admits, space separated CIDRs. Environment only |
| `PLAIN_HTTP_REGISTRIES` | unset | Registries that serve plain HTTP, `host:port`, space separated. `oras` gets `--plain-http` for them |

## Failure

A failed `run` writes `$OUT_DIR/diagnostics/<stage>/`: nodes, pods, events, `helm list`, controller and CAPNICo logs. `install.sh` prints the directory and the `--from` flag to resume. Every `run` step is safe to repeat.

## Test

```bash
make -C deploy test   # renderer and release manifest fixtures; Python 3 only, no cluster
```
