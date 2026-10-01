# install

The reference implementation of [docs/site-bootstrap/install-site-controller.md](../../../../docs/site-bootstrap/install-site-controller.md).
One script per stage of that guide, run in order.
Generated parts are marked at the top of each script. Do not edit them by hand.
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

## Describe your site

Stage 3 has no cluster access. It reads one `site-config.yaml` and writes the files stage 5 installs from into `$OUT_DIR`: `bootstrap-config.json` for the `nke-base-config` template item, and `controller.yaml`, `spire.yaml`, `postgresql.yaml`, `temporal.yaml` for `--values-for`.
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

## Test

The renderer has fixtures under `testdata/`. Python 3 only, no cluster:

```bash
make test            # or ./testdata/test.sh
```

## Markers

Commands between `# doc-begin` and `# doc-end` in each stage script are extracted verbatim into the guide; keep them copy-pasteable and free of functions.
