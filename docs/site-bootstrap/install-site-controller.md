# Install the Site Controller

`nkectl bootstrap site` leaves a plain Kubernetes cluster. This page installs the management plane onto it: Postgres, Temporal, SPIRE, [CAPNICo](https://github.com/dsx-ai-factory/cluster-api-provider-nico), NKTP and the NKE controller.

The install has seven stages. Each stage has a run step and a check step. Each run step is safe to repeat. You can stop after any stage and resume later.

A reference implementation lives at [`controller/deploy/nke-sbom/install/`](../../controller/deploy/nke-sbom/install/), one script per stage. The command blocks on this page are extracted from those scripts. This page is the authority. When a script and this page disagree, this page wins.

To run every stage in order:

```bash
export SITE_KUBECONFIG=./my-site.kubeconfig
controller/deploy/nke-sbom/install/install-all.sh
```

`--from NAME` resumes at a stage. `--until NAME` stops after one. `NAME` is the script name without its number, for example `--from install-cluster-api`. The [install README](../../controller/deploy/nke-sbom/install/README.md) lists every environment variable the scripts read.

## Before You Begin

| Requirement | Notes |
|---|---|
| The site cluster kubeconfig | Written by `nkectl bootstrap site`. Every command below takes it through `SITE_KUBECONFIG` |
| An odd node count | One, three or five nodes, all control-plane |
| The bootstrap site spec | You need its `spire.trustDomain`, `spire.caCertFile` and `spire.caKeyFile` values. See [Configuration](configuration.md) |
| A full checkout of this repository | The controller chart and the NKTP chart are installed from the tree, not from a registry |
| `helm` `v4.2.4` | The version `make/tool-versions.mk` pins. Helm 3 cannot install these charts |
| `kubectl`, `clusterctl` `v1.12.3`, `python3` | `clusterctl` matches `CAPI_VERSION` in `make/tool-versions.mk` |
| Root shell access to every node | Stage 3 imports images into each node's containerd |
| `nkectl` | For the final stage. See [Download and Install nkectl](installation.md) |
| Egress | `github.com`, `ghcr.io`, `registry-1.docker.io`, `docker.io`, `quay.io`, `registry.k8s.io`, `spiffe.github.io`, `go.temporal.io`, `open-telemetry.github.io`, `prometheus-community.github.io`, `kubernetes-sigs.github.io`. An air-gapped site needs a mirror |

The last five hosts serve the NKTP chart's dependencies and images.

## Artifacts

| Artifact | Build or pull | How |
|---|---|---|
| NKE controller image | Build | `make -C cli images` builds `controller/Dockerfile` for the host architecture. The Dockerfile is architecture-neutral |
| Profiles image | Build | Start from `controller/profiles/example`. See [Supply OS and config profiles](../../controller/profiles/README.md) |
| CAPNICo image | Pull | Public, multi-arch. The pinned manifest at `controller/deploy/nke-sbom/capnico/capnico.yaml` names it |
| Postgres, Temporal, SPIRE, NKTP dependency charts and images | Pull | From the egress hosts above |
| cert-manager, Cluster API | Pull | `clusterctl init` fetches them from `github.com` |
| `nkectl` | Download or build | [Download and Install nkectl](installation.md) |

The cluster's nodes are the cluster's container hosts. An image imported into a node's `k8s.io` containerd namespace resolves with `imagePullPolicy: IfNotPresent` and needs no registry or pull secret. That is how the controller and profiles images reach the cluster. CAPNICo is the one image the cluster pulls.

## 1. Check Readiness

Confirm the tools, the checkout, egress, the node count and the architecture before anything changes.

<!-- stage:00-check-readiness begin -->
<!-- stage:00-check-readiness end -->

### Check

Every tool is on `PATH` at its pinned version. The checkout holds the controller and NKTP charts. Each egress host answers. The node count is odd. The CAPNICo image has a manifest for the node architecture. A warning names API authentication as off.

### If It Fails

The report names the missing item. Fix it and run the stage again. Nothing on the cluster has changed.

## 2. Prepare the Site Cluster

Make the nodes schedulable and give the cluster a default StorageClass.

### Choices

Every node is a control-plane node and carries the `NoSchedule` taint. Nothing in later stages tolerates it, so the stage removes it.

A bare site ships no StorageClass. Postgres and SPIRE need one. The stage installs a local-path provisioner. It stores data on one node's disk, so it is single-node durability. A site with real storage should use its own CSI driver instead: set `SKIP_STORAGECLASS=1` and create a default StorageClass yourself before the check.

<!-- stage:01-prepare-cluster begin -->
<!-- stage:01-prepare-cluster end -->

### Check

No node carries `node-role.kubernetes.io/control-plane:NoSchedule`. Exactly one StorageClass is marked default.

### If It Fails

Two default StorageClasses fail the check. Remove the `storageclass.kubernetes.io/is-default-class` annotation from one. The taint removal skips nodes that are already untainted, so a rerun is safe.

## 3. Stage Images

Import the controller and profiles images into every node's containerd.

<!-- stage:02-stage-images begin -->
<!-- stage:02-stage-images end -->

### Check

`ctr -n k8s.io images ls` on each node lists both images. The check prints each digest.

### If It Fails

The namespace is `k8s.io`, not `default`. An image in the wrong namespace is invisible to the kubelet and the controller pod fails with a pull error. Import single-manifest images; an OCI index with an attestation manifest makes the kubelet pull by digest. The kubelet garbage-collects unused images under disk pressure, so stage 6 repeats this check first.

## 4. Describe Your Site

Write one `site-config.yaml` and render it into the values and configuration the install needs.

The file holds credentials. Keep it out of git. The renderer writes to `OUT_DIR` with mode `0700`, prints a redacted summary, and writes no Secret to disk. The schema is unstable and may change between releases.

| Key | Feeds | Rule |
|---|---|---|
| `site.name` | Release labels, telemetry cluster name | Required |
| `site.trustDomain` | SPIRE, controller `spiffe.trustDomain` | Required. Must equal `spire.trustDomain` in the bootstrap site spec. Never derived from the name |
| `site.vip` | SPIRE server address, OIDC discovery URL, IP SANs, the `set-site` URL | Required |
| `site.arch` | TPM attestor server checksum | Required. `amd64` or `arm64` |
| `spire.caCertFile`, `spire.caKeyFile` | SPIRE upstream authority | Required. The same files the bootstrap site spec names |
| `db.password` | Postgres, both Temporal datastores, the controller database | Optional. Generated once and reused from `OUT_DIR` on rerun |
| `images.controller`, `images.profiles` | Controller chart | Required. No default |
| `nico.endpoint`, `nico.orgId`, `nico.token`, `nico.apiName` | `nico-credentials` Secret | Required |
| `registry.server`, `registry.username`, `registry.token` | `ghcr-imagepull` Secret | Required. A fine-grained token with `read:packages` is enough |
| `tenant.user`, `tenant.password` | `dgxc-tenant-credentials` Secret | Required |

The trust domain and the CA are the two values that tie the permanent SPIRE server to the bootstrap. Every node's SPIRE agent already trusts the bootstrap CA and carries the bootstrap trust domain. Reusing both lets the agents move to the permanent server. Changing either changes every SPIFFE ID and strands the agents. See [A SPIFFE trust domain](../first-cluster.md#real-site).

<!-- stage:03-describe-site begin -->
<!-- stage:03-describe-site end -->

### Check

The renderer exits 0. No referenced key is empty. When a Postgres Secret already exists on the cluster, the rendered password matches it.

### If It Fails

The renderer names the empty or missing key. A password mismatch means `OUT_DIR` was deleted after an install. Restore it, or set `db.password` to the live value from the `postgres` namespace.

## 5. Install Cluster API

Install cert-manager, Cluster API core and the kubeadm providers at the version this repository pins.

Cluster API comes first because CAPNICo needs its CRDs, the SPIRE item waits on a cert-manager Certificate, and the controller reads the Machine CRDs once at start. [Infrastructure Providers](infrastructure-providers.md) explains the pin and the ordering.

<!-- stage:04-install-cluster-api begin -->
<!-- stage:04-install-cluster-api end -->

### Check

The Deployments in `capi-system`, `capi-kubeadm-bootstrap-system`, `capi-kubeadm-control-plane-system` and `cert-manager` are Available.

### If It Fails

A hang on cert-manager means the control-plane taint is still present. Run stage 2 again. `clusterctl init` skips providers that are already installed, so a rerun is safe. Only a conflicting provider version is an error.

## 6. Install the Site Controller

Apply the deploy items in list order: base configuration, SPIRE node ports, CAPNICo, then the NKTP, Postgres, SPIRE, Temporal and controller releases.

### Choices

**CAPNICo route.** This stage applies the pinned manifest. The Helm and `clusterctl` routes in [Infrastructure Providers](infrastructure-providers.md) are whole replacements for this stage, not alternatives inside it. The manifest pre-creates `capnico-system` and bakes in the annotation edit the controller needs. Mixing routes gives two owners for one set of objects.

**Security posture.** The controller runs as root with a writable root filesystem. The `materialize-profiles` init container copies the profiles image into a shared volume and needs that. Restore a non-root design before production use.

<!-- stage:05-install-site-controller begin -->
<!-- stage:05-install-site-controller end -->

### Check

Two checks run. The provider check: CAPNICo CRDs are Established, the `capnico-system` Deployment is Available, and `nico-credentials` exists. The plane check: every release in `helm list -A` is `deployed`, `/readyz` reports the database and Temporal as ok, and the controller log has no "CRD not installed" warning.

### If It Fails

Run `helm status <release> -n <namespace>` for the release the check names.

| Status | Action |
|---|---|
| `failed` | Run the stage again. `helm upgrade --install` recovers a failed release |
| `pending-install`, `pending-upgrade` | `helm uninstall <release> -n <namespace>`, then run the stage again |

A `--wait` timeout leaves the release `failed`. A killed helm process leaves it `pending-*`. The scripts write pods, events, `helm list` and controller logs to `OUT_DIR/diagnostics/<stage>/` on failure. `PHASE_TIMEOUT` sets the per-release wait; the default is 15 minutes.

A pull error on the controller pod means the image is missing from a node. Run stage 3 again.

## 7. Secure and Register the Site

Patch the Cluster API controllers to authenticate to tenant clusters with SPIFFE JWTs, then register the site with `nkectl`.

### Choices

**API authentication is off by default.** The controller API is plain HTTP on NodePort `30080` on every node IP, with no token required. Anyone who can reach a node can call every endpoint. Turn authentication on before exposing the site: [Configuring NKE API Authentication](../../controller/docs/configuring-api-authentication.md).

<!-- stage:06-secure-and-register begin -->
<!-- stage:06-secure-and-register end -->

### Check

The patched Cluster API Deployments finish their rollout. `nkectl get providers` lists `nico` as `CONFIGURED`.

`CONFIGURED` means the CRDs are established, the controller runs and the credentials Secret exists. It does not prove the credentials work. CAPNICo reads the Secret on every reconcile, so a corrected Secret takes effect without a restart.

### If It Fails

The rollout waits for the `csi.spiffe.io` CSIDriver, which the SPIRE release installs. If it is missing, stage 6 did not finish. `clusterctl upgrade` replaces the patched Deployments, so run this stage again after every Cluster API upgrade.

## Remove

Uninstall in reverse item order, then remove Cluster API:

```bash
helm uninstall controller -n cloud-workflow-engine
helm uninstall spire -n spire-server
helm uninstall temporal -n temporal
helm uninstall spire-crds -n spire-server
helm uninstall postgres -n postgres
helm uninstall nktp -n nktp
kubectl delete -f controller/deploy/nke-sbom/capnico/capnico.yaml
clusterctl delete --all
```

Helm leaves PersistentVolumeClaims behind. Delete the `postgres` and `spire-server` PVCs yourself when the data is no longer needed.

## Upgrade

1. Build the new controller image and run stage 3 to import it.
2. Update `images.controller` in `site-config.yaml` and run stage 4.
3. Run `install-all.sh --from install-site-controller`.

Stage 7 runs again as part of that range. [Tenant Cluster Kubernetes Upgrades](../operations/upgrade.md) covers upgrades of tenant clusters.

## Next Steps

- [Your first cluster](../first-cluster.md#7-create-a-tenant-cluster) — create a tenant cluster on the registered site
- [Infrastructure Providers](infrastructure-providers.md) — prove the NICo credentials before committing hardware
