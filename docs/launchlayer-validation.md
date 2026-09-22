# Typed LaunchLayer support for NKE tenant clusters

The NKE Cloud Workflow Engine (CWE) accepts three independent API modes:
`nico`, `launchlayer`, and `infra`. Use `launchlayer` for first-class LaunchLayer
intent and validation. Keep `infra` for generic CAPI envelopes, including raw CAPLL
manifests; their kind does not select typed validation. This is API admission,
not provider registration, installation discovery, or backend health verification.

## Deployment and compatibility

`SUPPORTED_INFRA_PROVIDERS` (chart value `supportedInfraProviders`) is a
comma-separated allowlist. The production/chart default remains `nico`.
For example, `nico,launchlayer` permits typed NICo and LaunchLayer requests but
not raw `infra` requests. `infra` alone permits raw CAPLL manifests without
enabling typed LaunchLayer requests. Unknown, empty, and duplicate mode names
fail startup validation.

`GET /api/infraProviders` reports configured API modes. It is distinct from
`nkectl get providers`, which inspects installed Kubernetes provider modules.
Existing objects remain readable and scalar maintenance remains available when
a mode is disabled; disabling admission does not stop reconciliation or queued
work. New clusters, later pools, and provider-bearing PATCH requests require
their mode to be enabled.

The source contract is pinned to
[CAPLL revision 0f5ac57f1aaa0f0cadf4043525223bc907c1fff8](https://github.com/dsx-ai-factory/cluster-api-provider-launchlayer/tree/0f5ac57f1aaa0f0cadf4043525223bc907c1fff8).
[CRD fixtures and inventory](../controller/internal/capi/validation/testdata/capll-v1alpha1/README.md)
record the reviewed schema. CWE authors
`infrastructure.cluster.x-k8s.io/v1alpha1` LaunchLayerCluster and
LaunchLayerMachineTemplate resources. Matching CRDs, CAPLL controller images,
manager credentials/endpoints, profiles, and bootstrap configuration must be
installed and qualified separately in the relevant NKE CAPI management cluster.
A source revision or API version string is not evidence of deployment.

Apply the additive `clusters.api_provider` migration first, upgrade **all**
CWE readers and writers, and only then enable `launchlayer`.
The immutable API mode is separate from the existing `provider_type=capi`
execution path. Legacy NULL rows retain their historical NICo or generic CAPI
representation, even when their raw manifests use CAPLL kinds. No automatic
backfill, promotion, or manifest rewrite occurs.

Mixed-version writers and rollback after typed rows exist are not automatically
safe. The down migration refuses to remove the discriminator. Preserve the
column and use roll-forward or a separately approved offline conversion;
do not silently reclassify typed rows as generic input.

## Typed contract

Every LaunchLayer-specific object is closed: unknown keys and explicit null
values fail. Shared bootstrap file entries retain the existing CloudInitFile
schema and validation rules.
Do not submit raw GVKs, metadata, status, providerID, credentials, or nested
Kubernetes resource envelopes inside `launchlayer`.

| Location | Contract |
| --- | --- |
| launchlayer.vpcID / orgID | Exactly one nonblank string; no NICo UUID restriction. Existing VPC versus CAPLL-managed VPC. |
| launchlayer.owner | Required userID and username, both nonblank and at most 253 Unicode characters. No owner.orgID. |
| launchlayer.siteID | Optional string, including empty. Passed through; not validated routing or scheduling information. |
| launchlayer.cloudProviderEnabled | Optional boolean, preserved without defaulting. True requires a functioning CCM. |
| Pool/CP launchlayer.machine.instanceType | Required nonblank string; not a NICo instance-type UUID or preference list. |
| machine.hostOSProfile / vmProfile | Independently optional strings. name[:version] is a convention, not an enforced regex or substrate selector. |
| machine.fulfillmentStrategy | Optional BEST_EFFORT, ALL_OR_NOTHING, or WAIT_FOR_RESOURCE. Omission leaves CAPLL to default. |
| machine.securityGroups | Optional primaryNetworkInterface and gpuNetworkInterface string arrays; ordering and explicit empty arrays are preserved. |
| Pool/CP launchlayer.bootstrapFiles / preKubeadmCommands | Optional existing cloud-init inputs. Reserved SPIRE paths remain controller-managed. |

Owner presence is intentionally stricter than the CRD's optional owner because
the reviewed Compute client requires both identity strings. Presence does not
authorize impersonation of that identity. Profile spelling does not prove
compatibility. In particular, vmProfile support does not promise VM provisioning:
the pinned CAPLL implementation still requires backend VPC type BARE_METAL.

All initial pools are validated, including zero-sized and disabled pools, before
the create transaction. Both control-plane node-pool mode and controller-based
control-plane mode preserve the shared naming, Kubernetes version, CNI/network,
count/VIP, SPIRE, and bootstrap-path checks. NICo-only subnet, VPC-prefix,
OS-profile, and UUID rules are not applied to LaunchLayer intent.

## Create and read examples

[Typed JSON request](examples/launchlayer-cluster.json) and
[nkectl YAML example](../cli/examples/launchlayer-cluster.yaml) describe an NKE
tenant cluster with a control-plane pool and worker pool. Identifiers and
profiles are illustrative, not allocated resources.

For a managed VPC, replace root `launchlayer.vpcID` with `orgID`, never both.
For controller-based control-plane mode, remove the control-plane pool and put
its typed provider into `controlPlaneConfig.launchlayer`, alongside
`desiredCount: 1` and optionally `maxSurge: 1`. Enable the existing
control-plane-controller feature flag.

Reads retain the cluster's immutable API mode. Typed cluster, pool, and node-set
responses contain `launchlayer`, never an additional `infra` block.
Control-plane responses expose all stored `machinePreferences`, for example:

```json
{
  "launchlayer": {
    "machinePreferences": [
      {"instanceType": "example-control-plane-type", "hostOSProfile": "example-host:1"}
    ]
  }
}
```

This is the provider fragment of a control-plane response, not a create body.
The runtime currently uses the **first** preference; this API does not implement
fallback scheduling. Pool/node-set reads obtain the immutable mode with parent
joins, not one extra query per result. Configuration-history responses project
typed field paths and machine specs without rewriting historical snapshots.
The pre-existing Temporal audit record remains limited to the fields it stores.

## PATCH semantics and errors

A later pool must use the same API block as its parent cluster. Neither raw
`infra` input on a typed cluster nor typed input on a generic cluster can switch
that binding. Root identity, owner, VPC/org, site settings, and the API mode are
not patchable through pool or control-plane endpoints.

Omit `launchlayer` to leave provider intent unchanged. Inside a typed PATCH,
omit `machine` to preserve its complete stored spec. A supplied `machine` is
a **full replacement**, requires instanceType, and removes omitted optional
profiles and security groups. The following provider fragment intentionally
clears those optional machine fields:

```json
{
  "launchlayer": {
    "machine": {"instanceType": "replacement-type"},
    "bootstrapFiles": [],
    "preKubeadmCommands": []
  }
}
```

Bootstrap and command lists preserve when omitted and clear with `[]`.
Attestation preserves when omitted; empty, null, or unknown values fail.
The same rules apply beneath `controlPlaneConfig.launchlayer`.
A supplied control-plane machine replaces the preference list with one template;
omitting it preserves every stored preference. All preserved preferences are
checked on typed provider-bearing updates.

CWE validates a merged copy before storing revisions or enqueueing any operation,
including a combined etcd-encryption/control-plane PATCH. Rejected creates never
open the write transaction. Client errors return HTTP 400 with bounded public
field paths, not raw values or unknown field names. For example:

```json
{"message":"nodePools[1].launchlayer.machine.instanceType: required nonblank string"}
```

Malformed or unrepresentable typed storage produces a repair-oriented internal
error, never a partial typed response or fallback to generic `infra`.
No live backend checks are performed while reading existing objects.

## Generic escape hatch

[Equivalent raw example](examples/generic-launchlayer-cluster.json) uses
`infra.cluster` and `infra.machineTemplate` with `infra` enabled.
Generic envelope, SPIRE, reserved-file, and shared cluster checks remain;
CAPLL-specific fields are neither pruned nor typed-validated.
Consequently raw CAPLL input that first-class admission rejects can still pass
generic admission. This is intentional, not certification that a future module
is supported by a product release. Existing NICo, CAPD, and pre-provisioned flows
keep their existing API and execution paths.

## What HTTP acceptance does not prove

HTTP 201 accepts desired state, not a ready VPC, instance, or Kubernetes node.
Backend authentication, endpoint reachability, VPC existence/status/org/type,
owner authorization, security-group membership, quota, capacity, and profile
compatibility remain asynchronous CAPLL/LaunchLayer checks.

CWE writes bootstrap Secrets referenced by CAPI Machines. The pinned CAPLL
controller does not forward raw cloud-init UserData to Compute: manager-wide
ConfigMap mappings select Secret values for user_data_args, and backend profiles
provide cloud-init. Accepting bootstrapFiles or preKubeadmCommands does not prove
they reach nodes. Image/profile/SPIRE paths and placeholder mappings require
separate deployment acceptance. No bootstrap transport redesign is included.

## Local verification

Tests cover the real HTTP/OpenAPI stack, synthetic authentication, recorded
transaction writes, isolated SQLite migration/query behavior, typed snapshots,
CLI fake-HTTP round trips, and production CAPI/bootstrap rendering with fake
Kubernetes clients. PostgreSQL-specific deployment behavior and live provisioning
are separate checks, not established by these tests.

Always use a verified nonexistent kubeconfig for local verification unless live
access is explicitly authorized:

```sh
test ! -e /tmp/nke-no-live-kubeconfig && \
KUBECONFIG=/tmp/nke-no-live-kubeconfig GOPROXY=off make -C controller test
```

Use GNU Make (`gmake` on macOS) and the Go version pinned in go.work.
