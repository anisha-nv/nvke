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
		"launchlayerclusters":         "bc53e04991f2a19af435807cd7933fa07bdb6df52e7f70f8107b22603417e4d3",
		"launchlayermachines":         "c907abbb9aaf9034eae1775e97624111c3bb7667a1ae7029d088088faeb388e6",
		"launchlayermachinetemplates": "d03d240e1cc677d0f9ae5e9416afad1d295c583bc83dc67e80ea354aad2e8ec0",
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

// No general CEL evaluator: the sole creation rule is the VPC presence XOR.
// Every other reviewed rule requires oldSelf and belongs to Kubernetes updates.
func classifyRules(t *testing.T, value any) {
	t.Helper()
	switch v := value.(type) {
	case map[string]any:
		if rules, ok := v["x-kubernetes-validations"].([]any); ok {
			for _, raw := range rules {
				rule := raw.(map[string]any)["rule"].(string)
				require.True(t, strings.Contains(rule, "oldSelf") || rule == "has(self.vpcID) != has(self.orgID)",
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
