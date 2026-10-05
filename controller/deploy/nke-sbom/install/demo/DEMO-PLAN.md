# Demo plan: bootstrap a site, install the site controller from a release spec, create a tenant

Review copy of the demo plan. It shows the path the install scripts in this directory are built for: build from the clone, publish artifacts to a registry, write `nke-release.yaml`, install from that spec, create a tenant. Environment-specific glue (Colima, DinD hosts, nico-sim, the demo registry) lives outside this repository and is named in backticks. Published scripts are linked.

Published scripts are linked by repository path. Demo-only glue and the part-1 host scripts live outside the repository and appear in backticks.

Published scripts:

| Script | Link |
|---|---|
| `install-all.sh` | ../../../../../controller/deploy/nke-sbom/install/install-all.sh |
| `00-check-readiness.sh` | ../../../../../controller/deploy/nke-sbom/install/00-check-readiness.sh |
| `01-prepare-cluster.sh` | ../../../../../controller/deploy/nke-sbom/install/01-prepare-cluster.sh |
| `02-stage-images.sh` | ../../../../../controller/deploy/nke-sbom/install/02-stage-images.sh |
| `03-describe-site.sh` | ../../../../../controller/deploy/nke-sbom/install/03-describe-site.sh |
| `04-install-cluster-api.sh` | ../../../../../controller/deploy/nke-sbom/install/04-install-cluster-api.sh |
| `05-install-site-controller.sh` | ../../../../../controller/deploy/nke-sbom/install/05-install-site-controller.sh |
| `06-secure-and-register.sh` | ../../../../../controller/deploy/nke-sbom/install/06-secure-and-register.sh |
| Install guide | ../../../../../docs/site-bootstrap/install-site-controller.md |

## Setup, off camera

| # | Command | Input | Expected output | Purpose |
|---|---|---|---|---|
| S1 | Mac: `colima stop && colima start --cpu 10 --memory 28 && colima list` | none | VM Running, 10 CPU, 28 GiB, disk stays 96 GB | Done 2026-10-01. The only permitted restart: any later restart destroys the site cluster. `demo-hosts-up.sh` restores what part 1 loses |
| S2 | Mac: `demo-preflight.sh` | none | 0 FAIL | Tools, disk, vtpmx, egress |
| S3 | `source /etc/profile.d/nkectl-bootstrap-e2e.sh` then `demo-hosts-up.sh` | none | 3 node containers up, `hosts.csv`, `spire-ca.{crt,key}` | Stand-in for three imaged hosts |
| S4 | `demo-nico-sim.sh` `up` | none | nico-sim on `0.0.0.0:18080`, `nico-containers` backend on Docker net `kind` (172.19.0.0/16) | Stand-in for the NICo machine API. Site reaches it at `http://172.26.0.1:18080` |
| S5 | `demo-build-images.sh` | repo at HEAD | build caches warm; `cwe-capd-kindest-node:local` loaded into Docker for nico-sim | Act 0 repeats the builds on camera against a warm cache so they finish in seconds |
| S6 | Copy `site-config.yaml` and `demo-tenant-site.yaml` to `/home/liac.linux/demo/` | the two files | files present in the VM | Inputs for steps 8 and 11. `nke-release.yaml` is written there by step 3b |

## Act 0: build from source and assemble the bundle (about 4 min raw, warm cache)

Everything a customer runs comes from the clone. No downloads.

| # | Command | Input | Expected output | Purpose |
|---|---|---|---|---|
| 1 | `git clone https://github.com/dsx-ai-factory/nke && cd nke && make build` | the public repo | `bin/nkectl`; `bin/nkectl version` prints the commit | The CLI is built, not downloaded |
| 2 | `make -C cli images` | `controller/Dockerfile` | `nke-controller:latest` in the VM containerd, host arch | The site controller image is built, not pulled |
| 3 | `nerdctl build --provenance=false -t cwe-profiles:demo -f controller/test/fixtures/Dockerfile controller/test/fixtures` | fixture profiles | `cwe-profiles:demo` | Profiles content is the site's own; customers build it from `controller/profiles/example` |
| 3b | `demo-make-release.sh` | the builds above, `release.yaml` pins | a `registry:2` on the node network holding the controller image and the four external charts at the pinned versions; `nke-release.yaml` with refs into that registry, `lane: local-assembly` | `nke-release.yaml` is the spec the installer consumes in Act 2: which artifacts, from where, by digest. An NKD Release yields the same file through `nkdctl release verify` plus a converter, with a signed root, SLSA provenance and SBOM attestations behind it. The installer cannot tell the two registries apart |

## Act 1: bootstrap the site (about 6.5 min raw)

| # | Command | Input | Expected output | Purpose |
|---|---|---|---|---|
| 4 | `nkectl generate sitespec nke-demo --nodes hosts.csv --set spire.trustDomain=nkectl-bootstrap.nke.local --set spire.caCertFile=spire-ca.crt --set spire.caKeyFile=spire-ca.key > sitespec.yaml` | hosts.csv (3 rows: name, ip, TPM thumbprint), SPIRE CA | sitespec.yaml | A CSV of hosts becomes a site spec |
| 5 | `nkectl bootstrap site -f sitespec.yaml --dry-run` | sitespec.yaml | loaded spec with defaults filled in, nothing changed | Validate before touching anything |
| 6 | `nkectl bootstrap site -f sitespec.yaml` | sitespec.yaml | log ends `wrote the cluster kubeconfig path=nke-demo.kubeconfig`. Proof: `kubectl --kubeconfig nke-demo.kubeconfig get nodes` shows 3 Ready control-plane nodes; `make -C /home/liac.linux/nke/cli nodes-attested` shows 3 attested | One command turns three bare hosts into a TPM-attested cluster |

## Act 2: install the site controller (about 10 min raw)

The installer takes three inputs. Everything else is derived. Run from `controller/deploy/nke-sbom/install/` in the VM checkout with `RELEASE_FILE=/home/liac.linux/demo/nke-release.yaml`, `SITE_KUBECONFIG=/home/liac.linux/demo/nke-demo.kubeconfig`, `SITE_CONFIG=/home/liac.linux/demo/site-config.yaml`, `NODE_EXEC="nerdctl exec -i {node}"` and `NODE_MAP` mapping the three node names to `nkectl-e2e-node{1,2,3}` by InternalIP .10/.11/.12. [`install-all.sh`](../../../../../controller/deploy/nke-sbom/install/install-all.sh) runs a range of stages by name, each followed by its check. The stage names are the narration.

| Input | From | Says |
|---|---|---|
| Deploy path directory: [`nke-deploy.yaml`](../../nke-deploy.yaml) and the item directories beside it | NKE checkout at the pinned tag | what to install, in which order, with which values and templates |
| `nke-release.yaml` | assembled by `demo-make-release.sh` for the demo (`nkdctl` in production) | where to pull the published artifacts from: the controller image and the four external charts by digest, plus the NKE tag |
| `site-config.yaml` | the operator | what is specific to this site |

`install-all.sh` runs a range of stages by name, each followed by its check. The stage names are the narration. The stages are a reorganized copy of the install that proved end to end on Forge bare metal in [PR 812](https://github.com/dsx-ai-factory/nke/pull/812), captured there as the `nke-sbom` deploy path and its executor [`build/use-nke-sbom.sh`](../../../../../build/use-nke-sbom.sh). The original provisioning script is not in the public repository. Split into stages, with additions: the readiness checks, the release spec, the site description, image staging and the per-stage checks.

| # | Command | Input | Expected output | Purpose |
|---|---|---|---|---|
| 7 | `./install-all.sh --until stage-images` | `nke-release.yaml`, kubeconfig | three checks PASS: [readiness](../../../../../controller/deploy/nke-sbom/install/00-check-readiness.sh) tools and bundle checksums; [prepare](../../../../../controller/deploy/nke-sbom/install/01-prepare-cluster.sh) no NoSchedule taint and a default StorageClass; [images](../../../../../controller/deploy/nke-sbom/install/02-stage-images.sh) the controller image pulled by the digest `nke-release.yaml` names, external ones pre-pulled, on all 3 nodes through the nodes' own containerd | Prepare the cluster. Stage 0 prints the release lane and the source tag the spec names |
| 8 | [`./03-describe-site.sh`](../../../../../controller/deploy/nke-sbom/install/03-describe-site.sh) | `site-config.yaml` | redacted summary; out dir with BootstrapConfig, 4 values files, `site.env` | Describe the site once. Never `cat` the config on camera |
| 9 | `./install-all.sh --from install-cluster-api --until install-site-controller` | deploy path at the tag, `nke-release.yaml`, out dir | [Cluster API](../../../../../controller/deploy/nke-sbom/install/04-install-cluster-api.sh) Available; [11 items](../../../../../controller/deploy/nke-sbom/install/05-install-site-controller.sh) applied in order, the four external charts from the refs in `nke-release.yaml`; provider check (CAPNICo CRDs, Deployment, Secret) and plane check (6 releases deployed, `/readyz` ok, no CRD warning) | Install the site controller |
| 10 | [`./06-secure-and-register.sh`](../../../../../controller/deploy/nke-sbom/install/06-secure-and-register.sh) | kubeconfig | CAPI deployments patched; `nkectl get providers` shows nico CONFIGURED | nkectl now talks to the new site |

## Act 3: create a tenant cluster (about 6 min raw)

| # | Command | Input | Expected output | Purpose |
|---|---|---|---|---|
| 11 | `nkectl create cluster -f demo-tenant-site.yaml` | `demo-tenant-site.yaml`: 1 control plane, 1 worker, nico provider, no VIP | `Cluster "tenant-1" created (id: …)` and a note that provisioning is async | One API call to the site controller |
| 12 | `nkectl get clusters` (second pane: `demo-tenant-create.sh` `--watch`) | none | `Provisioning`, then `Provisioned`; Machines appear, nico-sim containers start | Controller runs the workflow through CAPI and CAPNICo |
| 13 | `nkectl get kubeconfig tenant-1 --output-file tenant.kubeconfig && kubectl --kubeconfig tenant.kubeconfig get nodes` | tenant name | tenant nodes Ready | The tenant is a real cluster |
| 14 | `nkectl delete cluster tenant-1` | tenant name | `Deleting`, then the row disappears and the machines are gone | Lifecycle is complete |

`demo-tenant-create.sh` wraps steps 11, 12 and 14 with polling for rehearsals. On camera, type the `nkectl` commands.

## Epilogue (2 min)

| # | Command | Input | Expected output | Purpose |
|---|---|---|---|---|
| 15 | `cat` `gitops/applicationset.yaml` | the 11 items | one Argo Application per item, sync waves in list order, values from the same files | The manual install is the baseline. The same items extend to GitOps without changing the deploy path |
| 16 | Optional: agent prompt "Read install-site-controller.md and write the install scripts", then `diff -r agent-out/ controller/deploy/nke-sbom/install/` | the guide | small or empty diff | The doc stands alone |

Optional 401 beat, not scripted end to end: `demo-auth-dev.sh` applies the dev auth objects; enforcing still needs stage 5 rerun with `apiAuth` values. Dev scaffolding, never on a real site.

## Teardown, off camera

| # | Command | Expected output |
|---|---|---|
| T1 | `demo-nico-sim.sh` `down` | nico-sim stopped, tenant containers removed |
| T2 | `demo-hosts-down.sh` `--keep-kubeconfig` | node containers gone, `nke-demo.kubeconfig` kept |

Only if site-config keeps the VIP as the SPIRE address: `demo-net-up.sh` adds the one DNAT rule, `--down` removes it.

## Timing

| Act | Raw | Edited target |
|---|---|---|
| 0 | 3 min | 1 min |
| 1 | 6.5 min | 3 min |
| 2 | 10 min, worst case 5 x `${PHASE_TIMEOUT}` | 5 min |
| 3 | 6 min | 3 min |

## Not yet true

- Every demo-only script is a draft validated offline only, except `demo-build-images.sh` and stage 2, which have run for real against the site cluster.
- Step 10 provider CONFIGURED and step 13 tenant nodes Ready are unproven on DinD plus nico-containers.
- Local helm is v3.18.6; the stages warn unless the pinned v4.2.4 is on PATH in the VM.
- Consuming the release spec does not exist yet: W27 to W30. Until then the stages read images from site-config and the local runtime, and charts from their repositories.
- NKD has no deploy-bundle output kind; the deploy path always comes from the pinned NKE tag. Say so if asked.
- Node3 of the current site cluster was NotReady with etcd CrashLoopBackOff at last check. The first full run needs a Ready quorum.
