// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func clusterFixture() map[string]any {
	return map[string]any{
		"apiVersion": LaunchLayerAPIVersion, "kind": "LaunchLayerCluster",
		"spec": map[string]any{"vpcID": "example-vpc", "owner": map[string]any{
			"userID": "example-user", "username": "example-operator",
		}},
	}
}

func templateFixture() map[string]any {
	return map[string]any{
		"apiVersion": LaunchLayerAPIVersion, "kind": "LaunchLayerMachineTemplate",
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"instanceType": "example-type"}}},
	}
}

func setField(obj map[string]any, path string, value any, remove bool) {
	keys := strings.Split(path, ".")
	for _, key := range keys[:len(keys)-1] {
		obj = obj[key].(map[string]any)
	}
	if remove {
		delete(obj, keys[len(keys)-1])
	} else {
		obj[keys[len(keys)-1]] = value
	}
}

func TestLaunchLayerInvalidFields(t *testing.T) {
	tests := []struct {
		name, field     string
		value           any
		remove, machine bool
	}{
		{"missing cluster spec", "spec", nil, true, false},
		{"null cluster spec", "spec", nil, false, false},
		{"array cluster spec", "spec", []any{}, false, false},
		{"scalar cluster spec", "spec", "secret-value", false, false},
		{"missing VPC", "spec.vpcID", nil, true, false},
		{"null VPC", "spec.vpcID", nil, false, false},
		{"empty VPC", "spec.vpcID", "", false, false},
		{"blank VPC", "spec.vpcID", " \t", false, false},
		{"number VPC", "spec.vpcID", 42, false, false},
		{"both VPC modes", "spec.orgID", "example-org", false, false},
		{"empty second mode still present", "spec.orgID", "", false, false},
		{"missing owner", "spec.owner", nil, true, false},
		{"null owner", "spec.owner", nil, false, false},
		{"owner scalar", "spec.owner", true, false, false},
		{"missing user", "spec.owner.userID", nil, true, false},
		{"blank user", "spec.owner.userID", "\t", false, false},
		{"missing username", "spec.owner.username", nil, true, false},
		{"null username", "spec.owner.username", nil, false, false},
		{"long unicode username", "spec.owner.username", strings.Repeat("\u754c", 254), false, false},
		{"long user", "spec.owner.userID", strings.Repeat("x", 254), false, false},
		{"owner org not a field", "spec.owner.orgID", "secret-value", false, false},
		{"site type", "spec.siteID", false, false, false},
		{"null site", "spec.siteID", nil, false, false},
		{"cloud flag type", "spec.cloudProviderEnabled", "false", false, false},
		{"null cloud flag", "spec.cloudProviderEnabled", nil, false, false},
		{"unknown cluster field", "spec.endpointRef", "secret-value", false, false},
		{"status", "status", map[string]any{}, false, false},
		{"missing template spec", "spec", nil, true, true},
		{"null template spec", "spec", nil, false, true},
		{"missing template", "spec.template", nil, true, true},
		{"null template", "spec.template", nil, false, true},
		{"array template", "spec.template", []any{}, false, true},
		{"missing machine spec", "spec.template.spec", nil, true, true},
		{"null machine spec", "spec.template.spec", nil, false, true},
		{"missing instance type", "spec.template.spec.instanceType", nil, true, true},
		{"null instance type", "spec.template.spec.instanceType", nil, false, true},
		{"blank instance type", "spec.template.spec.instanceType", " ", false, true},
		{"numeric instance type", "spec.template.spec.instanceType", 10, false, true},
		{"null host profile", "spec.template.spec.hostOSProfile", nil, false, true},
		{"wrong VM profile", "spec.template.spec.vmProfile", []any{}, false, true},
		{"empty strategy", "spec.template.spec.fulfillmentStrategy", "", false, true},
		{"null strategy", "spec.template.spec.fulfillmentStrategy", nil, false, true},
		{"object strategy", "spec.template.spec.fulfillmentStrategy", map[string]any{}, false, true},
		{"unknown strategy", "spec.template.spec.fulfillmentStrategy", "secret-value", false, true},
		{"null groups", "spec.template.spec.securityGroups", nil, false, true},
		{"wrong groups", "spec.template.spec.securityGroups", []any{}, false, true},
		{"invalid group list", "spec.template.spec.securityGroups", map[string]any{"primaryNetworkInterface": "secret-value"}, false, true},
		{"invalid group item", "spec.template.spec.securityGroups", map[string]any{"gpuNetworkInterface": []any{false}}, false, true},
		{"unknown group interface", "spec.template.spec.securityGroups", map[string]any{"other": []any{}}, false, true},
		{"allocated identity", "spec.template.spec.providerID", "secret-value", false, true},
		{"template metadata injection", "spec.template.metadata", map[string]any{}, false, true},
		{"template kind injection", "spec.template.kind", "Pod", false, true},
		{"template version injection", "spec.template.apiVersion", "v1", false, true},
		{"template status injection", "spec.template.status", map[string]any{}, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj, check := clusterFixture(), LaunchLayerCluster
			if tc.machine {
				obj, check = templateFixture(), LaunchLayerMachineTemplate
			}
			setField(obj, tc.field, tc.value, tc.remove)
			before, err := json.Marshal(obj)
			require.NoError(t, err)
			err = check(obj, "input")
			require.Error(t, err)
			require.True(t, strings.HasPrefix(err.Error(), "input"))
			require.NotContains(t, err.Error(), "secret-value")
			require.Less(t, len(err.Error()), 250)
			after, marshalErr := json.Marshal(obj)
			require.NoError(t, marshalErr)
			require.Equal(t, before, after, "validation must not mutate input")
		})
	}
}

func TestLaunchLayerGVKAndMetadata(t *testing.T) {
	for _, machine := range []bool{false, true} {
		fresh := clusterFixture
		check := LaunchLayerCluster
		if machine {
			fresh, check = templateFixture, LaunchLayerMachineTemplate
		}
		for _, field := range []string{"apiVersion", "kind"} {
			for _, value := range []any{nil, "", "wrong", 42, map[string]any{}} {
				obj := fresh()
				obj[field] = value
				require.ErrorContains(t, check(obj, "input"), "input."+field)
			}
		}
		for _, meta := range []any{
			nil, "scalar",
			[]any{},
			map[string]any{"uid": "secret-value"},
			map[string]any{"resourceVersion": "1"},
			map[string]any{"ownerReferences": []any{}},
			map[string]any{"finalizers": []any{}},
			map[string]any{"name": "-invalid"},
			map[string]any{"namespace": "UPPER"},
			map[string]any{"labels": map[string]any{"bad key": "value"}},
			map[string]any{"labels": map[string]any{"key": "bad value"}},
			map[string]any{"annotations": map[string]any{"key": false}},
			map[string]any{"annotations": map[string]any{"key": strings.Repeat("x", 256*1024)}},
		} {
			obj := fresh()
			obj["metadata"] = meta
			require.ErrorContains(t, check(obj, "input"), "input.metadata")
		}
		obj := fresh()
		obj["metadata"] = map[string]any{
			"name": "example-name", "namespace": "example-ns",
			"labels":      map[string]any{"example.org/key": "value"},
			"annotations": map[string]any{"example.org/key": "free form value"},
		}
		require.NoError(t, check(obj, "input"))
	}
	for _, kind := range []string{"LaunchLayerCluster", "launchlayercluster", "LaunchLayerMachine", " LaunchLayerMachineTemplate "} {
		require.True(t, IsLaunchLayer(map[string]any{"kind": kind}))
	}
	require.False(t, IsLaunchLayer(map[string]any{"kind": "DockerCluster"}))
}

func TestLaunchLayerAcceptedIntent(t *testing.T) {
	for _, managed := range []bool{false, true} {
		obj := clusterFixture()
		spec := obj["spec"].(map[string]any)
		if managed {
			delete(spec, "vpcID")
			spec["orgID"] = "example-org"
		}
		spec["siteID"] = ""
		spec["owner"] = map[string]any{"userID": strings.Repeat("\u754c", 253), "username": "operator"}
		for _, cloud := range []bool{false, true} {
			spec["cloudProviderEnabled"] = cloud
			require.NoError(t, LaunchLayerCluster(obj, "infra.cluster"))
		}
	}
	for _, strategy := range []string{"", "BEST_EFFORT", "ALL_OR_NOTHING", "WAIT_FOR_RESOURCE"} {
		obj := templateFixture()
		spec := obj["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		spec["hostOSProfile"], spec["vmProfile"] = "example-host:1", "example-vm:1"
		spec["securityGroups"] = map[string]any{
			"primaryNetworkInterface": []any{"example-sg", "example-sg", ""},
			"gpuNetworkInterface":     []any{},
		}
		if strategy != "" {
			spec["fulfillmentStrategy"] = strategy
		}
		before, err := json.Marshal(obj)
		require.NoError(t, err)
		require.NoError(t, LaunchLayerMachineTemplate(obj, "infra.machineTemplate"))
		after, err := json.Marshal(obj)
		require.NoError(t, err)
		require.Equal(t, before, after)
	}
}
