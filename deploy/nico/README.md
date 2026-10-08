# deploy/nico

The deploy path for a NICo site controller on real hardware, with SPIRE. It is
the customer deploy path; the e2e lanes under `controller/deploy/` are not.

`deploy/install.sh` runs the install, in seven stages. This directory is what
stage 5 installs. To apply the directory alone, with no stages around it:
`deploy/nke-deploy.sh install --deploy-path deploy/nico`. The format is in
[deploy-path.md](../../docs/site-bootstrap/deploy-path.md).

Not a reference implementation: the item list and its order are derived from a
provisioning run that stood a site controller up on bare metal and had it
provision a tenant. Where that run and `capnico-e2e` disagree, this path follows
the run -- it is the one that has been exercised on real hardware.

## Where this sits

Four steps, three owners:

| step | who | produces |
|---|---|---|
| 1. create and image the instance | **not NKE** -- the NICo API, iPXE and cloud-init | a booted host running `nke-agent` |
| 2. `nkectl bootstrap site` | nkectl, on the instance | a **vanilla** Kubernetes cluster |
| 3. this deploy path | `deploy/install.sh`, on the node | the management plane |
| 4. `nkectl create cluster` | the site controller itself | tenant clusters |

Step 2 leaves bare Kubernetes on purpose. `bootstrap site` stands up a
throwaway kind cluster carrying Postgres, Temporal, SPIRE, CAPI and CWE, uses
that CWE to drive `kubeadm init` on the host over the node agent, and then
deletes it -- taking its record of the cluster with it. That is why a site
controller cannot later be scaled or upgraded through the API, and why step 3
starts from nothing.

Step 3 runs ON the node, not against it from a workstation. That is what makes
the image story cheap: the node's containerd IS the cluster's containerd, so
an image imported there resolves with `imagePullPolicy: IfNotPresent` and needs
no registry credential. CAPNICo is the one exception, and the one pull secret.

What the node needs, all present on the reference instance type:

- `helm`, `kubectl`, `git`, `python3` (the executor's item parser and template
  renderer are Python), plus `clusterctl` for the prerequisites
- a checkout laid out so that `deploy/` and `controller/` are siblings --
  `controller/release.yaml` reaches its chart as
  `../../controller/deploy/charts/controller`
- egress to `registry-1.docker.io` (the Bitnami OCI chart),
  `spiffe.github.io` and `go.temporal.io` (the spire-crds and temporal
  repositories), and GitHub if the spire chart is materialised there
- for the nktp item, egress to `open-telemetry.github.io`,
  `prometheus-community.github.io` and `kubernetes-sigs.github.io`, where the
  chart's dependencies come from, and to `docker.io`, `ghcr.io`, `quay.io` and
  `registry.k8s.io`, where its images come from

## Before this runs

A fresh cluster is required: this path installs the controller as release `nke-controller`, with database `nke-controller` and Temporal namespace `nke-controller`. Earlier revisions used release `controller`, database `cloud-workflow` and the chart's Temporal names. Helm cannot take over the old release's objects, and Postgres runs its `initdb` scripts only on an empty volume; nothing migrates the old database.

`deploy/nke-deploy.sh` installs the items below and nothing else. The stages in
`deploy/stages/` do the rest, in this order:

| Need | Stage |
|---|---|
| Control-plane nodes schedulable; a default StorageClass. A bare site ships none, and every PVC sits Pending without one. The stage installs rancher local-path when nothing else is default; a site with real storage sets `SKIP_STORAGECLASS=1` and marks its own class | `01-prepare-cluster.sh` |
| The controller image in each node's containerd, namespace `k8s.io`, pulled by digest from the release manifest. The node's containerd is the cluster's, so the image resolves with `imagePullPolicy: IfNotPresent` and needs no pull secret. CAPNICo is the one exception; its Secret is the `30-ghcr-imagepull` template | `02-stage-images.sh` |
| The per-site values files and the BootstrapConfig JSON the `nke-base-config` templates render against | `03-describe-site.sh` |
| cert-manager, Cluster API core and the kubeadm providers, with `clusterctl init`. cert-manager first because CAPI's manifests embed an Issuer and a Certificate | `04-install-cluster-api.sh` |

## After this runs

`06-secure-and-register.sh` patches the Cluster API controller Deployments with
`controller/hack/apply-capi-spiffe-exec-auth.sh`: a `csi.spiffe.io` volume and
the SPIFFE JWT exec-auth plugin, so Cluster API reaches tenant clusters with a
SPIFFE JWT rather than a stored admin kubeconfig. It is not an item: it patches
Deployments `clusterctl init` owns, and it waits on the `csi.spiffe.io`
CSIDriver that the `spire` item installs. The same stage registers the site
with `nkectl config set-site`.

## Release manifest

`release-manifest.yaml` pairs with this directory by item name. It pins the
published artifacts: the `controller` image, and the four external charts
`postgresql`, `spire-crds`, `temporal` and `spire`, each by OCI reference and
digest. `nke-deploy.sh --release-manifest` pulls a chart named there by digest,
verifies its chart layer, and installs that file instead of fetching from
`chart.repository`. The `nktp` and `controller` charts are not in the manifest;
they come from this checkout through `chart.path`. The manifest's `source.tag`
names the checkout they must come from.

Each chart item in the manifest carries a `version`. Check Readiness compares
it with `chart.version` in that item's `release.yaml` here and fails on a
mismatch. The two files describe one release; the deploy path says what and in
which order, the manifest says from where.

## Per-site values

Every item's committed values are site-independent. What varies comes in
through `--values-for`. `deploy/lib/describe_site.py` renders those files from
`site-config.yaml`; `controller/hack/ci/deploy-stack.sh` derives the same values
for the e2e lanes from environment variables.

| value | e2e derivation | on a NICo site |
|---|---|---|
| trust domain | `${KIND_CLUSTER_NAME}.nke.local` | the trust domain the site's nodes were bootstrapped with (`spire.trustDomain` in the site spec); never derived from a name |
| management VIP | kind control-plane IP | the site controller's node IP |
| SPIRE admin path | `/nke-service/nke-controller` | the same; it is a constant |
| TPM attestor **server** checksum | pinned per arch in `resolve_tpm_checksum()` | the same pin for the same arch |
| OIDC domains / IP SANs | VIP plus the kind gateway | the VIP |
| discovery URL | `https://<VIP>:30443/.well-known/openid-configuration` | the same shape |
| Postgres / Temporal password | generated per run | site secret, on BOTH Temporal datastores |
| controller image | built per run | a released tag |
| profiles image | built per run | the site's fleet profile content |
| telemetry backend | not installed | the bundled development backend, until the site sets an `nktp:` block in `site-config.yaml` |
| telemetry `k8s.cluster.name` | not installed | `site.name`, rendered into the BootstrapConfig as `ClusterName` |

The TPM checksum is easy to confuse: the value in `spire/values.yaml`
(`spire-server.nodeAttestor.tpmDirect.checksum`) is the **server** plugin, and
is unrelated to `TPM_PLUGIN_CHECKSUM` in `/etc/spire-agent/spire-agent.env` on
a node, which is the **agent** plugin. Both are per-architecture.

## SPIRE

`spiffe.enabled=true`, and the five SPIRE items are the same ones
`capnico-e2e` installs and proves on every run.

Node-side needs nothing built: the OS profile writes
`/etc/spire-agent/agent.conf` and `initial-trust-bundle.pem` into cloud-init,
which is one of the two mutually exclusive paths `os-image/SPEC.md` allows --
the other being `/etc/nke/spire.env` plus the image's renderer. A profile that
takes the first path works on an image that lacks the second.

Two ordering facts worth knowing before the first run, neither of which any
lane has exercised on NICo:

- CWE fills `initial-trust-bundle.pem` from its own SPIFFE workload socket, so
  **CWE must hold an SVID before it can bootstrap a node**.
- Tenant nodes must reach the SPIRE server at the address the profile renders.
  In e2e that is a kind NodePort; here it is the VIP, published by the
  `spire-server-nodeport` item.

The site controller's own node is a separate matter and stays plaintext: it is
provisioned outside CWE, and when it bootstraps the only SPIRE alive is the one
in the throwaway kind cluster, which is deleted. Nothing dials that node
afterwards.

## Telemetry

The `nktp` item installs the NKTP chart from this checkout
(`nktp/deploy/charts/nktp`), with the values in `nktp/values.yaml`. It comes
straight after `capnico` because it installs the monitoring CRDs, and three
later items create monitors that need them:

| item | what it adds | where |
|---|---|---|
| `temporal` | a ServiceMonitor per server role | `temporal/overlay.yaml` |
| `spire` | PodMonitors for the server and the agent | `spire/overlay.yaml` |
| `controller` | its ServiceMonitor, a metrics NetworkPolicy that admits `nktp`, and traces to the NKTP traces gateway | `controller/overlay.yaml` |

The chart's own control-plane monitors cover the API server, etcd and CoreDNS;
`nktp/values.yaml` turns off the scheduler, controller manager and kube-proxy
ones, which NKE clusters cannot serve. This path creates no monitors for other
components: each component ships its own, and one that does not is not scraped.

Everything NKTP exports carries `k8s.cluster.name`, the site's name. Nothing
in the cluster knows it -- NKE's kubeadm configuration names every cluster
`kubernetes` -- so `nke-base-config` renders it from BootstrapConfig's
`ClusterName` into the `nktp-cluster` ConfigMap, and the watchdog and the three
gateways, which are what export to the backend, read it from there. A config
without `ClusterName` stops the install at that template.

Everything exports to the chart's bundled development backend by default. It
keeps data in memory and has no authentication, so it is for evaluation. A site
with a real backend sets an `nktp:` block in `site-config.yaml`; stage 3 writes
it to `nktp.yaml` and stage 5 passes it as `--values-for nktp=`. The chart
README's "The backend" section lists the keys that block sets.

Not scraped yet: the Postgres item runs no metrics exporter; CAPI core and the
kubeadm providers serve metrics only to a caller with a bearer token, which the
node Target Allocator does not send; the scheduler and controller manager
listen on 127.0.0.1 only; and cert-manager, NodeLocal DNSCache,
OVN-Kubernetes and CAPNICo are installed without a monitor. Their pods' state still arrives through
kube-state-metrics.
