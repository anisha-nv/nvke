# CAPLL create-contract fixtures

These three generated CRDs are copied unchanged from
[NVIDIA/cluster-api-provider-launchlayer at 80a0d3da5d4af8ebd25cf3d8f897c4779248f50b](https://github.com/NVIDIA/cluster-api-provider-launchlayer/tree/80a0d3da5d4af8ebd25cf3d8f897c4779248f50b/config/crd/bases).
Upstream is Copyright NVIDIA CORPORATION & AFFILIATES, Apache-2.0; see
[the upstream license](https://github.com/NVIDIA/cluster-api-provider-launchlayer/blob/80a0d3da5d4af8ebd25cf3d8f897c4779248f50b/LICENSE).
They are test data, not manifests to install automatically.

SHA-256 checksums (also enforced by contract_test.go):

- Cluster: bc53e04991f2a19af435807cd7933fa07bdb6df52e7f70f8107b22603417e4d3
- Machine: c907abbb9aaf9034eae1775e97624111c3bb7667a1ae7029d088088faeb388e6
- MachineTemplate: d03d240e1cc677d0f9ae5e9416afad1d295c583bc83dc67e80ea354aad2e8ec0

create-schema-inventory.json records all schema keywords except descriptions
and the root status subtree. Tests compare it against all three CRDs, including
field names, required lists, types, enums, lengths and CEL.
The only creation CEL rule is presence XOR for vpcID/orgID.
All rules referencing oldSelf are transition-only and are not executed on a
new request; their exact text remains pinned for review.
Root status is forbidden by the HTTP contract and remains covered by whole-file hashes.

NKE CWE intentionally requires both nonblank owner strings even though the CRD
makes owner optional: the Compute client requires them at runtime.
Nonblank identifiers, required template object chains, the metadata allowlist,
unknown-field rejection, and rejection of allocated providerID/status are
additional HTTP create policies, not claims about upstream CRD optionality.
The pure tests exercise those tightenings and optional fields including vmProfile.

To refresh: review the upstream API, CRDs and Compute client together; replace
the three fixtures, update the revision/hashes, inspect the inventory diff,
classify every added constraint, and update validator/tests/docs.
Do not just regenerate the inventory to silence a failure.
A schema pin is not proof that matching CRDs/images are deployed.
