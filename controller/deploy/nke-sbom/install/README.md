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

## Markers

Commands between `# doc-begin` and `# doc-end` in each stage script are extracted verbatim into the guide; keep them copy-pasteable and free of functions.
