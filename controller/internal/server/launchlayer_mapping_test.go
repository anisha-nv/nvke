// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/nke/controller/api"
	"github.com/dsx-ai-factory/nke/controller/internal/db"
	"github.com/dsx-ai-factory/nke/controller/internal/testutil"
)

func TestTypedLaunchLayerMappingAndReplacement(t *testing.T) {
	request := api.CreateClusterRequest{Launchlayer: &api.LaunchLayerClusterProvider{
		OrgID: testutil.PtrTo("org"), SiteID: testutil.PtrTo(""), CloudProviderEnabled: testutil.PtrTo(false),
		Owner: api.LaunchLayerOwner{UserID: "user", Username: "operator"},
	}}
	cluster, err := DBClusterFromAPI(&request)
	require.NoError(t, err)
	got, err := APIClusterFromDB(&cluster)
	require.NoError(t, err)
	require.Equal(t, request.Launchlayer, got.Launchlayer)
	require.Nil(t, got.Infra)
	intent := &api.LaunchLayerNodePoolProvider{
		Machine: api.LaunchLayerMachine{
			InstanceType: "before", HostOSProfile: testutil.PtrTo("host:1"), VMProfile: testutil.PtrTo("vm:2"),
			FulfillmentStrategy: testutil.PtrTo(api.LaunchLayerMachineFulfillmentStrategyWAITFORRESOURCE),
			SecurityGroups:      &api.LaunchLayerSecurityGroups{PrimaryNetworkInterface: &[]string{"one", "two"}, GpuNetworkInterface: &[]string{}},
		},
		BootstrapFiles:     &[]api.CloudInitFile{{Path: "/etc/example", Content: "safe"}},
		PreKubeadmCommands: &[]string{"example-command"},
	}
	require.NoError(t, validateLaunchLayerNodeProvider(intent, "launchlayer"))
	provider, err := dbLaunchLayerNodeProvider(intent)
	require.NoError(t, err)
	roundTrip, err := apiLaunchLayerNodeProvider(&provider)
	require.NoError(t, err)
	require.Equal(t, intent, roundTrip)
	pool := db.NodePool{ClusterAPIProvider: db.APIProviderLaunchLayer}
	pool.SetCapi(provider)
	original := append([]byte(nil), provider.InfraMachineTemplate...)
	patch := &api.UpdateNodePoolRequest{Launchlayer: &api.LaunchLayerNodePoolProviderUpdate{PreKubeadmCommands: &[]string{}, BootstrapFiles: &[]api.CloudInitFile{}}}
	require.NoError(t, applyUpdateNodePoolRequest(cluster, &pool, patch))
	require.JSONEq(t, string(original), string(pool.Capi.InfraMachineTemplate))
	require.Empty(t, pool.Capi.PreKubeadmCommands)
	require.Empty(t, pool.Capi.BootstrapFiles)
	require.Equal(t, []string{"example-command"}, provider.PreKubeadmCommands, "the previous snapshot must not be mutated")
	patch.Launchlayer.Machine = &api.LaunchLayerMachine{InstanceType: "after"}
	require.NoError(t, applyUpdateNodePoolRequest(cluster, &pool, patch))
	read, err := apiNodePoolFromDB(&pool)
	require.NoError(t, err)
	require.Equal(t, api.LaunchLayerMachine{InstanceType: "after"}, read.Launchlayer.Machine)
	require.Nil(t, read.Infra)
	require.NoError(t, validateLaunchLayerNodePoolUpdate(cluster, &pool, patch))
	// The identical replacement contract applies to the control-plane snapshot.
	spec := db.ControlPlaneConfigSpec{}
	spec.SetCapi(db.CAPIControlPlaneProvider{
		InfraMachineTemplatePreferences: []json.RawMessage{original, original},
		SpireAttestationMethod:          db.SpireAttestationMethodJoinToken, BootstrapFiles: provider.BootstrapFiles, PreKubeadmCommands: provider.PreKubeadmCommands,
	})
	old := spec
	service := &clusterService{}
	require.NoError(t, service.applyClusterLevelControlPlaneSpecUpdate(t.Context(), &cluster, &api.UpdateClusterControlPlaneConfig{Launchlayer: patch.Launchlayer}, &spec))
	require.Len(t, spec.Capi.InfraMachineTemplatePreferences, 1)
	cp, err := apiLaunchLayerControlPlane(spec.Capi)
	require.NoError(t, err)
	require.Equal(t, []api.LaunchLayerMachine{{InstanceType: "after"}}, cp.MachinePreferences)
	require.Empty(t, cp.BootstrapFiles)
	require.Empty(t, cp.PreKubeadmCommands)
	require.Len(t, old.Capi.InfraMachineTemplatePreferences, 2)
	require.Equal(t, []string{"example-command"}, old.Capi.PreKubeadmCommands)
}

func TestTypedLaunchLayerMalformedStoredResponsesFailClosed(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"kind":"LaunchLayerMachineTemplate"}`,
		`{"apiVersion":"infrastructure.cluster.x-k8s.io/v1alpha1","kind":"LaunchLayerMachineTemplate","spec":{"template":{"spec":{"instanceType":"x","providerID":"secret"}}}}`,
	} {
		pool := db.NodePool{ClusterAPIProvider: db.APIProviderLaunchLayer}
		pool.SetCapi(db.CAPINodePoolProvider{InfraMachineTemplate: json.RawMessage(raw), SpireAttestationMethod: db.SpireAttestationMethodJoinToken})
		out, err := apiNodePoolFromDB(&pool)
		require.ErrorIs(t, err, errStoredLaunchLayerIntent)
		require.Nil(t, out.Infra)
		require.Nil(t, out.Launchlayer)
		set := db.NodeSet{ClusterAPIProvider: db.APIProviderLaunchLayer}
		set.SetCapi(*pool.Capi)
		_, err = apiNodeSetFromDB(&set)
		require.ErrorIs(t, err, errStoredLaunchLayerIntent)
	}
}

func TestTypedLaunchLayerConfigHistoryProjection(t *testing.T) {
	previous, err := json.Marshal(launchLayerMachineTemplate(api.LaunchLayerMachine{InstanceType: "before"}))
	require.NoError(t, err)
	current, err := json.Marshal(launchLayerMachineTemplate(api.LaunchLayerMachine{InstanceType: "after"}))
	require.NoError(t, err)
	for _, mode := range []db.APIProvider{"", db.APIProviderInfra, db.APIProviderLaunchLayer} {
		t.Run(string(mode), func(t *testing.T) {
			changes := []api.NodePoolConfigChange{{ChangedFields: []api.NodePoolConfigField{
				{Field: "capiInfraMachineTemplate", From: testutil.PtrTo(string(previous)), To: string(current)},
				{Field: "capiSpireAttestationMethod", To: "tpm"},
				{Field: "capiBootstrapFiles", To: "[]"},
				{Field: "capiPreKubeadmCommands", To: "[]"},
				{Field: "kubernetesVersion", To: "1.35.0"},
			}}}
			got, err := typedConfigHistory(db.NodePool{ClusterAPIProvider: mode}, changes)
			require.NoError(t, err)
			fields := got[0].ChangedFields
			if mode == db.APIProviderLaunchLayer {
				require.Equal(t, "launchlayer.machine", fields[0].Field)
				require.JSONEq(t, `{"instanceType":"before"}`, *fields[0].From)
				require.JSONEq(t, `{"instanceType":"after"}`, fields[0].To)
				require.Equal(t, "launchlayer.spireAttestationMethod", fields[1].Field)
				require.Equal(t, "launchlayer.bootstrapFiles", fields[2].Field)
				require.Equal(t, "launchlayer.preKubeadmCommands", fields[3].Field)
			} else {
				require.Equal(t, "capiInfraMachineTemplate", fields[0].Field)
				require.Equal(t, string(previous), *fields[0].From)
				require.Equal(t, string(current), fields[0].To)
			}
			require.Equal(t, "kubernetesVersion", fields[4].Field)
		})
	}
	_, err = typedConfigHistory(db.NodePool{ClusterAPIProvider: db.APIProviderLaunchLayer}, []api.NodePoolConfigChange{{ChangedFields: []api.NodePoolConfigField{{Field: "capiInfraMachineTemplate", To: `{"private-field":"example-value"}`}}}})
	require.ErrorIs(t, err, errStoredLaunchLayerIntent)
	require.NotContains(t, err.Error(), "example-value")
}
