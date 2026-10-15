# Tenant API contract

> **Under development.** This pre-release API is not for production use.
> Backward compatibility is not currently enforced, including within `v1`.

`cluster/v1` defines the public `Cluster` representation of the internal
[`NVKECluster`](../../apis/v1/nvkecluster_types.go). The generated Go package is
`clusterv1`; the gRPC service is `cluster.v1.ClusterService`. The
[application service](../../internal/public/tenant/apis/cluster/service.go)
implements the contract using generated bindings and spec mappings. See
[development](../../DEVELOPMENT.md#tenant-api-runtime) to run the standalone process.

Hand off the generated [OpenAPI 2.0 document](gen/cluster/v1/openapi.yaml) to API
consumers. This standalone YAML includes routes, nested schemas, required fields,
constraints, and lifecycle descriptions; its references resolve within the file.
The gateway generator retains some path/query limits only in descriptions;
those limits and cross-field rules remain part of the contract.
The generated [field reference](gen/cluster/v1/FIELDS.md) also lists spec exposure.

## Contract and ownership

[`cluster.proto`](apis/cluster/v1/cluster.proto) defines create, get, list, and
replace operations, request/resource envelopes, pagination, and public status
enums. Public field names are snake_case. Create and replace use separate spec
types; their requests cannot set status or arbitrary Kubernetes metadata.
`resource_version` carries an opaque datastore concurrency token; it is not the
public API version.

The [exposure policy](apis/cluster/v1/exposure.json) selects internal spec paths,
protobuf tags, and `create`/`read`/`replace` access. Unselected fields stay private.
Its `input` options can narrow writable list bounds and provide a public
description; read projections retain the internal schema's bounds.
These operations describe field representation, not caller authorization.
The contract defines no org/project resource or caller-selected storage namespace.

| Location | Ownership |
| --- | --- |
| `apis/cluster/v1/` | Maintained `cluster.proto`, exposure policy, HTTP/OpenAPI configuration, compatibility lock, and contract tests |
| `apis/cluster/v1/spec.proto` | Generated intermediate protobuf schema imported by `cluster.proto` |
| `gen/cluster/v1/` | Generated protobuf/gRPC/gateway Go, route metadata, OpenAPI, and field reference |
| [`mapping`](../../internal/public/tenant/apis/cluster/mapping) | Generated private spec mappings, maintained helpers, and projection tests |

The compiler in [`hack/public-api`](../../hack/public-api) derives spec types,
requiredness, descriptions, and validation constraints from the
[internal CRD](../../config/crd/bases/internal.nvke.nvidia.com_nvkeclusters.yaml).
[`gateway.yaml`](apis/cluster/v1/gateway.yaml) defines HTTP bindings;
[`openapi-options.yaml`](apis/cluster/v1/openapi-options.yaml) configures OpenAPI
output. Generated HTTP route metadata is checked against the compiled protobuf
descriptors.

Generated files have ownership headers and must be regenerated rather than
edited. For spec schema review, read [`spec.proto`](apis/cluster/v1/spec.proto).
The escaped `rawDesc` constants in generated Go encode that schema for the
protobuf runtime.

Future operator contracts can use `public/operator` and
`internal/public/operator`; extract shared definitions or helpers when both
audiences need them.

## Runtime boundary

The [server](../../internal/public/tenant/server/server.go) sends HTTP calls through
a generated gRPC client to a separate loopback listener on the same gRPC server.
Native clients have an independent connection allowance. Both transports share
handler admission and interceptors. HTTP body processing has a separate budget,
while connection, stream, message-size, and deadline limits bound RPC input.
HTTP parsing guards reject unknown fields,
camelCase aliases, duplicate keys, nulls, and conflicting path/body names before
dispatch. The [architecture decision](../../docs/adrs/20261014-stateless-tenant-api.md)
explains the service and datastore boundaries.

The runtime requires explicit `--evaluation-no-auth` and serves one configured
namespace without caller authentication or authorization. Future caller scope
must come from verified identity and an authorized storage mapping. Unverified
claims and caller-supplied headers must not select storage. Requests have size and
time limits. Datastore calls have deadlines and bounded pagination. List cursors
are encrypted and bound to the configured datastore and namespace. Only the tenant
Cluster service is registered.

Create persists the selected spec fields and private creation values below.
Replace supplies the last `resource_version` and the complete writable spec,
preserving create-only and private fields. Omitting the optional release-channel
version removes it. Replacement is allowed only before a provisioning checkpoint
exists and while the resource is not deleting. A merge patch carries the version
precondition so concurrent spec, status, or metadata writes cause a conflict.
Read the current resource before retrying HTTP 409 / gRPC `ABORTED`.

The service checks supported network semantics before storage admission. It accepts
one canonical IPv4 CIDR each for pods and services, and the ranges must not overlap.
Datastore admission remains authoritative for the internal CRD and its CEL rules.

GET and LIST project stored data without waiting for reconciliation. Missing or
stale observations remain PENDING, unavailable readiness is UNKNOWN, and only an
explicit provisioning observation produces `REASON_PROVISIONING`. Ordinary READY
and DELETING states omit a reason. Private diagnostic messages never become public
status. `Cluster.uid` distinguishes a later resource created with the same name.

## Generate and test

Change the internal Go types, maintained proto, or configuration and regenerate.
For internal type changes, first [regenerate the CRD](../../DEVELOPMENT.md#build-and-tests).
From `management-engine`:

```sh
make generate-public-api
make test-public-api
```

`test-public-contract` runs `check-public-api` before testing the descriptors,
compiler, and HTTP bindings. `test-public-api` includes those checks, unit tests,
and envtest persistence, RBAC, and controller checkpoint races. A persistence case
exercises both transports and controller-written status using direct reconciliation
without CWE. Deployment and watch delivery are separate checks. `make test` includes
these suites. `check-public-api` can also be run alone to verify drift.

Generation installs pinned local tools, checks Buf formatting/lint, and generates
clients, gateway handlers, and OpenAPI. Buf uses the repository-wide pin; protobuf
and gateway plugins follow their runtime versions in `go.mod`. Annotation schemas
come from that same pinned gateway module in the temporary generation tree. The
drift check compares a clean temporary output tree and detects stale artifacts.

## Compatibility

CI runs `check-public-api` and the contract tests through `make test`. The check
rejects unacknowledged changes to the [contract lock](apis/cluster/v1/wire-lock.json)
and stale generated output. After reviewing a contract change, update the lock
and regenerate:

```sh
make public-api-tools
bash hack/public-api/generate.sh --update-lock
make test-public-contract test-unit
```

`--update-lock` permits intentional breaking changes during development. Policy,
schema, and protobuf validity checks still apply. Review the source, lock,
generated output, and affected tests together.

Before publishing a supported contract, add CI compatibility checks against an
independently protected released baseline, covering protobuf/JSON, exposure,
defaults, replacement behavior, and admission rules. These historical checks are
not implemented yet.

## Mapping boundary

The compiler supports objects, strings, and atomic string lists. Parents must be
selected before children, and children cannot gain operations their parent lacks.
Required fields beneath populated objects need create exposure or an explicit
private default. Unpopulated optional private objects are not inspected, and CRD
defaults are not interpreted.

Writable objects require a required child exposed for the same operation, and
writable lists must be required. These restrictions preserve presence despite
protobuf's inability to distinguish an absent repeated field from an empty one.
Optional read-only lists are supported. Unsupported selected schema shapes fail
generation; internal schema growth never exposes fields automatically.

Mappings validate scalar constraints and list cardinality, project selected spec
fields, and retain private/create-only fields during replacement. They do not
enforce authentication, authorization, networking semantics, lifecycle, or
optimistic concurrency. CEL rules are recorded for compatibility review but are
not evaluated by mappings; the datastore remains authoritative for admission.

### Private create values

The [exposure policy](apis/cluster/v1/exposure.json) supplies required internal
configuration that tenants cannot set at this time. The compiler rejects defaults on public
create fields, so these assignments never override tenant input.

| Stored field | Value | Rationale |
| --- | --- | --- |
| `apiServerAccess.publicAccessEnabled` | `false` | Public API-server access configuration is unsupported by the management-engine provisioning integration at this time. |
| `clusterNetwork.serviceDomain` | `cluster.local` | Matches the DNS domain configured by CWE's kubeadm renderer. Custom service domains are unsupported at this time. |

The [provisioning validation](../../controllers/nvkecluster_resolve.go) rejects
public access and domains other than `cluster.local`. CWE selects that domain in
its [defaults](../../../controller/internal/kubeadmconfig/render.go) and applies it
to [kubeadm networking](../../../controller/internal/kubeadmconfig/init.go).
An empty service domain is invalid under the [CRD](../../config/crd/bases/internal.nvke.nvidia.com_nvkeclusters.yaml).

These are service-selected spec values, not observations of a provisioned cluster.
They are excluded from tenant reads and writes. Replacement preserves stored values.
