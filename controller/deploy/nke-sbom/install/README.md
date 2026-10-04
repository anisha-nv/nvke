# install

The executable form of [the install guide](../../../../docs/site-bootstrap/install-site-controller.md).
One script per stage. Run them in order. The guide is the authority.

## Run

```bash
export SITE_KUBECONFIG=~/.kube/site.kubeconfig RELEASE_FILE=./nke-release.yaml
./install-all.sh                                    # every stage: run, then check; stop on the first failure
./install-all.sh --from install-cluster-api         # resume at a stage
./install-all.sh --until stage-images               # stop after a stage
./install-all.sh --dry-run                          # print kubectl and helm argv only
./04-install-cluster-api.sh run                     # one stage
./04-install-cluster-api.sh check
```

Stage names drop the numeric prefix. `--from install-cluster-api` selects `04-install-cluster-api.sh`.

## Inputs

| File | Says | Set by |
|---|---|---|
| `nke-release.yaml` | Which artifacts to install and where to pull each from, by digest | `RELEASE_FILE`, or `RELEASE_REF` plus the variables below |
| `site-config.yaml` | What is specific to this site | `SITE_CONFIG` |

Everything else is derived.

Two ways to get `nke-release.yaml`:

- Point `RELEASE_FILE` at one. [nke-release.example.yaml](nke-release.example.yaml) shows the shape. The grammar is in the docstring of `release_spec.py`.
- Set `RELEASE_REF` to an NKD Release reference. `lib.sh` runs `nkdctl release verify`, saves the description to `$OUT_DIR/nkd-release.json`, and converts it with `nkd-release-to-nke.py` into `$OUT_DIR/nke-release.yaml`. The description has no source pin and no chart versions, so also set `RELEASE_SOURCE_REPO`, `RELEASE_SOURCE_TAG` and `RELEASE_CHART_VERSIONS`. The converted file is reused while its `release:` equals `RELEASE_REF`.

Neither set is an error that names both variables.

## Stages

| Stage | Reads | Writes | Check |
|---|---|---|---|
| 00 check readiness | spec, checkout, tools | nothing | tools present; lane and scope printed; checkout at `source.tag`; chart versions match `release.yaml`; ref hosts reachable from a node |
| 01 prepare cluster | kubeconfig | taint removed, default StorageClass | no control-plane taint; one default StorageClass |
| 02 stage images | spec, `images.txt` | images in each node's containerd | controller digest present; external images present (warning if not) |
| 03 describe site | `site-config.yaml`, spec | values files, Secrets input, `site.env`, `images.txt`, `version-bom.cdx.json` | files present; no empty value; password matches the live Secret |
| 04 install Cluster API | pinned versions | cert-manager, CAPI | Deployments Available; CRDs Established |
| 05 install site controller | `$OUT_DIR`, spec | the 11 deploy items | releases deployed at the spec versions; `/readyz` ok; no CRD warning in the controller log |
| 06 secure and register | kubeconfig, `site.env` | exec-auth patch, `nkectl` site | rollouts complete; `nkectl get providers` shows `nico` CONFIGURED |

## Release spec parser

Every stage reads the spec through `release_spec.py`. Its CLI is a contract.

```bash
release_spec.py meta   FILE                             # RELEASE=.. LANE=.. SCOPE=.. SOURCE_REPO=.. SOURCE_TAG=.. SOURCE_COMMIT=..
release_spec.py items  FILE [--type image|chart|binary] # name<TAB>type<TAB>ref<TAB>version, file order
release_spec.py ref    FILE NAME                        # the item's ref; exit 1 when absent
release_spec.py digest FILE NAME                        # sha256:<hex> from the ref
```

`lib.sh` wraps them as `release_meta`, `release_ref NAME` and `release_items [TYPE]`.
A line the parser does not recognise is an error naming the file and line.

## Stage 2: images

- The controller image comes from the `controller` item's `ref`. Each node pulls it by digest:
  `ctr -n k8s.io images pull --hosts-dir /etc/containerd/certs.d <ref>`.
- `--hosts-dir` is where the node's containerd reads `hosts.toml`. Mirrors and plain-HTTP registries configured there apply. `HOSTS_DIR` overrides the path.
- External images come from `$OUT_DIR/images.txt`, plus `EXTRA_IMAGES`. Each node pre-pulls them the same way.
- `NODE_EXEC` says how to reach a node: unset on the node itself, `ssh root@{node}` for hosts, `nerdctl exec -i {node}` for container nodes. `NODE_MAP` renames nodes as `name=target` pairs.
- A node that holds an image is skipped. A failed pull names the node, the image and the registry.
- Stage 3 writes `images.txt`, and it runs after this stage. `install-all.sh` runs this stage a second time after stage 3, when both are in the selected range, so the external images are pre-pulled before stage 5. Run by hand, rerun this stage after stage 3 yourself, or let the kubelet pull the rest. The check fails on the controller digest and warns on external images.

```bash
NODE_EXEC='ssh root@{node}' ./02-stage-images.sh run
./02-stage-images.sh check
```

## Stage 3: describe your site

No cluster access. It reads `site-config.yaml` and writes to `$OUT_DIR`:

| File | Used by |
|---|---|
| `bootstrap-config.json` | the `nke-base-config` template item |
| `controller.yaml`, `spire.yaml`, `postgresql.yaml`, `temporal.yaml` | the last `--values` of each release |
| `nktp.yaml` | the `nktp` release, only when `site-config.yaml` has an `nktp:` block |
| `site.env` | stage 6: `SITE_NAME`, `SITE_VIP`, the image refs |
| `images.txt` | stage 2: every external image the install pulls |
| `version-bom.cdx.json` | CycloneDX 1.5: the deploy path, one component per chart item, the controller image, digests from the refs |

Config rules:

- Copy [site-config.example.yaml](site-config.example.yaml). Replace every `REPLACE_*` value. The root `.gitignore` ignores `site-config.yaml` and `site-config.*.yaml`.
- Every key is required except `db.password`, `images.controller`, `registry.mirror` and `nktp`. A missing, empty or non-scalar value fails with the key name.
- `images.controller` defaults to the `controller` ref in the spec. Set it only to override. Without a spec it is required.
- `images.profiles` is always required. NKD does not build it. Both image keys accept `repository:tag` or `repository@sha256:<digest>`.
- `site.trustDomain` must equal the trust domain the nodes were bootstrapped with.
- `spire.caCertFile` and `spire.caKeyFile` are the bootstrap SPIRE CA. `spire.yaml` carries the PEM contents, so it is mode 0600.
- `db.password` is generated once and reused on rerun.
- The `nktp:` block is passed through verbatim. See "Telemetry" in [the deploy path README](../README.md).
- The summary redacts credentials. The schema is unstable until it is folded into BootstrapConfig.

`images.txt` sources:

- `helm template` of each helm item with its values and the site values. Needs `helm` and the network. `IMAGES_TXT_SKIP=1` writes only the static entries and a skip note.
- The CAPNICo manifest image.
- The Cluster API and cert-manager images at the versions in `make/tool-versions.mk`. These are static names; `clusterctl` lists none without a cluster.
- The controller image is left out. Stage 2 pulls it from the spec.

`registry.mirror` (optional, a host with optional path, no scheme) repoints chart images:

| Chart | Keys set |
|---|---|
| postgresql | `global.imageRegistry`, `global.security.allowInsecureImages: true` |
| temporal | `server.image.repository`, `admintools.image.repository`, `web.image.repository` |
| spire | `global.spire.image.registry` |
| CAPNICo | `capnico-kustomization.yaml` with an `images:` override. Stage 5 does not read it yet; set `CAPNICO_IMAGE` instead |

Not repointed: the Cluster API, cert-manager and nktp images. Nodes reach those through `hosts.toml` or directly.

```bash
SITE_CONFIG=./site-config.yaml ./03-describe-site.sh run
./03-describe-site.sh check
```

## Stage 5: charts

The four published charts come from the spec, not from their upstream repositories.

1. `release_ref <item>` gives the ref. The ref pins the manifest digest.
2. `oras manifest fetch` reads the chart layer: its digest and its file name.
3. `oras pull "$ref" -o "$OUT_DIR/charts/<item>"` writes the `.tgz`. A file already there with the right digest is not pulled again.
4. The file's sha256 must equal the layer digest. A mismatch stops the run and names the item and both digests.
5. `helm upgrade --install` takes the file with the same namespace, wait, timeout and values as before.

The controller and NKTP charts stay in the tree. `oras` must be on `PATH` at the pinned version. Under `DRY_RUN` the argv is printed and the digest check reports `SKIP`.

## Stage 6: secure and register

- Patches the Cluster API controllers with the SPIRE exec-auth plugin: `controller/hack/apply-capi-spiffe-exec-auth.sh --kubeconfig "$SITE_KUBECONFIG"`.
- Registers the site: `nkectl config set-site`. Name and VIP come from `$OUT_DIR/site.env`, else from `site-config.yaml`. `SITE_NAME` and `SITE_VIP` override both. `nkectl` must be on `PATH` ([install guide](../../../../docs/site-bootstrap/installation.md)).
- `clusterctl upgrade` drops the patch. Rerun this stage after it.
- CONFIGURED means the provider CRDs exist, its manager is available and its credentials Secret exists. It does not mean the credentials work.
- API authentication is off by default. The API is plain HTTP on NodePort 30080 on every node IP. To require a token, follow [configuring-api-authentication.md](../../../docs/configuring-api-authentication.md).

## Environment

| Variable | Default | Purpose |
|---|---|---|
| `SITE_KUBECONFIG` | required | Site cluster kubeconfig. Every kubectl and helm call uses it |
| `RELEASE_FILE` | required unless `RELEASE_REF` | Path of `nke-release.yaml`. Optional for stage 3, which then needs `images.controller` and writes no BOM |
| `RELEASE_REF` | unset | NKD Release reference. Converted into `$OUT_DIR/nke-release.yaml` |
| `RELEASE_VERIFY_ARGS` | unset | Extra `nkdctl release verify` arguments: `--lane`, `--scope`, `--identity`, `--issuer`, `--cosign`, `--cosign-digest` |
| `RELEASE_SOURCE_REPO`, `RELEASE_SOURCE_TAG`, `RELEASE_SOURCE_COMMIT` | required with `RELEASE_REF`; commit optional | The NKE repository, tag and commit the Release was pinned from |
| `RELEASE_CHART_VERSIONS` | required with `RELEASE_REF` | `name=version,...` for every chart item |
| `SITE_CONFIG` | `./site-config.yaml` | Site description. Holds credentials; keep it out of git |
| `OUT_DIR` | `./.site-install` | Rendered files, generated password, diagnostics. Mode 0700. Ignored by git |
| `SBOM_DIR` | `controller/deploy` | Deploy tree the stages install from |
| `PHASE_TIMEOUT` | `15m` | `helm --wait` timeout per release |
| `REQUIRE_TIMEOUT` | `180s` | `kubectl wait` timeout for preconditions |
| `DRY_RUN` | unset | Print kubectl and helm argv instead of running them |
| `NODE_EXEC` | unset | Stage 0 and 2: command prefix that reaches a node, with `{node}` in it |
| `NODE_MAP` | unset | `name=target` pairs renaming nodes for `NODE_EXEC` |
| `NODE` | first node | Stage 0: the node that probes the ref hosts |
| `HOSTS_DIR` | `/etc/containerd/certs.d` | Stage 2: the node's `hosts.toml` directory |
| `EXTRA_IMAGES` | unset | Stage 2: more images to pre-pull, space separated |
| `IMAGES_TXT_SKIP` | unset | Stage 3: `1` skips `helm template` |
| `CAPNICO_IMAGE` | unset | Stage 5: `repository:tag` the CAPNICo manifest is repointed at |
| `SITE_NAME`, `SITE_VIP` | from `site.env` | Stage 6 overrides |

## Failure

A failed `run` writes `$OUT_DIR/diagnostics/<stage>/`: nodes, pods, events, `helm list`, controller logs.
`install-all.sh` prints the directory and the `--from` flag to resume.

## Test

```bash
make test                            # renderer and release spec fixtures, Python 3 only, no cluster
bash ../../../../hack/site-install/regen_test.sh   # the generator
```

## Generated files

- `05-install-site-controller.sh` is generated. `hack/site-install/regen.sh` writes it from the deploy path. Do not edit it by hand.
- Commands between `# doc-begin` and `# doc-end` in each stage script are extracted into the guide. Keep them copy-pasteable and free of functions.
- The guide marks each block with `<!-- stage:NN-name begin -->` and `<!-- stage:NN-name end -->`.
- `regen.sh --check` fails when the script or a block is stale.
