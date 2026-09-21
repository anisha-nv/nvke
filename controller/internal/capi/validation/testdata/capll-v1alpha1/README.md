# CAPLL create-contract fixtures

These three generated CRDs are copied unchanged from
[dsx-ai-factory/cluster-api-provider-launchlayer at 0f5ac57f1aaa0f0cadf4043525223bc907c1fff8](https://github.com/dsx-ai-factory/cluster-api-provider-launchlayer/tree/0f5ac57f1aaa0f0cadf4043525223bc907c1fff8/config/crd/bases).
Upstream is Copyright NVIDIA CORPORATION & AFFILIATES, Apache-2.0; see
[the upstream license](https://github.com/dsx-ai-factory/cluster-api-provider-launchlayer/blob/0f5ac57f1aaa0f0cadf4043525223bc907c1fff8/LICENSE).
They are test data, not manifests to install automatically.

SHA-256 checksums (also enforced by contract_test.go):

- Cluster: a45fc67d5b8163499be90bab0cfdf2f0957662d41b51183076e489f7e217ac76
- Machine: 07c273e6fa943bfaca755204f5c107d183f320a642fb3273d6d134663b7bd0bd
- MachineTemplate: 761154203f7fd9d9c9bf10efc86e0be60595dae1d6e6357e7e423278a8d0b5b6

create-schema-inventory.json records all schema keywords except descriptions
and the root status subtree. Tests compare it against all three CRDs, including
field names, required lists, types, enums, lengths and CEL.
The creation CEL rules are: presence XOR for vpcID/orgID; a CIDR-format check
and a protocol/ports consistency check inside spec.securityGroups[].rules,
new at this revision; and a securityGroups/securityGroupRefs mutual-exclusivity
check on Machine and MachineTemplate, also new at this revision. All four are
listed by exact text in contract_test.go's classifyRules.
All rules referencing oldSelf are transition-only and are not executed on a
new request; their exact text remains pinned for review.
Root status is forbidden by the HTTP contract and remains covered by whole-file hashes.

securityGroups (on LaunchLayerCluster, new at this revision -- inline security
group rule definitions) and securityGroupRefs (on LaunchLayerMachine and
LaunchLayerMachineTemplate, new at this revision -- reference existing groups
by name) are both optional and unreachable through CWE's typed API today: CWE's
own securityGroups intake only accepts group name references, matching the
pre-existing shape. Their CEL rules are pinned and reviewed above, but nothing
in this repo currently exercises them.

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
