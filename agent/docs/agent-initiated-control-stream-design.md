# Agent-initiated control stream

> Status: draft for review

## Problem

CWE currently opens a new gRPC connection to a node's IP address whenever a
controller or Temporal activity calls `nke-agent`. This requires every managed
node to accept inbound TCP connections from the site controller cluster on port
`15005`. It also ties node-agent reachability to routable node addresses and
site firewall rules even though nodes can initiate outbound connections to the
site controller.

The connection direction must be reversed without moving orchestration into the
agent. CWE must remain the component that chooses the node operation, supplies
the request, applies its deadline and interprets the result. The node agent must
continue to execute only commands that arrive through the authenticated control
channel.

## Solution summary

`nke-agent` opens one long-lived bidirectional gRPC stream to a CWE agent
gateway. CWE retains its generated `NodeAgentClient` call pattern through a
reverse-channel adapter that implements `grpc.ClientConnInterface`. Calls made
by controllers and Temporal activities are multiplexed onto the authenticated
agent stream, dispatched to the existing `NodeAgentServer` implementation, and
correlated with responses by call ID.

CWE is the gRPC server for the outer `Connect` stream because the node opens the
network connection. CWE remains the logical NodeAgent client because it starts
each node operation after the stream is established.

The reverse channel uses TLS for server authentication and encryption. The node
agent authenticates with a short-lived JWT-SVID obtained from its local SPIRE
Workload API. JWT-SVID authentication is layered on TLS and does not replace
TLS. Where CWE and node agents share federated X.509 trust, the same stream may
use SPIFFE mTLS instead of the JWT authentication profile.

## Architecture sequences

### Before: CWE opens each connection

```mermaid
sequenceDiagram
    autonumber
    participant Controller as CWE controller or Temporal activity
    participant Client as CWE NodeAgentClient
    participant CWESPIRE as CWE SPIRE Workload API
    participant Agent as nke-agent gRPC server
    participant NodeSPIRE as Node SPIRE Workload API
    participant Handler as Node operation handler

    Controller->>Client: Call a NodeAgent RPC for node UUID
    Client->>CWESPIRE: Fetch CWE X.509 SVID and trust bundle
    Agent->>NodeSPIRE: Fetch node X.509 SVID and trust bundle
    Client->>Agent: Open TCP to node IP port 15005
    Client->>Agent: Establish SPIFFE mTLS
    Client->>Agent: Send unary or streaming RPC
    Agent->>Handler: Execute the requested node operation
    Handler-->>Agent: Return result or gRPC status
    Agent-->>Client: Return RPC response
    Client-->>Controller: Return typed result
    Client->>Agent: Close connection
```

The current transport is implemented in
`controller/internal/agent/client.go`. The node listener and SPIFFE mTLS server
are implemented in `agent/cmd/agent/main.go`.

### After: the agent opens a persistent reverse channel

```mermaid
sequenceDiagram
    autonumber
    participant IMDS as DPU IMDS
    participant SPIREServer as SPIRE Server
    participant NodeSPIRE as Host SPIRE agent
    participant Agent as nke-agent reverse client
    participant Gateway as CWE AgentGateway server
    participant Sessions as CWE session registry
    participant Controller as CWE controller or Temporal activity
    participant Reverse as Reverse NodeAgentClient
    participant Handler as Existing NodeAgentServer

    NodeSPIRE->>IMDS: Obtain the host identity document
    NodeSPIRE->>SPIREServer: Attest the host with dsx_imds
    SPIREServer-->>NodeSPIRE: Return the attested SPIRE agent identity
    Agent->>NodeSPIRE: Fetch JWT-SVID for the gateway audience
    Agent->>Gateway: Open TLS and call Connect with JWT-SVID
    Gateway->>Gateway: Verify issuer, signature, audience, expiry, and subject
    Gateway->>Gateway: Authorize subject for the matching eligible node
    Gateway->>Sessions: Allocate and register session generation
    Gateway-->>Agent: Accept session with assigned generation

    loop While the session is active
        Agent->>Gateway: Send heartbeat and agent capabilities
        Gateway->>Sessions: Renew session liveness
    end

    Controller->>Reverse: Call a typed NodeAgent RPC for node UUID
    Reverse->>Sessions: Resolve the active authenticated session
    Reverse->>Gateway: Open reverse call with method, call ID, and deadline
    Gateway-->>Agent: Send call frames on Connect
    Agent->>Handler: Dispatch through the NodeAgent service descriptor
    Handler-->>Agent: Return typed result or gRPC status
    Agent-->>Gateway: Send correlated response frames
    Gateway-->>Reverse: Complete the generated client call
    Reverse-->>Controller: Return typed result
```

The IMDS interaction belongs to SPIRE node attestation. `nke-agent` does not
call IMDS. It asks the already-attested local SPIRE agent for its workload
JWT-SVID.

## Connection roles

The two roles are intentionally different:

- At the transport boundary, `nke-agent` is the gRPC client and CWE
  `AgentGateway` is the gRPC server for `Connect`.
- At the node-operation boundary, CWE is the `NodeAgent` client. It selects and
  starts every RPC. The agent never chooses an operation from desired state.

The outer stream does not change ownership of cross-node orchestration,
workflow retries, topology decisions, or cluster state. Those remain in CWE.

## Reverse gRPC channel

Add a transport service alongside the existing `NodeAgent` service:

```protobuf
service AgentGateway {
  rpc Connect(stream AgentFrame) returns (stream ControllerFrame);
}
```

The conceptual frame types are:

```protobuf
message ControllerFrame {
  oneof frame {
    OpenCall open_call = 1;
    CallMessage call_message = 2;
    HalfClose half_close = 3;
    CancelCall cancel_call = 4;
    SessionControl session_control = 5;
  }
}

message AgentFrame {
  oneof frame {
    AgentHello hello = 1;
    Heartbeat heartbeat = 2;
    CallHeaders call_headers = 3;
    CallMessage call_message = 4;
    CloseCall close_call = 5;
  }
}
```

Every call frame carries a session generation and call ID. Message frames also
carry a per-call sequence number. `OpenCall` carries the full generated gRPC
method name, allowlisted request metadata, and the caller's deadline. The
allowlist contains NKE-owned tracing and request-correlation keys only. It never
forwards `authorization`, cookies, or the JWT-SVID used to establish the outer
stream. `CloseCall` carries the final gRPC status and trailers.

On the CWE side, `ReverseClientConn` implements the two methods required by
`grpc.ClientConnInterface`:

```go
type ClientConnInterface interface {
    Invoke(ctx context.Context, method string, args, reply any, opts ...CallOption) error
    NewStream(ctx context.Context, desc *StreamDesc, method string, opts ...CallOption) (ClientStream, error)
}
```

The generated `NewNodeAgentClient` therefore remains the client presented to
rollout operations and new controller code. Existing higher-level methods in
`controller/internal/agent.Client` also continue to return their current typed
responses.

The adapter explicitly supports the call options used by generated and existing
NKE clients, including the static-method marker, response headers and trailers,
message-size limits, and wait-for-ready behavior. It rejects an unsupported
option before sending a frame instead of silently changing its meaning. Peer,
authority override, and per-RPC credential options are not forwarded because
the authenticated outer session already fixes those transport properties.
For wait-for-ready, the registry wakes the call when a matching session arrives
or the caller's context ends. A fail-fast call with no session returns
`Unavailable` immediately.

On the node side, the dispatcher accepts only methods present in the generated
`NodeAgent_ServiceDesc`. It invokes the corresponding handler on the existing
`NodeAgentServer`, preserving the protobuf request and response types and gRPC
status codes. It does not expose a generic method that can invoke arbitrary Go
functions or host commands.

Unary, client-streaming, and server-streaming calls use the same channel.
Per-call sequence numbers and bounded queues isolate each call's ordering and
backpressure. One receive loop demultiplexes frames, and one priority-aware send
loop services bounded control and per-call queues. Heartbeats, cancellation, and
session-control frames take priority over call data, and every frame has a
configured maximum size. A peer that stops reading can still block the whole
HTTP/2 stream, so the session liveness deadline closes that stream and cancels
its calls. Handler execution is bounded independently so a long operation does
not block heartbeats, cancellation, or status calls. Existing
operation-specific mutexes continue to protect node state.

## Identity and TLS

### Host attestation and workload identity

DSX IMDS establishes the host SPIRE agent identity before `nke-agent` starts the
control stream:

1. The DPU `dpu_hw` identity makes the host identity available through IMDS.
2. The host SPIRE agent's `dsx_imds` NodeAttestor obtains that identity and
   attests to SPIRE Server.
3. A workload registration entry beneath the attested host agent selects
   `nke-agent` using its Unix selectors.
4. `nke-agent` uses the Workload API `FetchJWTSVID` operation on the local
   socket and requests a JWT-SVID for the configured CWE gateway audience.

Successful host attestation alone is not sufficient. The workload entry, parent
agent ID, Unix selectors, Workload API socket permissions, audience, and issued
claims must all be verified. The workload entry must exist before the first
connection attempt so registration does not depend on the connection it is
intended to authorize.

The JWT-SVID subject must identify one CWE database node. Preserve the existing
node identity shape when CWE owns the workload registration:

```text
spiffe://<trust-domain>/host/<node-uuid>
```

The gateway derives the node UUID from the verified subject and loads that node
from PostgreSQL. `AgentHello.node_id` must equal the authenticated UUID, but the
unverified hello value never selects the identity or authorization policy.

### JWT-SVID purpose

The JWT-SVID is the node agent's client credential for establishing `Connect`.
It has three bounded uses in the new architecture:

1. **Authentication:** CWE verifies the token and obtains a cryptographically
   authenticated SPIFFE subject for the connecting workload.
2. **Authorization:** CWE maps the verified `/host/<node-uuid>` subject to the
   same node UUID in PostgreSQL and admits a session only when that node exists,
   is eligible, and is configured for reverse connectivity.
3. **Session lifetime:** the token's expiry bounds the lifetime of the accepted
   stream. Reconnection requires a newly fetched JWT-SVID.

The token is checked when the stream is established, not attached to each
NodeAgent call sent later on that stream. It does not act as a TLS certificate,
grant access to unrelated CWE APIs, or authorize the commands CWE sends to the
node. The node authenticates the CWE gateway through its TLS server identity,
and the reverse dispatcher separately limits commands to the admitted
`NodeAgent` method set.

### TLS profile with JWT-SVID client authentication

The agent connects to a stable CWE gateway DNS name using TLS 1.2 or newer and
validates the server certificate against an approved CA bundle. It sends the
JWT-SVID as bearer metadata only when creating `Connect`. CWE validates:

- the exact configured issuer and signing algorithm
- the signature against the issuer's rotating JWKS
- the exact gateway audience
- `exp` and `nbf` with bounded clock skew
- a SPIFFE subject in an admitted trust domain
- the exact `/host/<node-uuid>` path and matching database node
- that the node is not deleting and is configured for reverse connectivity

The JWT is a bearer credential. It must remain in memory, must not be persisted
or logged, and must not appear in connection errors. CWE closes the stream
before token expiry. The node then fetches a new JWT-SVID and reconnects. This
ensures a connection cannot outlive its authenticated credential indefinitely.

JWT-SVID validation can reuse the non-HTTP core of CWE's existing authentication
verifier, including issuer, audience, expiry, algorithm, JWKS refresh, and
SPIFFE-path admission checks. The gRPC interceptor adds the exact node-to-subject
binding.

### SPIFFE mTLS profile

When the node and CWE trust domains are the same or federated for X.509, the
outer stream uses SPIFFE mTLS. The agent authorizes the exact CWE workload ID,
and CWE authorizes the exact node workload ID. This profile does not also send a
JWT-SVID because mTLS already authenticates both peers with proof of possession.

Plaintext is limited to local development and tests. Neither production profile
falls back to plaintext when SPIRE, JWKS, or TLS setup fails.

## Session lifecycle

`nke-agent` maintains one connection loop:

1. Wait for networking and the local SPIRE Workload API.
2. Obtain the configured workload credential.
3. Dial the CWE gateway with exponential backoff and jitter.
4. Send `AgentHello` with node UUID, agent version, supported full method names,
   and a connection nonce.
5. Receive the CWE-assigned session generation, then process calls and send
   heartbeats until the stream ends.
6. Cancel all calls tied to the closed session and reconnect with a new
   credential and connection nonce. CWE assigns a new session generation.

CWE accepts one active generation for each node UUID. A newly authenticated
generation replaces and closes the previous generation. Every reverse call is
fenced by the generation it resolved so a response from an old stream cannot
complete work sent on a newer stream.

Heartbeats carry no credential or secret material. Missing the configured
liveness window removes the session from routing and enqueues the node for an
immediate readiness update.

## CWE replica routing

An agent stream terminates on one CWE pod, while a Temporal activity may execute
on another pod. Session ownership therefore cannot be an in-memory map alone.

Each CWE pod exposes an internal dispatch service. On connection, it writes a
session ownership row containing its pod identity and address, node UUID, and
session generation. Each pod renews one pod lease rather than writing every
node heartbeat to PostgreSQL. Node heartbeats update the owner's in-memory
liveness state. Disconnect and timeout remove the ownership row only when its
generation still matches, so an old stream cannot delete its replacement.

Before sending a call, `ReverseClientConn` reads the current owner and requires
both its session row and pod lease to be live:

- Calls owned by the local pod use its in-memory session directly.
- Calls owned by another pod use the internal dispatch service at that pod's
  registered address and include the expected session generation. The owner
  validates that generation against its in-memory stream before dispatch.
- Calls with no live owner return `Unavailable` and do not silently dial port
  `15005` unless the node is explicitly in migration mode.

The internal dispatch hop uses SPIFFE mTLS between CWE workloads. It carries
request data only in memory. Request bodies are not written to PostgreSQL
because NodeAgent requests can contain kubeconfigs, encryption keys, uploaded
files, and other sensitive data.

If the owner pod exits, its lease expires, the agent reconnects through the CWE
Service, and the new owner publishes a new generation. Calls sent to the failed
owner return `Unavailable` and follow the operation's existing retry contract.

## Call completion and retry behavior

The reverse channel does not claim exactly-once execution. A connection can be
lost after the agent starts a mutation but before CWE receives its result, which
is also possible with the current direct unary transport.

- CWE assigns a call ID and preserves the caller's context deadline.
- Context cancellation sends `CancelCall` and cancels the node-side handler
  context.
- The agent returns the original gRPC status code, message, headers, trailers,
  and typed response.
- Async operations continue to use their operation ID and durable node-agent
  status as the retry and recovery boundary.
- Idempotent operations may be retried with the same operation-specific key.
- An operation that is neither idempotent nor durably keyed must not be
  automatically replayed after an unknown delivery outcome.

Every RPC must be classified as read-only, idempotent, operation-keyed, or
non-replayable before it is enabled on the reverse channel. This classification
is enforced by an allowlist in the node dispatcher and covered by tests.

## Capability discovery

The current rollout path probes gRPC reflection on port `15005`. Reflection on
the CWE gateway describes `AgentGateway`, not the connected agent's
`NodeAgent` implementation.

`AgentHello` therefore advertises the generated full method names implemented
by the running agent. CWE records the capabilities with the active session and
uses them for `MethodSupportChecker`. Capability information expires with the
session and is never inferred only from semantic version.

## Readiness

`NodeAgentReady` remains behavioral rather than a TCP-only condition:

1. CWE requires a currently authenticated, non-expired session.
2. The readiness controller calls `GetKubeadmStatus` through the reverse
   `NodeAgentClient` with its existing probe deadline.
3. A successful round trip records `NodeAgentReady=True` and the returned agent
   version.
4. Authentication failure, disconnect, heartbeat expiry, or failed probe records
   `NodeAgentReady=False` with a sanitized reason.

The existing periodic resync remains as recovery for a missed connection event.
A connect or disconnect event also enqueues the node immediately.

## Configuration and deployment

Add agent settings for:

```text
--connection-mode=listen|reverse|dual
--cwe-agent-gateway-address=<dns-name:port>
--cwe-agent-gateway-server-name=<tls-server-name>
--cwe-agent-gateway-ca-bundle=<path>
--cwe-agent-gateway-auth=jwt-svid|spiffe-mtls
--cwe-agent-gateway-jwt-audience=<audience>
--cwe-agent-gateway-spiffe-id=<exact-cwe-id>
```

Only settings for the selected authentication profile are required. The SPIFFE
Workload API continues to use `--spiffe-socket-path`.

The CWE chart adds an agent-gateway container port, Service port, TLS settings,
JWT issuer/JWKS configuration, and an internal dispatch port. Site networking
must permit managed nodes to reach the gateway address. It no longer needs to
permit CWE to initiate connections to every node once migration completes.

Node bootstrap data supplies the gateway address, TLS trust bundle, node UUID,
authentication profile, and audience or expected CWE SPIFFE ID. These values are
configuration and public trust material, not credentials. JWT-SVIDs are always
obtained at runtime through the local Workload API.

## Migration

1. Deploy CWE with `AgentGateway`, reverse session routing, internal dispatch,
   and the existing direct client still enabled.
2. Release `nke-agent` with `dual` mode. It retains the current port `15005`
   listener while establishing the outbound stream.
3. Configure selected nodes for reverse connectivity and verify authenticated
   sessions, capabilities, readiness probes, unary calls, streaming calls, and
   reconnect behavior.
4. Mark those nodes reverse-preferred. Direct dialing remains available only
   because the node is explicitly in migration mode, not as fallback after an
   authentication or authorization failure.
5. Roll out reverse-preferred mode to the fleet and observe it across CWE and
   node-agent restarts.
6. Change nodes to `reverse` mode, remove inbound port `15005` from site network
   policy, and later remove the direct client after the supported mixed-fleet
   window ends.

## Observability and audit

CWE records bounded metrics for:

- active sessions by authentication profile
- successful and rejected connections by bounded reason
- reconnects, heartbeat expiry, and replaced generations
- active reverse calls, latency, cancellations, and status-code class
- calls routed locally or through another CWE replica
- calls rejected by the method replay classification

The node-agent audit log retains the RPC full method, authenticated CWE identity,
call ID, session generation, duration, and final status. It does not log JWTs,
protobuf bodies, kubeconfigs, encryption keys, file contents, or unbounded
caller-controlled strings.

## Implementation areas

- `agent/pkg/agent/proto/node_agent.proto`: transport service and frame contract
- `agent/cmd/agent`: reverse connection lifecycle, TLS and JWT-SVID acquisition
- `agent/internal/grpcserver`: NodeAgent descriptor dispatcher and stream adapter
- `controller/internal/agent`: session-backed `grpc.ClientConnInterface`
- `controller/internal/auth`: reusable verifier wiring for the gRPC interceptor
- `controller/internal/controllers/nodeagentreadiness`: connection events and
  reverse behavioral probes
- `controller/internal/rollout/adapters/cwe`: shared session-backed dialer and
  capability checker
- `controller/internal/db`: session owner and lease metadata
- `controller/deploy/charts/cloud-workflow-engine`: external gateway and internal
  dispatch ports, TLS, JWT issuer, and JWKS configuration
- node provisioning and deployment-owned OS profiles: gateway bootstrap settings
  and the DSX IMDS workload registration

Generated protobuf files change only after the proto source is updated and
`make protos` is run.

## Validation

Unit and integration coverage must prove:

- JWT issuer, signature, audience, expiry, subject, and node binding failures are
  rejected before a session is registered
- SPIFFE mTLS accepts only the exact configured peer identities
- a controller uses the generated `NodeAgentClient` over the reverse connection
  for unary, client-streaming, and server-streaming methods
- concurrent calls are correlated correctly and one blocked call does not block
  heartbeats or other calls
- deadlines, cancellation, gRPC status codes, headers, and trailers survive the
  channel
- a new generation replaces the old stream and stale responses are fenced
- heartbeat expiry changes `NodeAgentReady` without waiting for periodic resync
- an agent reconnects after CWE restart, node restart, credential rotation, and
  transient network loss
- a Temporal activity running on a non-owner CWE replica reaches the owner over
  the internal dispatch service
- mixed direct, dual, and reverse nodes preserve the explicitly configured
  migration behavior
- no JWT, kubeconfig, encryption key, uploaded content, or protobuf request body
  is persisted or logged by the transport

Backend qualification must run an actual node lifecycle operation through the
reverse channel and verify the requested result on the node. Establishing the
stream or passing a health probe alone is not behavioral qualification.

## Non-goals

- Moving cluster orchestration or desired-state reconciliation into
  `nke-agent`.
- Letting the agent select or synthesize node operations.
- Using JWT-SVIDs as TLS certificates.
- Persisting NodeAgent request or response bodies in PostgreSQL.
- Removing the direct listener before the mixed-fleet migration completes.
