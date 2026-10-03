# Install the Site Controller

`nkectl bootstrap site` leaves a plain Kubernetes cluster. This page installs the management plane onto it: Postgres, Temporal, SPIRE, [CAPNICo](https://github.com/dsx-ai-factory/cluster-api-provider-nico), NKTP and the NKE controller.

The installer takes two inputs. `nke-release.yaml`, the release spec, says which artifacts to install and where to pull each one from, by digest. `site-config.yaml` says what is specific to the site. `RELEASE_FILE` points at the spec on disk. `RELEASE_REF` names an NKD Release instead, and stage 1 produces the spec from `nkdctl release verify`. Everything else is derived.

The install has seven stages. Each stage has a run step and a check step. Each run step is safe to repeat. You can stop after any stage and resume later.

An executable form of this guide lives at [`controller/deploy/nke-sbom/install/`](../../controller/deploy/nke-sbom/install/), one script per stage. The command blocks on this page are extracted from those scripts. This page is the authority. When a script and this page disagree, this page wins.

To run every stage in order:

```bash
export SITE_KUBECONFIG=./my-site.kubeconfig
export RELEASE_FILE=./nke-release.yaml
controller/deploy/nke-sbom/install/install-all.sh
```

`nke-release.yaml` names every artifact the install pulls, each as an OCI reference with its digest. It is converted from `nkdctl release verify` output or assembled locally; `controller/deploy/nke-sbom/install/nke-release.example.yaml` shows the shape. Setting `RELEASE_REF` to the Release reference instead runs the verification and the conversion for you. The install README has the details.

`--from NAME` resumes at a stage. `--until NAME` stops after one. `NAME` is the script name without its number, for example `--from install-cluster-api`. The [install README](../../controller/deploy/nke-sbom/install/README.md) lists every environment variable the scripts read.

## Release Spec

`nke-release.yaml` names every artifact the install pulls. It has the shape of `nke-deploy.yaml`: a flat `items` list, one entry per artifact, named after the deploy item it serves. `ref` carries the digest, so there is no separate checksum file.

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
| `release` | The OCI reference of the Release the spec came from. Informational |
| `lane`, `scope` | How the artifacts were verified. Stage 1 prints both. A hand-written spec says `lane: local-assembly` and `scope: none` |
| `source` | The repository, tag and commit the deploy path comes from. The checkout must be at `source.tag` |
| `items[].name` | The deploy item in `nke-deploy.yaml` the artifact serves. The names are equal |
| `items[].type` | `image`, `chart` or `binary` |
| `items[].ref` | Where to pull from, by digest. The registry can be public, a mirror or private. The installer does not distinguish |
| `items[].version` | Charts only. Must equal the pin in that item's `release.yaml` at `source.tag` |

There are two ways to get the file.

- **From an NKD Release.** `nkdctl release verify` checks the signature and provenance of a Release and prints its artifacts with references. The converter `nkd-release-to-nke.py` in `controller/deploy/nke-sbom/install/` turns that output into this file. NKD's description carries no source pin, so you add `source` yourself. With `RELEASE_REF` set, stage 1 does both.
- **By hand.** Build the artifacts, push them to a registry the nodes reach, and write the file with those refs. See [Artifacts](#artifacts). An air-gapped site points every ref at its own registry.

## Before You Begin

| Requirement | Notes |
|---|---|
| The site cluster kubeconfig | Written by `nkectl bootstrap site`. Every command below takes it through `SITE_KUBECONFIG` |
| A release spec | `nke-release.yaml` through `RELEASE_FILE`, or an NKD Release through `RELEASE_REF`. See [Release Spec](#release-spec) |
| An odd node count | One, three or five nodes, all control-plane |
| The bootstrap site spec | You need its `spire.trustDomain`, `spire.caCertFile` and `spire.caKeyFile` values. See [Configuration](configuration.md) |
| A full checkout of this repository | At `source.tag` from the spec. The controller chart and the NKTP chart are installed from the tree, not from a registry |
| `helm` `v4.2.4` | The version `make/tool-versions.mk` pins. Helm 3 cannot install these charts |
| `oras` `1.3.3` | The version `make/tool-versions.mk` pins. Stage 6 pulls the external charts with it |
| `kubectl`, `clusterctl` `v1.12.3`, `python3` | `clusterctl` matches `CAPI_VERSION` in `make/tool-versions.mk` |
| Root shell access to every node | Stage 3 pulls images through each node's containerd |
| `nkectl` | For the final stage. See [Download and Install nkectl](installation.md) |
| The release spec | `nke-release.yaml`, or the Release reference it is converted from. See the install README |
| Egress | The registries your refs point at, reachable from the nodes. The nodes also pull the images the charts, the CAPNICo manifest and the Cluster API providers run, and `clusterctl init` fetches from `github.com`. An air-gapped site mirrors those images |

## Artifacts

Build three artifacts from the clone at `source.tag`.

| Artifact | How |
|---|---|
| NKE controller image | `make -C cli images` builds `controller/Dockerfile` for the host architecture. Push it to a registry the nodes reach. Its ref is the `controller` item in the spec |
| Profiles image | Start from `controller/profiles/example`. See [Supply OS and config profiles](../../controller/profiles/README.md). Push it to a registry the nodes reach and name it in `images.profiles` |
| `nkectl` | `make build`, or [Download and Install nkectl](installation.md) |

Pull the four external charts at the versions each item's `release.yaml` pins, with `helm pull`, and push them to a registry the nodes reach with `helm push` or `oras push`. Put each ref and version in the spec.

The cluster pulls the rest itself: the CAPNICo image the pinned manifest at `controller/deploy/nke-sbom/capnico/capnico.yaml` names, cert-manager and Cluster API through `clusterctl init`, and the images the charts run.

## 1. Check Readiness

Confirm the tools, the checkout, the release spec, egress, the node count and the architecture before anything changes. With `RELEASE_REF` set, the stage runs `nkdctl release verify` first and writes `nke-release.yaml` into `OUT_DIR`.

<!-- stage:00-check-readiness begin -->
```bash
# Hosts the runner reaches directly, sorted by host. Stage 4 fetches Cluster
# API and cert-manager from github.com. The rest are the image registries the
# charts' images and CAPNICo come from: ghcr.io, quay.io and registry.k8s.io.
# Charts are pulled from the refs in nke-release.yaml, and stage 2 pre-pulls
# every image from the nodes, so a miss here is a warning: the nodes may
# reach a mirror the runner does not.
EGRESS_URLS=(
  https://ghcr.io/v2/
  https://github.com/
  https://quay.io/v2/
  https://registry.k8s.io/v2/
)
```
<!-- stage:00-check-readiness end -->

### Check

The release spec parses; its lane and scope are printed, and a lane other than `pipeline-attested` is a warning. The checkout is at the tag the spec names, and every chart version in the spec equals the pin in `release.yaml` there. Each registry host in the spec answers from a node when `NODE_EXEC` says how to reach one. Every tool is on `PATH` at its pinned version. The checkout holds the controller and NKTP charts. An egress host the runner cannot reach is a warning, because the nodes do the pulling. The node count is odd. The CAPNICo image has a manifest for the node architecture. A warning names API authentication as off.

### If It Fails

The report names the missing item. Fix it and run the stage again. Nothing on the cluster has changed. A version mismatch names the item, the version in the spec and the pin in `release.yaml`. Move the checkout to `source.tag`, or fix the spec.

## 2. Prepare the Site Cluster

Make the nodes schedulable and give the cluster a default StorageClass.

### Choices

Every node is a control-plane node and carries the `NoSchedule` taint. Nothing in later stages tolerates it, so the stage removes it.

A bare site ships no StorageClass. Postgres and SPIRE need one. The stage installs a local-path provisioner. It stores data on one node's disk, so it is single-node durability. A site with real storage should use its own CSI driver instead: set `SKIP_STORAGECLASS=1` and create a default StorageClass yourself before the check.

<!-- stage:01-prepare-cluster begin -->
```bash
# Let workloads schedule on control-plane nodes. Only nodes that carry the
# taint are touched.
printf '%s\n' "${tainted}" | while read -r node; do
  [[ -n "${node}" ]] || continue
  kc taint nodes "${node}" node-role.kubernetes.io/control-plane:NoSchedule-
done

# Default StorageClass for Postgres and Temporal. Skipped when one exists
# or when SKIP_STORAGECLASS is set.
if [[ -z "${SKIP_STORAGECLASS}" && -z "${default_sc}" ]]; then
  kc apply -f "${LOCAL_PATH_URL}"
  kc annotate storageclass local-path storageclass.kubernetes.io/is-default-class=true --overwrite
  kc -n local-path-storage rollout status deployment/local-path-provisioner --timeout="${REQUIRE_TIMEOUT}"
fi
```
<!-- stage:01-prepare-cluster end -->

### Check

No node carries `node-role.kubernetes.io/control-plane:NoSchedule`. Exactly one StorageClass is marked default.

### If It Fails

Two default StorageClasses fail the check. Remove the `storageclass.kubernetes.io/is-default-class` annotation from one. The taint removal skips nodes that are already untainted, so a rerun is safe.

## 3. Stage Images

Pull every image onto every node before anything schedules. The controller image is pulled by digest from its ref in the spec, through each node's containerd, into the `k8s.io` namespace. The external images are pre-pulled the same way: the images the charts run, the profiles image among them, the CAPNICo image and the Cluster API provider images, as listed in `images.txt`. Only the node's containerd knows the site's mirrors, so the pull happens there and not on the machine running the install. The script reaches a node through `NODE_EXEC`, a command prefix with `{node}` in it: unset when the script runs on the node, `ssh root@{node}` for hosts, `nerdctl exec -i {node}` for container nodes.

<!-- stage:02-stage-images begin -->
```bash
# The provider image is public. Pull it for the node architecture here, so
# the nodes need no registry credential.
if ! "${IMAGE_CLI}" image inspect "${CAPNICO_IMAGE}" >/dev/null 2>&1; then
  run "${IMAGE_CLI}" pull --platform "linux/${NODE_ARCH}" "${CAPNICO_IMAGE}"
fi

# One tarball per image, imported into every node that lacks the digest.
# Namespace k8s.io: an image in any other namespace is invisible to the kubelet.
mkdir -p "${OUT_DIR}/images"
for image in ${IMAGES} ${CAPNICO_IMAGE}; do
  digest="$(local_digest "${image}")"
  missing=""
  for node in ${NODES}; do
    if [[ -z "${DRY_RUN}" && -n "${digest}" && "$(node_digest "${node}" "${image}")" == "${digest}" ]]; then
      continue
    fi
    missing="${missing} ${node}"
  done
  if [[ -z "${missing}" ]]; then
    log "${image} ${digest} is on every node"
    continue
  fi
  tar="${OUT_DIR}/images/$(printf '%s' "${image}" | tr '/:@' '___').tar"
  run "${IMAGE_CLI}" save -o "${tar}" "${image}"
  for node in ${missing}; do
    log "${image} -> ${node}"
    exec_prefix="$(node_exec "${node}")"
    run_from "${tar}" ${exec_prefix} ctr -n k8s.io images import -
  done
  rm -f "${tar}"
done
```
<!-- stage:02-stage-images end -->

### Check

`ctr -n k8s.io images ls` on each node lists the controller image at the digest in the spec and every pre-pulled image. The check prints each digest and fails on a missing or stale one.

### If It Fails

A failed pull names the registry that refused and the node. Make that registry reachable from the node, or point the ref at one that is. The namespace is `k8s.io`, not `default`. An image in the wrong namespace is invisible to the kubelet and the controller pod fails with a pull error. The kubelet garbage-collects unused images under disk pressure, so stage 6 repeats this check first.

## 4. Describe Your Site

Write one `site-config.yaml` and render it into the values and configuration the install needs.

The file holds credentials. Keep it out of git. The renderer writes to `OUT_DIR` with mode `0700`, prints a redacted summary, and writes no Secret to disk. The schema is unstable and may change between releases.

The stage also writes two inventory files to `OUT_DIR`. `images.txt` lists every external image: the images the charts run with these values, the CAPNICo image and the Cluster API provider images. `version-bom.cdx.json` is a CycloneDX inventory with one component per spec item, keyed by deploy item and digest.

| Key | Feeds | Rule |
|---|---|---|
| `site.name` | Release labels, telemetry cluster name | Required |
| `site.trustDomain` | SPIRE, controller `spiffe.trustDomain` | Required. Must equal `spire.trustDomain` in the bootstrap site spec. Never derived from the name |
| `site.vip` | SPIRE server address, OIDC discovery URL, IP SANs, the `set-site` URL | Required |
| `site.arch` | TPM attestor server checksum | Required. `amd64` or `arm64` |
| `spire.caCertFile`, `spire.caKeyFile` | SPIRE upstream authority | Required. The same files the bootstrap site spec names |
| `db.password` | Postgres, both Temporal datastores, the controller database | Optional. Generated once and reused from `OUT_DIR` on rerun |
| `images.controller` | Controller chart | Optional. Defaults to the `controller` ref in the release spec. Set it only to override |
| `images.profiles` | Controller chart | Required. Your profiles image, in a registry the nodes reach |
| `nico.endpoint`, `nico.orgId`, `nico.token`, `nico.apiName` | `nico-credentials` Secret | Required |
| `registry.server`, `registry.username`, `registry.token` | `ghcr-imagepull` Secret | Required. A fine-grained token with `read:packages` is enough |
| `tenant.user`, `tenant.password` | `dgxc-tenant-credentials` Secret | Required |

The trust domain and the CA are the two values that tie the permanent SPIRE server to the bootstrap. Every node's SPIRE agent already trusts the bootstrap CA and carries the bootstrap trust domain. Reusing both lets the agents move to the permanent server. Changing either changes every SPIFFE ID and strands the agents. See [A SPIFFE trust domain](../first-cluster.md#real-site).

<!-- stage:03-describe-site begin -->
```bash
python3 "${INSTALL_DIR}/describe_site.py" render \
  --config "${SITE_CONFIG}" --out "${OUT_DIR}" --sbom-dir "${SBOM_DIR}"
```
<!-- stage:03-describe-site end -->

### Check

The renderer exits 0. No referenced key is empty. When a Postgres Secret already exists on the cluster, the rendered password matches it.

### If It Fails

The renderer names the empty or missing key. A password mismatch means `OUT_DIR` was deleted after an install. Restore it, or set `db.password` to the live value from the `postgres` namespace.

## 5. Install Cluster API

Install cert-manager, Cluster API core and the kubeadm providers at the version this repository pins.

Cluster API comes first because CAPNICo needs its CRDs, the SPIRE item waits on a cert-manager Certificate, and the controller reads the Machine CRDs once at start. [Infrastructure Providers](infrastructure-providers.md) explains the pin and the ordering.

<!-- stage:04-install-cluster-api begin -->
```bash
# Pinned, so clusterctl does not install whatever is latest. Installs
# cert-manager too. Safe to rerun: clusterctl skips what is present.
run clusterctl init --kubeconfig "${SITE_KUBECONFIG}" \
  --core "cluster-api:${CAPI_VERSION}" \
  --bootstrap "kubeadm:${CAPI_VERSION}" \
  --control-plane "kubeadm:${CAPI_VERSION}" \
  --wait-providers
```
<!-- stage:04-install-cluster-api end -->

### Check

The Deployments in `capi-system`, `capi-kubeadm-bootstrap-system`, `capi-kubeadm-control-plane-system` and `cert-manager` are Available.

### If It Fails

A hang on cert-manager means the control-plane taint is still present. Run stage 2 again. `clusterctl init` skips providers that are already installed, so a rerun is safe. Only a conflicting provider version is an error.

## 6. Install the Site Controller

Apply the deploy items in list order: base configuration, SPIRE node ports, CAPNICo, then the NKTP, Postgres, SPIRE, Temporal and controller releases.

The four external charts, Postgres, SPIRE CRDs, Temporal and SPIRE, are pulled from their refs in the spec with `oras` into `OUT_DIR/charts/<item>/<chart>-<version>.tgz`. The ref names a manifest; the sha256 of the pulled file must equal the digest of the chart layer in that manifest before the release is installed from the file. The controller and NKTP charts are installed from the tree.

### Choices

**CAPNICo route.** This stage applies the pinned manifest. The Helm and `clusterctl` routes in [Infrastructure Providers](infrastructure-providers.md) are whole replacements for this stage, not alternatives inside it. The manifest pre-creates `capnico-system` and bakes in the annotation edit the controller needs. Mixing routes gives two owners for one set of objects.

**Security posture.** The controller runs as root with a writable root filesystem. The `materialize-profiles` init container copies the profiles image into a shared volume and needs that. Restore a non-root design before production use.

<!-- stage:05-install-site-controller begin -->
```bash
# Imported images are eligible for kubelet garbage collection. Confirm they
# are still present before anything schedules.
if [[ -x "${INSTALL_DIR}/02-stage-images.sh" ]]; then
  "${INSTALL_DIR}/02-stage-images.sh" check ||
    die "staged images are missing. Rerun 02-stage-images.sh."
fi

HELM_TMP="$(mktemp -d "${TMPDIR:-/tmp}/nke-helm.XXXXXX")"

# item nke-base-config (template)
RENDER_TMP="$(mktemp -d "${TMPDIR:-/tmp}/nke-render.XXXXXX")"
run python3 "${SBOM_DIR}/../../build/render-template.py" "${OUT_DIR}/bootstrap-config.json" "${SBOM_DIR}/nke-sbom/nke-base-config" "${RENDER_TMP}"
for file in "${RENDER_TMP}"/*.yaml; do
  kc apply --server-side -f "${file}"
done
rm -rf "${RENDER_TMP}"

# item spire-tpm-pubhashes (manifest)
kc apply --server-side -f "${SBOM_DIR}/nke-sbom/spire-tpm-pubhashes/spire-tpm-pubhashes.yaml"

# item spire-server-nodeport (manifest)
kc apply --server-side -f "${SBOM_DIR}/nke-sbom/spire-server-nodeport/spire-server-nodeport.yaml"

# item spire-oidc-provider-nodeport (manifest)
kc apply --server-side -f "${SBOM_DIR}/nke-sbom/spire-oidc-provider-nodeport/spire-oidc-provider-nodeport.yaml"

# item capnico (manifest)
if [[ -n "${CAPNICO_IMAGE:-}" ]]; then
  CAPNICO_TMP="$(mktemp -d "${TMPDIR:-/tmp}/nke-capnico.XXXXXX")"
  cp "${SBOM_DIR}/nke-sbom/capnico/capnico.yaml" "${CAPNICO_TMP}/capnico.yaml"
  printf 'resources: [capnico.yaml]\nimages:\n  - name: %s\n    newName: %s\n    newTag: %s\n' \
    'ghcr.io/dsx-ai-factory/cluster-api-provider-nico/controller' "${CAPNICO_IMAGE%:*}" "${CAPNICO_IMAGE##*:}" >"${CAPNICO_TMP}/kustomization.yaml"
  run kubectl kustomize "${CAPNICO_TMP}" | kc apply --server-side -f -
  rm -rf "${CAPNICO_TMP}"
else
  kc apply --server-side -f "${SBOM_DIR}/nke-sbom/capnico/capnico.yaml"
fi

# item nktp (helm)

# building chart dependencies of ${SBOM_DIR}/nke-sbom/../../../nktp/deploy/charts/nktp
run env HELM_REPOSITORY_CONFIG="${HELM_TMP}/repositories.yaml" HELM_REPOSITORY_CACHE="${HELM_TMP}/cache" helm repo add dependency-1 https://kubernetes-sigs.github.io/metrics-server/
run env HELM_REPOSITORY_CONFIG="${HELM_TMP}/repositories.yaml" HELM_REPOSITORY_CACHE="${HELM_TMP}/cache" helm repo add dependency-2 https://open-telemetry.github.io/opentelemetry-helm-charts
run env HELM_REPOSITORY_CONFIG="${HELM_TMP}/repositories.yaml" HELM_REPOSITORY_CACHE="${HELM_TMP}/cache" helm repo add dependency-3 https://prometheus-community.github.io/helm-charts
run env HELM_REPOSITORY_CONFIG="${HELM_TMP}/repositories.yaml" HELM_REPOSITORY_CACHE="${HELM_TMP}/cache" helm dependency build "${SBOM_DIR}/nke-sbom/../../../nktp/deploy/charts/nktp" --skip-refresh

# nktp -> release nktp (nktp 0.1.0-rc.2) in nktp
# Site values for nktp are optional: stage 3 writes ${OUT_DIR}/nktp.yaml only
# when site-config.yaml has an nktp block.
nktp_extra=()
[[ ! -f "${OUT_DIR}/nktp.yaml" ]] || nktp_extra=(--values "${OUT_DIR}/nktp.yaml")
hl upgrade --install nktp "${SBOM_DIR}/nke-sbom/../../../nktp/deploy/charts/nktp" --namespace nktp --wait --timeout "${PHASE_TIMEOUT:-15m}" --values "${SBOM_DIR}/nke-sbom/nktp/values.yaml" ${nktp_extra[@]+"${nktp_extra[@]}"}

# item postgresql (helm)

# postgresql -> release postgres (postgresql 18.5.15) in postgres
# The chart is the postgresql item of the release spec, pulled by digest. The ref
# names a manifest with one chart layer: its title is the file name, its
# digest the sha256 of the file. The pull is skipped when the file is there.
chart_ref="$(release_ref postgresql)" || die "no chart item postgresql in ${RELEASE_FILE}"
chart_dir="${OUT_DIR}/charts/postgresql"
layer="$(chart_layer "${chart_ref}" postgresql-18.5.15.tgz)" || die "cannot read the chart layer of ${chart_ref}"
chart_digest="${layer%% *}"
chart_file="${chart_dir}/${layer#* }"
if [[ "$(sha256_file "${chart_file}")" != "${chart_digest}" ]]; then
  run oras pull "${chart_ref}" -o "${chart_dir}"
fi
chart_have="$(sha256_file "${chart_file}")"
if [[ -n "${DRY_RUN}" ]]; then
  check_skip "chart postgresql: layer digest check needs the pulled file (DRY_RUN)"
elif [[ "${chart_have}" != "${chart_digest}" ]]; then
  die "chart postgresql: ${chart_file} is ${chart_have:-missing}; the spec's chart layer is ${chart_digest}"
fi
hl upgrade --install postgres "${chart_file}" --namespace postgres --wait --timeout "${PHASE_TIMEOUT:-15m}" --values "${SBOM_DIR}/nke-sbom/postgresql/values.yaml" --values "${OUT_DIR}/postgresql.yaml"

# item spire-crds (helm)

# spire-crds -> release spire-crds (spire-crds 0.6.1) in spire-server
# The chart is the spire-crds item of the release spec, pulled by digest. The ref
# names a manifest with one chart layer: its title is the file name, its
# digest the sha256 of the file. The pull is skipped when the file is there.
chart_ref="$(release_ref spire-crds)" || die "no chart item spire-crds in ${RELEASE_FILE}"
chart_dir="${OUT_DIR}/charts/spire-crds"
layer="$(chart_layer "${chart_ref}" spire-crds-0.6.1.tgz)" || die "cannot read the chart layer of ${chart_ref}"
chart_digest="${layer%% *}"
chart_file="${chart_dir}/${layer#* }"
if [[ "$(sha256_file "${chart_file}")" != "${chart_digest}" ]]; then
  run oras pull "${chart_ref}" -o "${chart_dir}"
fi
chart_have="$(sha256_file "${chart_file}")"
if [[ -n "${DRY_RUN}" ]]; then
  check_skip "chart spire-crds: layer digest check needs the pulled file (DRY_RUN)"
elif [[ "${chart_have}" != "${chart_digest}" ]]; then
  die "chart spire-crds: ${chart_file} is ${chart_have:-missing}; the spec's chart layer is ${chart_digest}"
fi
hl upgrade --install spire-crds "${chart_file}" --namespace spire-server --wait --timeout "${PHASE_TIMEOUT:-15m}"

# item temporal (helm)

# temporal -> release temporal (temporal 0.74.0) in temporal
# The chart is the temporal item of the release spec, pulled by digest. The ref
# names a manifest with one chart layer: its title is the file name, its
# digest the sha256 of the file. The pull is skipped when the file is there.
chart_ref="$(release_ref temporal)" || die "no chart item temporal in ${RELEASE_FILE}"
chart_dir="${OUT_DIR}/charts/temporal"
layer="$(chart_layer "${chart_ref}" temporal-0.74.0.tgz)" || die "cannot read the chart layer of ${chart_ref}"
chart_digest="${layer%% *}"
chart_file="${chart_dir}/${layer#* }"
if [[ "$(sha256_file "${chart_file}")" != "${chart_digest}" ]]; then
  run oras pull "${chart_ref}" -o "${chart_dir}"
fi
chart_have="$(sha256_file "${chart_file}")"
if [[ -n "${DRY_RUN}" ]]; then
  check_skip "chart temporal: layer digest check needs the pulled file (DRY_RUN)"
elif [[ "${chart_have}" != "${chart_digest}" ]]; then
  die "chart temporal: ${chart_file} is ${chart_have:-missing}; the spec's chart layer is ${chart_digest}"
fi
hl upgrade --install temporal "${chart_file}" --namespace temporal --wait --timeout "${PHASE_TIMEOUT:-15m}" --values "${SBOM_DIR}/nke-sbom/temporal/values.yaml" --values "${SBOM_DIR}/nke-sbom/temporal/overlay.yaml" --values "${OUT_DIR}/temporal.yaml"

# item spire (helm)

# waiting for certificate/oidc-provider-upstream-ca condition=Ready
kc -n spire-server get certificate/oidc-provider-upstream-ca >/dev/null 2>&1 ||
  die "phase spire requires certificate/oidc-provider-upstream-ca in namespace spire-server, which does not exist.
 is not part of this chart -- an earlier item supplies it. In this repository
at is the nke-base-config item, from ${SBOM_DIR}/k8s/oidc-upstream-ca.yaml."
kc -n spire-server wait --for=condition=Ready certificate/oidc-provider-upstream-ca --timeout="${REQUIRE_TIMEOUT:-180s}"

# spire -> release spire (spire 0.30.1) in spire-server
# The chart is the spire item of the release spec, pulled by digest. The ref
# names a manifest with one chart layer: its title is the file name, its
# digest the sha256 of the file. The pull is skipped when the file is there.
chart_ref="$(release_ref spire)" || die "no chart item spire in ${RELEASE_FILE}"
chart_dir="${OUT_DIR}/charts/spire"
layer="$(chart_layer "${chart_ref}" spire-0.30.1.tgz)" || die "cannot read the chart layer of ${chart_ref}"
chart_digest="${layer%% *}"
chart_file="${chart_dir}/${layer#* }"
if [[ "$(sha256_file "${chart_file}")" != "${chart_digest}" ]]; then
  run oras pull "${chart_ref}" -o "${chart_dir}"
fi
chart_have="$(sha256_file "${chart_file}")"
if [[ -n "${DRY_RUN}" ]]; then
  check_skip "chart spire: layer digest check needs the pulled file (DRY_RUN)"
elif [[ "${chart_have}" != "${chart_digest}" ]]; then
  die "chart spire: ${chart_file} is ${chart_have:-missing}; the spec's chart layer is ${chart_digest}"
fi
hl upgrade --install spire "${chart_file}" --namespace spire-server --wait --timeout "${PHASE_TIMEOUT:-15m}" --values "${SBOM_DIR}/nke-sbom/spire/values.yaml" --values "${SBOM_DIR}/nke-sbom/spire/overlay.yaml" --values "${OUT_DIR}/spire.yaml"

# item controller (helm)

# controller -> release controller (nke-controller 0.0.0) in cloud-workflow-engine
hl upgrade --install controller "${SBOM_DIR}/nke-sbom/../charts/controller" --namespace cloud-workflow-engine --wait --timeout "${PHASE_TIMEOUT:-15m}" --values "${SBOM_DIR}/nke-sbom/controller/values.yaml" --values "${SBOM_DIR}/nke-sbom/controller/overlay.yaml" --values "${OUT_DIR}/controller.yaml"
```
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

A digest mismatch names the item, the digest in the spec and the digest of the pulled chart. The registry does not hold what the spec names. Nothing was installed for that item; fix the spec or the registry and run the stage again.

A pull error on the controller pod means the image is missing from a node. Run stage 3 again.

## 7. Secure and Register the Site

Patch the Cluster API controllers to authenticate to tenant clusters with SPIFFE JWTs, then register the site with `nkectl`.

### Choices

**API authentication is off by default.** The controller API is plain HTTP on NodePort `30080` on every node IP, with no token required. Anyone who can reach a node can call every endpoint. Turn authentication on before exposing the site: [Configuring NKE API Authentication](../../controller/docs/configuring-api-authentication.md).

<!-- stage:06-secure-and-register begin -->
```bash
run "${HACK_DIR}/apply-capi-spiffe-exec-auth.sh" --kubeconfig "${SITE_KUBECONFIG}"

run nkectl config set-site \
  --name "${SITE_NAME}" \
  --url "http://${SITE_VIP}:30080" \
  --kubeconfig "${SITE_KUBECONFIG}"

run nkectl get providers
```
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

1. Build and push the new controller image. Put its ref in the release spec.
2. Run stage 3 to pull it on every node, then stage 4 to render.
3. Run `install-all.sh --from install-site-controller`.

Stage 7 runs again as part of that range. [Tenant Cluster Kubernetes Upgrades](../operations/upgrade.md) covers upgrades of tenant clusters.

## Extend to GitOps

The manual install above is the baseline. Every item in `nke-deploy.yaml` is a Helm chart plus values files, or plain YAML. A GitOps tool can deliver the same items without changing the deploy path.

- **Source.** The clone is a valid git source. The controller and NKTP charts are in the tree, at `controller/deploy/charts/controller` and `nktp/deploy/charts/nktp`. Postgres, SPIRE CRDs, Temporal and SPIRE come from the refs in the release spec. An OCI registry is a valid Helm source, so the same refs serve Argo CD.
- **Order.** The item list order maps to sync waves, one wave per item.
- **The Certificate.** The `spire` item declares a `requires:` on the `oidc-provider-upstream-ca` Certificate. In GitOps that is a wave boundary or a health check on the Certificate, not a `kubectl wait`.
- **Secrets.** The `nke-base-config` item renders three Secrets: `nico-credentials`, `ghcr-imagepull` and `dgxc-tenant-credentials`. Under GitOps those come from the operator's secret store. The namespaces and the Certificate chain in the same item are plain YAML and stay in git.
- **Imperative steps.** `clusterctl init` and `controller/hack/apply-capi-spiffe-exec-auth.sh` stay imperative, outside GitOps. Stages 1 to 5 and stage 7 do not change.

The per-site values stage 4 renders belong in your own repository or your secret store. [Deploy Repository](deploy-repository.md) describes the `argocd` item type for a deploy path that carries Argo CD Applications.

The Application below delivers the `temporal` item. It is illustrative and untested. The chart source is the repository and version from `temporal/release.yaml`; with a release spec, `repoURL` points at the registry in the `temporal` ref instead. The values come from the two in-tree values files through the clone as a second source. The sync wave is the item's position in the list.

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
          - $values/controller/deploy/nke-sbom/temporal/values.yaml
          - $values/controller/deploy/nke-sbom/temporal/overlay.yaml
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
