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
  --capd-machine-image my-capd-node:local > cluster.yaml
```

### NICo

`--provider nico` creates the API's `nico` provider payload at cluster and
node-pool level. It requires values that identify resources already available
at the target NICo site:

```text
--org-id
--site-id
--vpc-id
--vpc-prefix-id | --subnet-id
--instance-type-id
--ssh-key-group-id
--os-profile
--control-plane-config-profile
--worker-config-profile
```

Use a VPC prefix on sites with native networking; otherwise use a subnet. The
two flags are mutually exclusive. Every UUID and profile name is site-specific;
the generator does not invent defaults for them.

```sh
nkectl generate clusterspec demo --provider nico \
  --org-id my-org \
  --site-id 11111111-1111-4111-8111-111111111111 \
  --vpc-id 22222222-2222-4222-8222-222222222222 \
  --vpc-prefix-id 33333333-3333-4333-8333-333333333333 \
  --instance-type-id 44444444-4444-4444-8444-444444444444 \
  --ssh-key-group-id 55555555-5555-4555-8555-555555555555 \
  --os-profile nke-os \
  --control-plane-config-profile default-cp \
  --worker-config-profile default-worker > cluster.yaml
```

The identifiers above are examples only. They are not valid site configuration.

When standard input is a terminal, the command prompts for any omitted NICo
field. Prompts go to standard error and YAML remains on standard output, so this
is safe:

```sh
nkectl generate clusterspec demo --provider nico > cluster.yaml
```

For scripts, pass every required NICo value as a flag. A non-terminal command
with missing fields fails rather than waiting for input. Pass
`--non-interactive` to enforce this behavior even from a terminal.

## Common options

Both providers support Kubernetes/CNI settings, etcd encryption, optional
control-plane VIP, system memory reservation, and the desired control-plane and
worker counts. Run `nkectl generate clusterspec --help` for the full current
flag reference.

The terminal prompts accept free-form input. A later version can obtain live
provider choices from `nkectl discover` and expose an interactive selection
flow without changing the generated request format.
