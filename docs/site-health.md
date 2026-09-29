# Tenant API reachability

`nkectl get clusters` lists the clusters known to the connected site controller
and shows CWE's last observed access to each tenant Kubernetes API:

```console
$ nkectl --site lab get clusters
NAME    KUBERNETES VERSION   CNI              STATUS        ID                                     AGE   API REACHABLE
alpha   1.35.0               ovn-kubernetes   Provisioned   11111111-1111-4111-8111-111111111111   2h    True
beta    1.35.0               ovn-kubernetes   Provisioned   22222222-2222-4222-8222-222222222222   1h    False
```

The existing `STATUS` column describes lifecycle state. A provisioned cluster can
have an unreachable API. Reachability does not change bootstrap completion or
describe node readiness, workloads, storage, or shared dependencies.

| API REACHABLE | Meaning |
|---|---|
| `True` | CWE successfully requested the tenant API's `/version` endpoint using its published access configuration. |
| `False` | CWE attempted access and encountered a connection, timeout, TLS, credential, authorization, or other request failure. |
| `Unknown` | No observation exists yet, or usable tenant access is unavailable. This also covers an older API response without the field. |

`False` describes access **from CWE**. A network route, certificate, or credential
problem can produce it even when the API process is running and another client
can connect. A legacy cluster without published access, including one without
the matching Cluster API scaffolding, remains `Unknown` until access is available.

## Reading the observation

The REST list/get responses expose optional `conditions.apiServerReachable`.
`nkectl --site lab get clusters -o json` and `-o yaml` preserve its status,
safe diagnostic message, and `lastTransitionTime`. The field is absent before
the first completed observation; the CLI does not invent a condition for it.

The observer normally checks each cluster every two minutes after its previous
completed check, with up to four checks in progress. Credential and HTTP attempts
have individual time limits. Queueing, retries, credential refresh, or management
outages can delay checks; this cadence is not a freshness guarantee.

**Observations do not expire.** If the observer stops, a previously stored `True`
or `False` remains visible. `lastTransitionTime` records a status transition, not
the latest successful poll. Identical observations do not write the database;
a changed message with the same status preserves the transition time. Management
read failures and shutdown preserve the previous observation.

## Site scope and authentication

A site in this command means the connected CWE endpoint and its database. The
local `lab` name is a connection alias; it is not a new server-side site identity
or a filter on the NICo provider's site ID. Selecting one site does not query or
aggregate other CWE endpoints.

Configure [API authentication](../controller/docs/configuring-api-authentication.md)
in enforcing mode. Callers without valid, allowed credentials are denied by both
the API and CLI. This feature uses the existing authentication policy and adds no
role model. See [authenticating to the API](../controller/docs/authenticating-to-the-api.md)
for configuring CLI credentials.

## Checking observer operation

The observer runs on the elected management-manager leader. With management
cluster access, inspect its lease and locate the pod named by the holder identity:

```sh
kubectl --context MANAGEMENT -n cloud-workflow-engine get lease controller.nke.nvidia.com -o yaml
kubectl --context MANAGEMENT -n cloud-workflow-engine get pods -o wide
kubectl --context MANAGEMENT -n cloud-workflow-engine logs POD -c cloud-workflow-engine --since=10m
```

Look for controller `cluster-api-reachability`. Changed observations produce
`Tenant API reachability changed` at info level. With CWE `LOG_LEVEL=debug`,
`Tenant API probe completed` also records unchanged checks, cluster ID, status,
and duration. These events are diagnostic logs, not stored observation times.
If completion logs are absent, inspect the elected process, its logging level,
and management/database errors before drawing conclusions about a tenant.

CWE's `/readyz` checks database and Temporal connectivity; it does not establish
probe progress. The management manager's metrics listener is disabled, so it is
not a source of reachability freshness either.

## Controller rollout

Before enabling the observer, deploy the locking fix and the version that
understands the optional database field to **every process that writes cluster
rows**. Then deploy the observer/API version. During a rolling API deployment,
older serving replicas can omit the field; wait for all replicas to finish before
comparing API and CLI results. Roll back only to a field-aware writer version if
stored observations must be preserved.

## Running the demo

From a checkout, with `nkectl`, `jq`, Mike Farah `yq` v4 and Python 3 installed:

```sh
controller/hack/site-health/demo.sh --site lab --token-file /path/to/cwe-token
```

The token file contains a valid, allowed CWE bearer token; keep it outside the
checkout. The configured site URL must be directly reachable by the demo,
for example through a local port-forward. The demo compares raw API results
with CLI table/JSON/YAML output and checks missing/invalid credential denial
using an isolated CLI config that cannot mint replacement credentials. It
prints `SKIP` for outage/recovery in this observation mode.

Full fault mode requires Docker access, an explicit management context, two
reachable disposable CAPD tenants, and an explicit target ID/name and sentinel
ID. Mark the target CAPI Cluster with
`site-health.nke.nvidia.com/disposable=true` only in your disposable fixture.
The script validates this marker, CAPD infrastructure, and container ownership
before staging every target API manifest:

```sh
controller/hack/site-health/demo.sh --site lab --token-file /path/to/cwe-token \
  --inject-outage --target TARGET_UUID --sentinel SENTINEL_UUID \
  --disposable-target TARGET_NAME --management-context MANAGEMENT
```

The target must become False while the sentinel stays True and both remain
listed, then return to True after restoration. Each settle check has a five-minute
test budget. EXIT/INT/TERM cleanup restores staged manifests through Docker and
verifies recovery before reporting success. A failed assertion or recovery exits
nonzero. On a disposable fixture, `SITE_HEALTH_DEMO_FAIL_AFTER_STAGING=true`
exercises restoration after a forced assertion failure; its expected exit is
nonzero with a successful cleanup line. SIGKILL/host loss cannot run a trap; the
printed target endpoints and `/etc/kubernetes/site-health-demo-kube-apiserver.yaml`
backup identify the manual recovery path.
