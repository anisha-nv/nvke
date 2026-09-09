# LaunchLayer-backed NKE tenant-cluster request validation

Use the existing `infra` request mode for LaunchLayer-backed NKE tenant clusters.
There is no new `launchlayer` API object or provider registration endpoint.
The NKE Cloud Workflow Engine (CWE) validates the complete initial request
before opening its database transaction, then persists desired state and outbox
records using the existing lifecycle workflow.

## Contract and deployment prerequisites

The supported pair is
`infrastructure.cluster.x-k8s.io/v1alpha1`,
`LaunchLayerCluster` and `LaunchLayerMachineTemplate`.
The implementation is pinned to
[CAPLL revision 80a0d3da5d4af8ebd25cf3d8f897c4779248f50b](https://github.com/NVIDIA/cluster-api-provider-launchlayer/tree/80a0d3da5d4af8ebd25cf3d8f897c4779248f50b).
The checked-in CRD fixtures and schema inventory are under
[controller/internal/capi/validation/testdata](../controller/internal/capi/validation/testdata/capll-v1alpha1/README.md).

An operator must separately install compatible CAPLL CRDs/images and manager
endpoint/authentication configuration in the relevant NKE CAPI management
cluster, authorize the infra request mode through the release's admission
configuration, and provide tested host/VM profiles.
The revision above is a source contract, not a selected released chart/image or
evidence of deployment. A v1alpha1 name alone does not establish compatibility:
vmProfile was added without changing that version name.
Do not advertise new fields until the installed CRDs and controller support them.

## Cluster and template fields

| Location | Contract |
| --- | --- |
| Cluster spec.vpcID / spec.orgID | Exactly one present, nonblank string; no UUID restriction. vpcID selects an existing VPC; orgID requests a CAPLL-managed VPC. |
| Cluster spec.owner | Required object with nonblank userID and username, each at most 253 Unicode characters. No extra owner.orgID. |
| Cluster spec.siteID | Optional string, including empty; not verified routing or scheduling information. |
| Cluster spec.cloudProviderEnabled | Optional boolean, preserved without defaulting; true requires a separately functioning CCM. |
| MachineTemplate spec.template.spec | Required object chain. No embedded metadata, kind, apiVersion or status override. |
| Machine spec.instanceType | Required nonblank string; not a NICo UUID or an instance-type preference list. |
| Machine spec.hostOSProfile / spec.vmProfile | Optional strings, independently preserved. name[:version] is a convention, not an enforced regex. Instance-type spelling does not establish substrate. |
| Machine spec.fulfillmentStrategy | Optional BEST_EFFORT, ALL_OR_NOTHING or WAIT_FOR_RESOURCE. Omission is preserved for CAPLL to default to BEST_EFFORT. |
| Machine spec.securityGroups | Optional object with primaryNetworkInterface and gpuNetworkInterface string arrays. No invented UUID, count or uniqueness limits. |
| Top-level metadata | Optional name, namespace, labels and annotations only, with Kubernetes syntax/types/size checks. |
| Lifecycle and other fields | Unknown fields, explicit nulls for nonnullable fields, root status, server-owned metadata and Machine providerID are rejected. |

The owner requirement is intentionally stricter than the CRD, which makes
owner optional: the reviewed Compute client requires both identity strings.
Owner presence does not verify the caller's authorization to use that identity.
The HTTP validator does not execute update-only CEL rules involving oldSelf.

All initial node pools are checked, including zero-sized/disabled pools.
Control-plane node-pool mode and controller-based control-plane mode both reuse
shared naming, Kubernetes version, CNI/network/count/VIP, SPIRE attestation and
reserved-bootstrap-file checks. NICo subnet/VPC-prefix or OS-profile rules are
not imposed on LaunchLayer inputs.

## Checked request example

[examples/launchlayer-cluster.json](examples/launchlayer-cluster.json) is a
complete request body for the default control-plane node-pool mode, with one
control-plane node and a worker pool. Its identifiers and profiles are examples,
not allocated resources; select a supported Kubernetes version for the deployment.

For a managed VPC, replace spec.vpcID with spec.orgID, never supply both.
For controller-based control-plane mode, remove the cp node pool and put its
infra object in controlPlaneConfig.infra with desiredCount: 1 and maxSurge: 1.
Enable the corresponding control-plane-controller feature flag.
The API tests build and persist both network choices in both modes.

The example includes vmProfile to illustrate the field contract, not to promise
VM provisioning. At the pinned revision, CAPLL still requires backend VPC type
BARE_METAL. Substrate/profile compatibility must be qualified separately.

For example, removing instanceType from the second node pool produces HTTP 400:

```json
{
  "message": "nodePools[1].infra.machineTemplate.spec.template.spec.instanceType: required nonblank string"
}
```

No transaction, desired-state write or outbox entry is made for that invalid
create. CAPLL errors use a declared field path and a bounded reason, not raw
manifest contents or unknown field names. Shared OpenAPI and generic validators
retain their existing error conventions.

## Later writes and compatibility

Standalone node-pool create compares the template with the stored cluster
binding. Provider-bearing node-pool and control-plane PATCH requests validate
the effective merged intent before writing desired state.
An omitted template remains unchanged; an explicitly empty required template
is rejected. Every stored control-plane template preference is checked on a
provider-bearing update, even though the public API supplies one template.

Malformed stored LaunchLayer bindings return a repair-oriented error for new
provider intent. Root identity is not freely editable through the cluster API:
coordinate an explicit administrative repair or recreate under the approved
lifecycle process; do not replace it by inferring identity from a new template.
Reads, deletion and unrelated scalar maintenance do not trigger the new
provider validation. Existing provisioning workflows are not retroactively
re-admitted or rewritten.

Dispatch recognizes LaunchLayer on either side of the cluster/template pair,
including wrong-case/wrong-role LaunchLayer kinds, and then requires the exact
pair. Unrelated generic CAPI inputs retain the existing generic path.
This is not a closed concrete-module catalog, installation discovery, or proof
that any arbitrary generic provider is supported by a product release.

Upgrade compatibility: formerly opaque LaunchLayer inputs with missing owner,
unknown fields, null values, lifecycle metadata, providerID, embedded template
overrides or malformed object chains now fail immediately.
Coordinate this tightening with the compatible CAPLL artifact and operator
release notes. There is no background rewrite or validation-disable escape hatch.

## What HTTP acceptance does not prove

HTTP 201 accepts desired state; it does not mean a VPC, instance or Kubernetes
node is ready. Backend endpoint/authentication, VPC existence/status/org/type,
owner authorization, security-group membership, quota, capacity, profile
existence and substrate compatibility remain asynchronous CAPLL/LaunchLayer
checks.

CWE stores template metadata, but CAPI GenerateTemplate does not automatically
copy top-level MachineTemplate labels/annotations into a Machine.
CWE assigns the generated resources' names/namespaces. Cluster labels and
annotations are applied with the infrastructure Cluster.
The template's approved machine spec is retained in each generated Machine.

CWE renders a bootstrap Secret referenced by the CAPI Machine.
CAPLL intentionally does not forward the raw cloud-init UserData to Compute:
its manager-wide ConfigMap maps selected Secret values to user_data_args, and
the selected backend profile provides cloud-init.
Accepting bootstrapFiles or preKubeadmCommands does not prove those customizations
reach LaunchLayer nodes. Matching SPIRE paths, attestation, images, profiles and
placeholder mappings require separate deployment acceptance.
No bootstrap transport redesign or new backend preflight is included here.

## Local verification

Tests use the vendored dependencies, synthetic authentication keys, recording
database transactions and in-memory Kubernetes apply recorders.
The rendering test executes the production scaffold and GenerateTemplate path;
it is not Kubernetes CRD admission or an end-to-end provisioning test.

Always set KUBECONFIG to a verified nonexistent file for local unit tests unless
live-environment access has been explicitly authorized:

```sh
test ! -e /tmp/nke-no-live-kubeconfig
KUBECONFIG=/tmp/nke-no-live-kubeconfig GOPROXY=off make -C controller test
```

On macOS, use GNU Make (gmake). Use the Go version pinned by go.work.
Live integration/E2E runs, release request-mode gating, exact deployed artifact
pins and bootstrap/profile acceptance are separate release gates.
