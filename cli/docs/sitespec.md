# nkectl bootstrap site — site spec file spec

`nkectl bootstrap site -f <sitespec-file>` reads one YAML site spec file. The
file is the single source of truth for a bootstrap run: it lists the
pre-provisioned nodes and the vanilla Kubernetes cluster to build on them.

The command loads the file, applies defaults, and validates every field before
it dispatches any work. Any invalid or missing field fails fast with a clear
error and no node is touched.

A ready-to-copy example is in [`examples/sitespec.yaml`](../examples/sitespec.yaml).

## Generating one

`nkectl generate sitespec` writes a complete site spec, so the file does not
have to be assembled by hand:

```bash
nkectl generate sitespec my-tier0-site --nodes hosts.csv > sitespec.yaml
```

The hosts come from a CSV, because a real site has more of them than a flag per
machine can carry. The header names the columns in any order; `name` and `ip`
are required, `tpm_ek_thumbprint` is optional per row:

```csv
name,ip,tpm_ek_thumbprint
cp-01,10.10.50.2,0000000000000000000000000000000000000000000000000000000000000000
cp-02,10.10.50.3,
cp-03,10.10.50.4,
```

An unknown column is an error rather than being ignored, so a misspelled header
does not silently drop a field. Pass `--nodes -` to read the CSV from standard
input.

This is a different format from the `NKECTL_CLUSTER_NODES` environment variable
below, which spells the same list as `name:ip,name:ip`. The two are not
interchangeable: `name:ip` has nowhere to put a TPM thumbprint.

Anything the CSV does not carry is set with `--set`, using the same field paths
this document uses. Run it with an unknown path to list what it accepts:

```bash
nkectl generate sitespec my-tier0-site --nodes hosts.csv \
  --set cluster.controlPlaneVIP=10.10.50.10 \
  --set cluster.podCidr=10.244.0.0/16 > sitespec.yaml
```

Two fields are deliberately not settable. `cluster.nodes` comes from `--nodes`.
`registry.apiKey` is a credential and this file is meant to be committed — pass
it to `nkectl bootstrap site` with `--registry-api-key` or `REGISTRY_API_KEY`
instead.

Unlike `bootstrap site`, `generate sitespec` ignores the environment variables
listed below, so a generated file never carries a value nobody typed. Every
field is validated before anything is written, so a file this command produces
is a file `bootstrap site` accepts.

## Where the machines come from

By default the file lists the hosts (`cluster.nodes`) and the bootstrap builds
the cluster on them. Setting `infrastructure: nico` swaps that list for
`cluster.nico`: CAPNICo allocates the same number of control-plane machines from
a NICo site, configured by the profiles the block names. Everything else in the
file means what it did. [`examples/sitespec-nico.yaml`](../examples/sitespec-nico.yaml)
is a complete example.

| Field | Type | Required | Notes |
|---|---|---|---|
| `infrastructure` | string | no | `provisioned` (default) or `nico`. `NKECTL_INFRASTRUCTURE` overrides it. |
| `cluster.nico.nodeCount` | int | with `nico` | Odd, at least 1. Takes the place of the length of `cluster.nodes`. |
| `cluster.nico.orgID` | string | with `nico` | NGC organization that owns the cluster. |
| `cluster.nico.siteID`, `vpcID` | UUID | with `nico` | The NICo site and the VPC the cluster is created in. |
| `cluster.nico.instanceTypePreference` | UUID list | with `nico` | Ordered; index 0 is the first choice. |
| `cluster.nico.vpcPrefixIDs` / `subnetIDs` | UUID list | with `nico` | Exactly one of the two: native networking or not. |
| `cluster.nico.sshKeyGroupIDs` | UUID list | with `nico` | At least one. |
| `cluster.nico.osProfile`, `configProfile` | string | with `nico` | Profile names served by the site. |
| `spire.externalAddress` | string | with `nico` | IP address or DNS name nodes use to reach the bootstrap host's SPIRE server, on NodePort 30081. See below. |

`cluster.nodes` must be empty with `nico`, and `cluster.nico` must be absent
with `provisioned`; a file that names both is rejected rather than half-obeyed.

With `nico` the nodes' names and addresses are results, not inputs, and are
printed once the nodes join.

TODO: need to implement this for nico mode. Until then, read them from the
finished cluster (`kubectl get nodes -o wide`).

### SPIRE with NICo nodes

Nodes are attested by TPM, as on a site of listed hosts, but the allowlist is
not in the file. A host's endorsement key cannot be known before the machine
exists, so there is no per-node `tpmEkThumbprint` to write. CAPNICo reports each
machine's key when NICo allocates it, and CWE adds it to the allowlist the
bootstrap SPIRE server reads (the `tpm-pubhashes` ConfigMap in `spire-server`).
A node attests once that entry has synced into the SPIRE pod, which can take up
to about a minute; its agent retries until then.

CWE writes the SPIRE server's address into each node's userdata, so it has to be
one a node can reach before any cluster exists. Set `spire.externalAddress` to
the address of the host running the bootstrap, as seen from the network the nodes
are created on. The bootstrap publishes the server there on NodePort 30081.
Setting `spire.externalAddress` with `provisioned` is rejected: those hosts were
given their SPIRE address when they were provisioned.

### NICo credentials

CAPNICo needs credentials for the NICo API. They are not in the file, which is
meant to be committed. Pass `--nico-credentials-file` (or set
`NKECTL_NICO_CREDENTIALS_FILE`) to a file of `KEY=VALUE` lines; it becomes the
`nico-credentials` Secret in `capnico-system`. The keys are CAPNICo's own — see
[its getting-started guide](https://github.com/dsx-ai-factory/cluster-api-provider-nico/blob/main/docs/getting-started.md#3-get-a-machine-usable-credential).

The bootstrap installs CAPNICo into its Kind cluster from the public Helm chart
(`helm` v4 or later, and network access to `ghcr.io`). The chart and its version
are pinned in the bootstrap manifest (`nkectl get bootstrap-manifest`).
`NKECTL_CAPNICO_CHART` and `NKECTL_CAPNICO_VERSION` override the chart reference
and version. The Kind host
must reach the NICo API and each new node's agent port, and the nodes must
reach the SPIRE server the bootstrap publishes on the host.

## Schema

| Field | Type | Required | Notes |
|---|---|---|---|
| `clusterName` | string | yes | Lowercase RFC 1123 label. |
| `cluster.nodes` | list | yes | Must contain an odd number of control-plane nodes and at least one node. |
| `cluster.nodes[].name` | string | yes, per entry | Lowercase RFC 1123 label. Unique across all nodes. |
| `cluster.nodes[].ip` | string | yes, per entry | Valid IPv4 or IPv6 address. Unique across all nodes. |
| `cluster.nodes[].tpmEkThumbprint` | string | no | SHA-256 of the node's TPM endorsement key: 64 lowercase hex digits. See below. |
| `cluster.k8sVersion` | string | yes | Plain semantic version, e.g. `1.32.0` (no leading `v`). |
| `cluster.cniName` | string | yes | `ovn-kubernetes`. `calico` is accepted for development. |
| `cluster.cniVersion` | string | no | CNI plugin version. Empty uses the node-agent default. |
| `cluster.controlPlaneVIP` | string | no | Optional IPv4 control-plane VIP. Empty uses the IP of the node that initializes the cluster. See below. |
| `cluster.podCidr` | string | yes | Valid CIDR. A comma-separated list of CIDRs is allowed. |
| `cluster.serviceCidr` | string | yes | Valid CIDR. |
| `cluster.nodeAgentPort` | string | no | Port number 1–65535. Defaults to `15005`. |
| `cluster.etcdEncryption` | bool | no | Encrypt the Kubernetes API's etcd data at rest. Defaults to `true`. Decided at bootstrap only: a cluster built with it `false` cannot be encrypted afterwards. See below. |
| `cluster.kubeletConfig.systemReservedMemory` | string | no | e.g. `16Gi`. |
| `registry.bootstrapRepo` | string | yes | OCI repository holding the bootstrap engine chart. The controller image is published separately, to `<registry>/<org>/controller`. |
| `registry.bootstrapChart` | string | yes | Bootstrap engine chart name within `bootstrapRepo`. |
| `registry.apiKey` | string | no | Registry credential. Never defaulted — see below. |
| `spire.trustDomain` | string | no | SPIFFE trust domain. Defaults to `<bootstrap-cluster>.nke.local`. Use the same value for the permanent site SPIRE server. |
| `spire.caCertFile` | string | no | Path to a CA certificate for SPIRE to chain its own CA to. See below. |
| `spire.caKeyFile` | string | no | Path to that CA's private key. Required with `caCertFile`. |

The version (image/chart tag) is baked into the `nkectl` build, so it is not
part of the site spec.

Unknown fields are rejected. A typo in a field name fails the load rather than
being silently ignored.

## Validation rules

- All problems are reported together in one error, each naming the offending
  field, so a bad file can be fixed in one pass.
- The site spec must contain an odd number of nodes and at least one node.
- Node names and IPs must be unique across all nodes.
- `podCidr` / `serviceCidr` are parsed as CIDRs; `podCidr` also accepts a
  comma-separated list and validates each entry.
- `controlPlaneVIP`, when set, must parse as a routable unicast IPv4 address,
  must not equal a node IP, and must not fall inside `podCidr` or `serviceCidr`.

## Environment variable overrides

Any field can be supplied — or overridden — through an environment variable, so
existing tier-0 / DS9 scripts can drive the site spec without editing the file.
A set variable takes precedence over the file value.

| Environment variable | Field |
|---|---|
| `CLUSTER_NAME` | `clusterName` |
| `CLUSTER_K8S_VERSION` | `cluster.k8sVersion` |
| `CLUSTER_CNI_NAME` | `cluster.cniName` |
| `CLUSTER_CNI_VERSION` | `cluster.cniVersion` |
| `CLUSTER_CONTROL_PLANE_VIP` | `cluster.controlPlaneVIP` |
| `CLUSTER_POD_CIDR` | `cluster.podCidr` |
| `CLUSTER_SERVICE_CIDR` | `cluster.serviceCidr` |
| `CLUSTER_SYSTEM_RESERVED_MEMORY` | `cluster.kubeletConfig.systemReservedMemory` |
| `CLUSTER_NODE_AGENT_PORT` | `cluster.nodeAgentPort` |
| `CLUSTER_ETCD_ENCRYPTION` | `cluster.etcdEncryption` |
| `REGISTRY_BOOTSTRAP_REPO` | `registry.bootstrapRepo` |
| `REGISTRY_BOOTSTRAP_CHART` | `registry.bootstrapChart` |
| `REGISTRY_API_KEY` | `registry.apiKey` |
| `NKECTL_SPIFFE_TRUST_DOMAIN` | `spire.trustDomain` |
| `SPIRE_CA_CERT_FILE` | `spire.caCertFile` |
| `SPIRE_CA_KEY_FILE` | `spire.caKeyFile` |
| `NKECTL_SPIRE_EXTERNAL_ADDRESS` | `spire.externalAddress` |
| `NKECTL_CLUSTER_NODES` | `cluster.nodes` — format `name:ip,name:ip`, applied only when the file lists no nodes |

A malformed `NKECTL_CLUSTER_NODES` entry fails the load rather than being
silently discarded.

`INIT_NODE_IP` and `INIT_NODE_NAME` are not supported. The site spec does not
select a node for `kubeadm init`; the bootstrap process selects it downstream.

## TPM endorsement-key thumbprints

`tpmEkThumbprint` is the SHA-256 of a node's TPM endorsement key, as printed by
[`spire-tpm-plugin`](https://github.com/spiffe/spire-tpm-plugin)'s
`get_tpm_pubhash`. It identifies the *machine* rather than anything about its
software: an EK is burned into the TPM and cannot be moved or changed.

Reading it is an out-of-band step before the bootstrap. For DSX, the ansible
runbook collects it from each host over SSH and writes it into the site spec.

Every thumbprint in the file is seeded into the allowlist of the SPIRE server
`bootstrap site` deploys, and it is the whole of that allowlist:

- a node with a thumbprint can attest and be issued an identity
- a node without one cannot
- a site spec where no node has one deploys SPIRE with no TPM attestor at all

It is a public key hash, not a secret, so it belongs in the site spec rather
than beside the credentials, and a file carrying them stays safe to commit.

A malformed thumbprint fails the load. Left to reach the allowlist it would
match nothing and surface much later as that one node failing attestation for no
visible reason.

## The SPIRE CA

`spire.trustDomain` sets the SPIFFE trust domain for the bootstrap server. It
must be a lowercase DNS name. If it is absent, `nkectl` uses
`<bootstrap-cluster>.nke.local`. A permanent site SPIRE server that replaces
the bootstrap server must use the same value.

`spire.caCertFile` and `spire.caKeyFile` name a CA the operator brings to the
bootstrap. `nkectl` configures SPIRE to chain its own CA to it, so a node that
was given that certificate ahead of time — for DSX, delivered by the ansible
runbook over SSH — verifies the server the first time it speaks to it, rather
than trusting whatever answers.

They are paths, not inline PEM. A private key does not belong in a file meant to
be committed, and the same certificate has to be delivered to each node by
whatever provisions it, so it already exists as a file.

Both or neither. Omitting them leaves SPIRE self-signed, which is fine when
nothing outside the cluster has to verify it — and means nodes cannot be given a
trust anchor in advance. Both files are checked for existence at load, while
nothing has been touched yet: otherwise a wrong path surfaces minutes later as a
helm failure naming a temporary file rather than the field that was wrong.

## etcd encryption at rest

`cluster.etcdEncryption` defaults to `true`. The first control-plane node's
kube-apiserver starts with an `EncryptionConfiguration` that encrypts these
resource kinds in etcd with a secretbox key generated for the cluster:
`secrets`, `configmaps`, `persistentvolumeclaims`, `persistentvolumes`,
`serviceaccounts`, `events`, `services`. An `identity` provider stays in the
configuration as a read fallback; it does not make writes plaintext.

The decision is final at bootstrap. Encryption is configured during
`kubeadm init`, and there is no controller on the cluster afterwards to turn it
on, so a cluster built with `etcdEncryption: false` stays unencrypted. Set it
to `false` only when that is the intent.

`nkectl bootstrap site` writes the key into the working directory, as a Secret
manifest named `<clusterName>-etcd-encryption-key.json` (`--encryption-key-out`
to choose the path; it is resolved independently of `--kubeconfig-out`). Every
control-plane node holds the key in its kube-apiserver configuration; the file
is what a controller installed later is given, and the copy a restore onto new
hosts needs. It is never overwritten, and the run refuses to start if it
already exists.

## Schedulable control plane

Every site spec node is a control-plane node, and kubeadm taints each one
`node-role.kubernetes.io/control-plane:NoSchedule`. With the taint in place
nothing schedules: a Helm `--wait` or `clusterctl init` on the new cluster hangs
until it times out, with no message saying why.

The site spec declares no worker nodes, so the control-plane nodes are the
only place workloads can run. Once every node has joined, `nkectl bootstrap
site` removes that taint from each node. Other taints are left alone, and a
node that already lacks the taint is not touched. There is no field for this:
the taint follows the topology, and the rule will become "only when the site
spec declares no worker nodes" once it can declare them.

If the removal fails, the cluster is still up and the kubeconfig is on disk.
The error names the command that finishes the step by hand:

```bash
kubectl taint nodes --all node-role.kubernetes.io/control-plane:NoSchedule- \
  --kubeconfig <clusterName>.kubeconfig
```

## The control-plane VIP

`cluster.controlPlaneVIP` is an optional IPv4 address that fronts the Kubernetes
API for every control-plane node. When set, CWE runs kube-vip on each node,
kubeadm uses the VIP as the `controlPlaneEndpoint`, and the kubeconfig `bootstrap
site` writes points at it. Empty uses the IP of the node that initializes the
cluster. CWE selects a control-plane node that reports ready to join, so the
endpoint does not necessarily use the first node listed in the site spec.

The VIP must be routable to the control-plane nodes over BGP. kube-vip
advertises it with local AS 65100 to each node's default gateway, peer AS
4294967000 by default, so the network has to peer with the nodes and carry the
route. Without BGP routing to the initializing node, the other control-plane
nodes cannot reach the VIP to join.

The address must be a routable unicast IPv4 address: the unspecified, loopback,
multicast, link-local and broadcast forms are refused, because the other
control-plane nodes have to dial it to join. It must also sit outside `podCidr`
and `serviceCidr` and must not be a node IP, or it collides with the cluster's
own networks or a node. Loading the site spec rejects a VIP that breaks any of
these rules.

## Relationship to the tier-0 bootstrap template

The field names mirror the tier-0 bootstrap template
so operators do not need to relearn them. The subset of settings our main user
(DSX) configures today is rendered by their Ansible role using the DSX tier-0 bootstrap template.

The differences from tier-0 are deliberate and make the file open-sourceable:

- **Generic registry fields.** tier-0's NVIDIA-specific `registry.ngcApiKey` /
  `registry.sweImageTag` become the registry-agnostic `registry.apiKey`,
  `registry.bootstrapRepo`, and `registry.bootstrapChart`. The image/chart tag
  is baked into the `nkectl` build rather than set in the file.
- **No deploy-time fields.** `argocd`, `repos`, `skipDeploy`, Forge/baremetal,
  and node-injection blocks are out of scope for the M0 bootstrap and are not
  part of this schema.

## Credential handling

`registry.apiKey` is resolved from, in order of precedence: the
`--registry-api-key` flag, the `REGISTRY_API_KEY` environment variable, then the
file. Keeping the secret out of the file lets the site spec be committed and
shared safely. The credential is never logged or printed.
