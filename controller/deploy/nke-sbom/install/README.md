# install

The reference implementation of [docs/site-bootstrap/install-site-controller.md](../../../../docs/site-bootstrap/install-site-controller.md).
One script per stage of that guide, run in order.
The guide is the authority. When a script and the guide disagree, the guide wins.

## Run

All stages, in order. Each stage runs, then checks. The first failure stops the run:

```bash
SITE_KUBECONFIG=~/.kube/site.kubeconfig ./install-all.sh
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
`install-all.sh` runs the stage scripts present in this directory. `02-stage-images.sh` is not in the tree yet; until it lands, import the controller and profiles images by hand as the guide describes, and stage 5 skips its image check.

## Describe your site

Stage 3 has no cluster access. It reads one `site-config.yaml` and writes the files later stages use into `$OUT_DIR`: `bootstrap-config.json` for the `nke-base-config` template item, `controller.yaml`, `spire.yaml`, `postgresql.yaml` and `temporal.yaml` as the last `--values` of each release, and `site.env` with `SITE_NAME` and `SITE_VIP` for stage 6.
Copy [site-config.example.yaml](site-config.example.yaml), replace every `REPLACE_*` value, keep the copy out of git.
Every key is required except `db.password`; a missing, empty or non-scalar value fails with the key name.
`site.trustDomain` is explicit and must equal the trust domain the nodes were bootstrapped with.
`spire.caCertFile` and `spire.caKeyFile` point at the bootstrap SPIRE CA; the chart stores them in a Secret it creates, so `spire.yaml` carries the PEM contents.
The database password is generated once and reused on rerun; `check` compares it with the live Postgres Secret when `SITE_KUBECONFIG` is set.
The summary redacts credentials. The schema is unstable until it is folded into BootstrapConfig.

```bash
SITE_CONFIG=./site-config.yaml ./03-describe-site.sh run
./03-describe-site.sh check
```

## Environment

| Variable | Default | Purpose |
|---|---|---|
| `SITE_KUBECONFIG` | required | Site cluster kubeconfig. Every kubectl and helm call uses it |
| `SITE_CONFIG` | `./site-config.yaml` | Site description. Holds credentials; keep it out of git |
| `OUT_DIR` | `./.site-install` | Rendered values, generated password, diagnostics. Mode 0700. Ignored by git |
| `SBOM_DIR` | `controller/deploy` | Deploy tree the stages install from |
| `PHASE_TIMEOUT` | `15m` | `helm --wait` timeout per release |
| `REQUIRE_TIMEOUT` | `180s` | `kubectl wait` timeout for preconditions |
| `DRY_RUN` | unset | Set to any value to print kubectl and helm argv instead of running them |

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

The renderer has fixtures under `testdata/`. Python 3 only, no cluster:

```bash
make test            # or ./testdata/test.sh
```

`hack/site-install/regen_test.sh` covers the generator.

## Markers and regeneration

Commands between `# doc-begin` and `# doc-end` in each stage script are extracted verbatim into the guide; keep them copy-pasteable and free of functions.
The guide marks where each block lands with `<!-- stage:NN-name begin -->` and `<!-- stage:NN-name end -->`.
`05-install-site-controller.sh` is generated. `hack/site-install/regen.sh` writes it from the deploy path and refreshes every block in the guide; `regen.sh --check` fails when either is stale. Do not edit the generated script by hand.
