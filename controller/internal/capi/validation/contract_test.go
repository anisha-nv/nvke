// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestLaunchLayerPinnedContract(t *testing.T) {
	digests := map[string]string{
		"launchlayerclusters":         "a45fc67d5b8163499be90bab0cfdf2f0957662d41b51183076e489f7e217ac76",
		"launchlayermachines":         "07c273e6fa943bfaca755204f5c107d183f320a642fb3273d6d134663b7bd0bd",
		"launchlayermachinetemplates": "761154203f7fd9d9c9bf10efc86e0be60595dae1d6e6357e7e423278a8d0b5b6",
	}
	inventory := map[string]any{}
	for plural, digest := range digests {
		data, err := os.ReadFile("testdata/capll-v1alpha1/infrastructure.cluster.x-k8s.io_" + plural + ".yaml")
		require.NoError(t, err)
		require.Equal(t, digest, fmt.Sprintf("%x", sha256.Sum256(data)), "refresh provenance and review contract changes")
		var crd map[string]any
		require.NoError(t, yaml.Unmarshal(data, &crd))
		spec := crd["spec"].(map[string]any)
		kind := spec["names"].(map[string]any)["kind"].(string)
		versions := spec["versions"].([]any)
		require.Len(t, versions, 1)
		version := versions[0].(map[string]any)
		require.Equal(t, "v1alpha1", version["name"])
		schema := version["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)
		delete(schema["properties"].(map[string]any), "status")
		classifyRules(t, schema)
		inventory[kind] = withoutDescriptions(schema)
	}
	expected, err := os.ReadFile("testdata/capll-v1alpha1/create-schema-inventory.json")
	require.NoError(t, err)
	actual, err := json.Marshal(inventory)
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(actual),
		"new fields, required/type/enum/length/default/CEL constraints require a validator review")
}

// No general CEL evaluator: creation rules are enumerated by exact text and
// reviewed individually. Every other reviewed rule requires oldSelf and
// belongs to Kubernetes updates.
//
// Reviewed at revision 0f5ac57f1aaa0f0cadf4043525223bc907c1fff8:
//   - isCIDR(self), and the protocol/ports consistency rule, both on
//     spec.securityGroups[].rules.{ingress,egress}, the security group rule
//     definitions. CWE's securityGroups intake only accepts group name
//     references (primaryNetworkInterface/gpuNetworkInterface string arrays,
//     see securityGroups() below); it does not let an operator submit a group
//     rule, so this constraint is unreachable through CWE today.
//   - the securityGroups/securityGroupRefs mutual-exclusivity rule.
//     securityGroupRefs is new at this revision. CWE does not expose it
//     anywhere in its typed API, so this constraint is unreachable through
//     CWE today.
func classifyRules(t *testing.T, value any) {
	t.Helper()
	switch v := value.(type) {
	case map[string]any:
		if rules, ok := v["x-kubernetes-validations"].([]any); ok {
			for _, raw := range rules {
				rule := raw.(map[string]any)["rule"].(string)
				require.True(t, strings.Contains(rule, "oldSelf") ||
					rule == "has(self.vpcID) != has(self.orgID)" ||
					rule == "isCIDR(self)" ||
					rule == "(self.protocol == 'tcp' || self.protocol == 'udp') ? (has(self.ports) && size(self.ports) > 0) : (!has(self.ports) || size(self.ports) == 0)" ||
					rule == "!has(self.securityGroups) || !has(self.securityGroupRefs) || (((!has(self.securityGroups.primaryNetworkInterface) || size(self.securityGroups.primaryNetworkInterface) == 0) || (!has(self.securityGroupRefs.primaryNetworkInterface) || size(self.securityGroupRefs.primaryNetworkInterface) == 0)) && ((!has(self.securityGroups.gpuNetworkInterface) || size(self.securityGroups.gpuNetworkInterface) == 0) || (!has(self.securityGroupRefs.gpuNetworkInterface) || size(self.securityGroupRefs.gpuNetworkInterface) == 0)))",
					"unclassified creation rule: %s", rule)
			}
		}
		for _, child := range v {
			classifyRules(t, child)
		}
	case []any:
		for _, child := range v {
			classifyRules(t, child)
		}
	}
}

func withoutDescriptions(value any) any {
	switch v := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, child := range v {
			if key != "description" {
				result[key] = withoutDescriptions(child)
			}
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, child := range v {
			result[i] = withoutDescriptions(child)
		}
		return result
	default:
		return value
	}
}
