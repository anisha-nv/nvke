# install

The executable form of [docs/site-bootstrap/install-site-controller.md](../../../../docs/site-bootstrap/install-site-controller.md).
One script per stage of that guide, run in order.
The guide is the authority. When a script and the guide disagree, the guide wins.

## Run

All stages, in order. Each stage runs, then checks. The first failure stops the run:

```bash
export SITE_KUBECONFIG=~/.kube/site.kubeconfig RELEASE_FILE=./nke-release.yaml
./install-all.sh
./install-all.sh --from install-cluster-api          # resume
./install-all.sh --until stage-images                # stop after a stage
./install-all.sh --dry-run                           # print kubectl and helm argv only
```

One stage:

```bash
./NN-name.sh run
./NN-name.sh check
```

Stage names drop the numeric prefix: `--from install-cluster-api` selects `04-install-cluster-api.sh`.
`install-all.sh` runs the stage scripts present in this directory.

## Release spec

Every stage except 3 reads `nke-release.yaml`, the one file that names every artifact the install pulls: the controller image, the four external charts and the `nkectl` binary, each as an OCI ref with its digest. Point `RELEASE_FILE` at one, or set `RELEASE_REF` to an NKD Release reference and `lib.sh` runs `nkdctl release verify --reference "$RELEASE_REF" $RELEASE_VERIFY_ARGS`, saves the description to `$OUT_DIR/nkd-release.json` and converts it with `nkd-release-to-nke.py` into `$OUT_DIR/nke-release.yaml`. The description carries no source pin and no chart versions, so that path needs `RELEASE_SOURCE_REPO`, `RELEASE_SOURCE_TAG` (`RELEASE_SOURCE_COMMIT` is optional) and `RELEASE_CHART_VERSIONS=postgresql=18.5.15,...`. The converted file is reused while its `release:` equals `RELEASE_REF`. [nke-release.example.yaml](nke-release.example.yaml) shows the shape with placeholder digests; the grammar is in the docstring of `release_spec.py`. Neither variable set is an error that names both.

`release_spec.py` is the parser every stage calls. Its CLI is a contract:

```bash
release_spec.py meta   FILE                          # RELEASE=.. LANE=.. SCOPE=.. SOURCE_REPO=.. SOURCE_TAG=.. SOURCE_COMMIT=.., single-quoted, for eval
release_spec.py items  FILE [--type image|chart|binary]  # name<TAB>type<TAB>ref<TAB>version per item, file order; version empty unless chart
release_spec.py ref    FILE NAME                     # the item's ref; exit 1 when absent
release_spec.py digest FILE NAME                     # sha256:<hex> from the ref
```

`lib.sh` wraps them as `release_meta` (evals the meta lines into the shell), `release_ref NAME` and `release_items [TYPE]`. Any line the parser does not recognise is an error naming the file and line. Stage 0 prints the lane and scope, warns when the lane is not `pipeline-attested`, checks the checkout is at `source.tag` (or `source.commit` when set), compares each chart item's `version` with `chart.version` in `nke-sbom/<name>/release.yaml`, and probes each registry host in the refs from a node when `NODE_EXEC` is set.

## Stage images

Stage 2 pulls the controller image by digest from its `ref` in the release spec through every node's containerd, into the `k8s.io` namespace. It then pre-pulls every entry in `images.txt` the same way. A failed pull names the registry that refused and the node. `NODE_EXEC` says how the script reaches a node: unset when it runs on the node, `ssh root@{node}` for hosts, `nerdctl exec -i {node}` for container nodes. A node that already holds an image at the same digest is skipped.

```bash
NODE_EXEC='ssh root@{node}' ./02-stage-images.sh run
./02-stage-images.sh check
```

## Describe your site

Stage 3 has no cluster access. It reads one `site-config.yaml` and writes the files later stages use into `$OUT_DIR`: `bootstrap-config.json` for the `nke-base-config` template item, `controller.yaml`, `spire.yaml`, `postgresql.yaml` and `temporal.yaml` as the last `--values` of each release, and `site.env` with `SITE_NAME`, `SITE_VIP` and the two image references for stages 2 and 6.
Copy [site-config.example.yaml](site-config.example.yaml), replace every `REPLACE_*` value, keep the copy out of git. The root `.gitignore` ignores `site-config.yaml` and `site-config.*.yaml`.
Every key is required except `db.password`; a missing, empty or non-scalar value fails with the key name.
The optional top-level `nktp:` block is the exception to the two-level schema: it is not parsed, it is dedented and written to `nktp.yaml` as written, and stage 5 appends that file as the last `--values` of the `nktp` release. Without the block no `nktp.yaml` is written (a stale one from an earlier run is removed) and telemetry exports to the chart's bundled development backend. See "Per-site values" and "Telemetry" in [the deploy path README](../README.md); the chart README's "The backend" section lists the keys.
`site.trustDomain` is explicit and must equal the trust domain the nodes were bootstrapped with.
`spire.caCertFile` and `spire.caKeyFile` point at the bootstrap SPIRE CA; the chart stores them in a Secret it creates, so `spire.yaml` carries the PEM contents.
The database password is generated once and reused on rerun; `check` compares it with the live Postgres Secret when `SITE_KUBECONFIG` is set.
The summary redacts credentials. The schema is unstable until it is folded into BootstrapConfig.

```bash
SITE_CONFIG=./site-config.yaml ./03-describe-site.sh run
./03-describe-site.sh check
```

## Release spec

Every artifact comes from the OCI ref `nke-release.yaml` names, by digest. `RELEASE_FILE` points at the file; `RELEASE_REF` names an NKD Release instead, and stage 0 runs `nkdctl release verify` and converts the output with `nkd-release-to-nke.py` into `$OUT_DIR/nke-release.yaml`. Stage 0 prints the lane and scope, checks the checkout is at `source.tag`, checks every chart item's `version` against its `release.yaml`, and checks the ref hosts are reachable from a node. Stage 3 defaults `images.controller` to the `controller` ref and writes `images.txt` and `version-bom.cdx.json`. Stage 5 pulls the four external charts with `oras` into `$OUT_DIR/charts/` and checks the layer digest before installing. The guide's "Release Spec" section has the file.

## Charts come from the spec

Stage 5 installs the four published charts, Postgres, SPIRE CRDs, Temporal and SPIRE, from the release spec and not from their upstream repositories. For each chart item it reads the ref with `release_ref`, fetches the manifest the ref names with `oras manifest fetch` and takes the chart layer from it: the one with the Helm chart content media type, else the first. The ref pins the manifest digest; the layer digest is the sha256 of the unchanged `.tgz`, and the layer's `org.opencontainers.image.title` is the file name. `oras pull "$ref" -o "$OUT_DIR/charts/<item>"` writes that file, the stage computes its sha256 and compares it with the layer digest, and a mismatch stops the run naming the item, the file's digest and the layer digest. Nothing is installed for that item. A file already there with the right digest is not pulled again. `helm upgrade --install` then takes the file, with the same namespace, wait, timeout and values arguments as before. The controller and NKTP charts stay in the tree. `oras` must be on `PATH` at the version `make/tool-versions.mk` pins; stage 0 checks for it with the other tools. Under `DRY_RUN` the `oras` argv is printed and the digest check is reported as `SKIP`.

## Environment

| Variable | Default | Purpose |
|---|---|---|
| `SITE_KUBECONFIG` | required | Site cluster kubeconfig. Every kubectl and helm call uses it |
| `RELEASE_FILE` | required unless `RELEASE_REF` | Path of `nke-release.yaml`. Stage 3 does not read it |
| `RELEASE_REF` | unset | OCI reference of an NKD Release. `nkdctl release verify` output is converted into `$OUT_DIR/nke-release.yaml` |
| `RELEASE_VERIFY_ARGS` | unset | Extra arguments for `nkdctl release verify`: `--lane`, `--scope`, `--identity`, `--issuer`, `--cosign`, `--cosign-digest` |
| `RELEASE_SOURCE_REPO`, `RELEASE_SOURCE_TAG`, `RELEASE_SOURCE_COMMIT` | required with `RELEASE_REF`; commit optional | The NKE repository, tag and commit the Release was pinned from. Written to `source:` |
| `RELEASE_CHART_VERSIONS` | required with `RELEASE_REF` | `name=version,...` for every chart item, from `release.yaml` at the source tag |
| `SITE_CONFIG` | `./site-config.yaml` | Site description. Holds credentials; keep it out of git |
| `OUT_DIR` | `./.site-install` | Rendered values, generated password, diagnostics. Mode 0700. Ignored by git |
| `SBOM_DIR` | `controller/deploy` | Deploy tree the stages install from |
| `PHASE_TIMEOUT` | `15m` | `helm --wait` timeout per release |
| `REQUIRE_TIMEOUT` | `180s` | `kubectl wait` timeout for preconditions |
| `DRY_RUN` | unset | Set to any value to print kubectl and helm argv instead of running them |
| `IMAGES` | the `controller` ref and `images.txt` | Stage 2: images to pull on every node, space separated. `EXTRA_IMAGES` adds more |
| `CAPNICO_IMAGE` | `image:` in `nke-sbom/capnico/capnico.yaml` | Stage 2 pre-pulls it on every node; stage 5 repoints the manifest at it |
| `NODE_EXEC` | unset (run on the node) | Stage 2: command prefix that reaches a node, with `{node}` in it. `NODE_MAP` renames nodes as `name=target` pairs. Stage 0 probes the release registries through it from `NODE`, the first node by default; unset, that check is skipped |

On a failed `run`, `lib.sh` writes `$OUT_DIR/diagnostics/<stage>/` with nodes, pods, events, `helm list` and controller logs.

## Stage 6: secure and register

`06-secure-and-register.sh` patches the Cluster API controllers with the SPIRE exec-auth plugin
(`controller/hack/apply-capi-spiffe-exec-auth.sh --kubeconfig "$SITE_KUBECONFIG"`) and registers the
site with `nkectl config set-site`. It takes the site name and VIP from `$OUT_DIR/site.env`, written by
stage 3, else from `site.name` and `site.vip` in `$SITE_CONFIG`; `SITE_NAME` and `SITE_VIP` in the
environment override both. `nkectl` must be on `PATH`
([install guide](../../../../docs/site-bootstrap/installation.md)).
`clusterctl upgrade` rewrites the patched Deployments and drops the patch, so rerun this stage after it.
The check passes when `nkectl get providers` reports `nico` as `CONFIGURED`. CONFIGURED means the
provider CRDs are established, its manager is available and its credentials Secret exists. It does not
mean the credentials work.

The default install leaves API authentication off: the API is plain HTTP on NodePort 30080 on every
node IP. To require a token, follow
[configuring-api-authentication.md](../../../docs/configuring-api-authentication.md).

## Test

The renderer and the release spec parser have fixtures under `testdata/`. Python 3 only, no cluster:

```bash
make test            # or ./testdata/test.sh
```

`hack/site-install/regen_test.sh` covers the generator.

## Markers and regeneration

Commands between `# doc-begin` and `# doc-end` in each stage script are extracted verbatim into the guide; keep them copy-pasteable and free of functions.
The guide marks where each block lands with `<!-- stage:NN-name begin -->` and `<!-- stage:NN-name end -->`.
`05-install-site-controller.sh` is generated. `hack/site-install/regen.sh` writes it from the deploy path and refreshes every block in the guide; `regen.sh --check` fails when either is stale. Do not edit the generated script by hand.
