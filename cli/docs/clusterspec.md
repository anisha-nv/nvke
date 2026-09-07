# Tenant Cluster Create Spec

`nkectl generate clusterspec` writes a YAML `CreateClusterRequest` that can be
passed unchanged to `nkectl create cluster -f`.

```sh
nkectl generate clusterspec demo --provider capd > cluster.yaml
nkectl create cluster -f cluster.yaml
```

The command validates every supplied value before it writes YAML. A failed
generation therefore cannot replace a redirected spec with partial output.

## Providers

### CAPD

`--provider capd` creates a local Docker-backed CAPI request. It supplies the
`infra.cluster` DockerCluster manifest and the DockerMachineTemplate manifests
for a control-plane pool and, unless `--worker-count=0`, a worker pool.

The default machine image is `cwe-capd-kindest-node:local`, the image used by
the local Tilt environment. Override it only when the local environment was
built with another image:

```sh
nkectl generate clusterspec demo --provider capd \
  --set capd.machineImage=my-capd-node:local > cluster.yaml
```

### NICo

`--provider nico` creates the API's `nico` provider payload at cluster and
node-pool level. It requires values that identify resources already available
at the target NICo site:

Use `--provider nico --help` to see the current, provider-specific field list
and descriptions. They are read from the versioned OpenAPI document that ships
with `nkectl`; no controller connection is needed to generate a spec.

Use a VPC prefix on sites with native networking; otherwise use a subnet. The
two flags are mutually exclusive. Every UUID and profile name is site-specific;
the generator does not invent defaults for them.

```sh
nkectl generate clusterspec demo --provider nico \
  --set nico.orgID=my-org \
  --set nico.siteID=11111111-1111-4111-8111-111111111111 \
  --set nico.vpcID=22222222-2222-4222-8222-222222222222 \
  --set nico.vpcPrefixID=33333333-3333-4333-8333-333333333333 \
  --set nico.instanceTypeID=44444444-4444-4444-8444-444444444444 \
  --set nico.sshKeyGroupID=55555555-5555-4555-8555-555555555555 \
  --set nico.osProfile=nke-os \
  --set nodePools.controlPlane.nico.configProfile=default-cp \
  --set nodePools.worker.nico.configProfile=default-worker > cluster.yaml
```

The identifiers above are examples only. They are not valid site configuration.

When standard input is a terminal, the command prompts for any omitted NICo
field. Prompts go to standard error and YAML remains on standard output, so this
is safe:

```sh
nkectl generate clusterspec demo --provider nico > cluster.yaml
```

For scripts, pass every required NICo value with `--set`. A non-terminal command
with missing fields fails rather than waiting for input. Pass
`--non-interactive` to enforce this behavior even from a terminal.

## Template fields

Both providers support Kubernetes/CNI settings, etcd encryption, optional
control-plane VIP, system memory reservation, and desired control-plane and
worker counts with the same `--set path=value` syntax. The paths express the
generator template: `nodePools.controlPlane` and `nodePools.worker` expand to
the corresponding API node-pool objects.

The terminal prompts accept free-form input. A later version can obtain live
provider choices from `nkectl discover` and expose an interactive selection
flow without changing the generated request format.
