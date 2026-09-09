// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nke/controller/api"
	"github.com/NVIDIA/nke/controller/internal/db"
	"github.com/NVIDIA/nke/controller/internal/server"
	"github.com/NVIDIA/nke/controller/internal/testutil"
)

func launchLayerStoredCluster(t *testing.T) db.Cluster {
	t.Helper()
	body := launchLayerCreateRequest(false, false)
	cluster, err := server.DBClusterFromAPI(&body)
	require.NoError(t, err)
	cluster.ID = uuid.New()
	cluster.PodCIDR, cluster.ServiceCIDR = "192.168.0.0/16", "10.96.0.0/12"
	return cluster
}

func launchLayerStoredPool(t *testing.T, clusterID uuid.UUID) db.NodePool {
	t.Helper()
	raw, err := json.Marshal(launchLayerTemplate())
	require.NoError(t, err)
	pool := db.NodePool{ID: uuid.New(), ClusterID: clusterID, Name: "workers", NodeNamePrefix: "worker", DesiredCount: 2}
	pool.SetCapi(db.CAPINodePoolProvider{
		InfraMachineTemplate: raw, SpireAttestationMethod: db.SpireAttestationMethodJoinToken,
	})
	return pool
}

func TestLaunchLayerStandaloneCreateRejectsMismatchedBinding(t *testing.T) {
	for _, kind := range []string{"DockerMachineTemplate", "LaunchLayerMachine", "launchlayermachinetemplate"} {
		t.Run(kind, func(t *testing.T) {
			cluster := launchLayerStoredCluster(t)
			body := api.CreateNodePoolRequest{
				ClusterID: cluster.ID, Name: "new-pool", NodeNamePrefix: "new", DesiredNodeCount: 0,
				Infra: &api.InfraNodePoolProvider{MachineTemplate: launchLayerTemplate(), SpireAttestationMethod: "joinToken"},
			}
			body.Infra.MachineTemplate["kind"] = kind
			database := &testutil.MockDB{}
			database.On("GetCluster", mock.Anything, cluster.ID).Return(cluster, nil).Once()
			response := launchLayerHTTP(t, launchLayerHandler(t, database, false), http.MethodPost, "/api/nodePools", body)
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), "infra.machineTemplate.kind")
			database.AssertNotCalled(t, "RunInTx", mock.Anything, mock.Anything)
			database.AssertExpectations(t)
		})
	}
}

func TestLaunchLayerNodePoolPatchValidatesMergedIntent(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		patch                               map[string]any
		badStoredTemplate, badStoredCluster bool
		wantStatus                          int
	}{
		{"foreign provider", map[string]any{"infra": map[string]any{"machineTemplate": map[string]any{
			"apiVersion": "infrastructure.cluster.x-k8s.io/v1alpha1", "kind": "DockerMachineTemplate",
		}}}, false, false, http.StatusBadRequest},
		{"clear template", map[string]any{"infra": map[string]any{"machineTemplate": map[string]any{}}}, false, false, http.StatusBadRequest},
		{"null template", map[string]any{"infra": map[string]any{"machineTemplate": nil}}, false, false, http.StatusBadRequest},
		{"invalid attestation", map[string]any{"infra": map[string]any{"spireAttestationMethod": "invalid"}}, false, false, http.StatusBadRequest},
		{"merged old template invalid", map[string]any{"infra": map[string]any{"spireAttestationMethod": "tpm"}}, true, false, http.StatusBadRequest},
		{"stored binding invalid", map[string]any{"infra": map[string]any{"machineTemplate": launchLayerTemplate()}}, false, true, http.StatusBadRequest},
		{"valid template", map[string]any{"infra": map[string]any{"machineTemplate": launchLayerTemplate()}}, false, false, http.StatusOK},
		{"preserve omitted template", map[string]any{"infra": map[string]any{"spireAttestationMethod": "tpm"}}, false, false, http.StatusOK},
		{"scalar maintenance on legacy binding", map[string]any{"description": "new description"}, true, true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := launchLayerStoredCluster(t)
			pool := launchLayerStoredPool(t, cluster.ID)
			if tc.badStoredTemplate {
				template := launchLayerTemplate()
				delete(launchLayerSpec(template), "instanceType")
				raw, err := json.Marshal(template)
				require.NoError(t, err)
				provider, err := pool.CAPIProvider()
				require.NoError(t, err)
				provider.InfraMachineTemplate = raw
				pool.SetCapi(provider)
			}
			if tc.badStoredCluster {
				cluster.Capi.InfraCluster = json.RawMessage(`{"apiVersion":"infrastructure.cluster.x-k8s.io/v1alpha1","kind":"LaunchLayerCluster","spec":{"vpcID":"example-vpc"}}`)
			}
			before := append([]byte(nil), pool.Capi.InfraMachineTemplate...)
			tx := &testutil.MockTx{}
			database := &testutil.MockDB{MockTx: tx}
			database.On("RunInTx", mock.Anything, mock.Anything).Return(nil).Maybe()
			tx.On("GetNodePool", mock.Anything, pool.ID).Return(pool, nil).Maybe()
			tx.On("GetClusterForUpdate", mock.Anything, cluster.ID).Return(cluster, nil).Maybe()
			tx.On("GetNodePoolForUpdate", mock.Anything, pool.ID).Return(pool, nil).Maybe()
			tx.On("ListNodePools", mock.Anything, mock.Anything).Return([]db.NodePool{pool}, nil).Maybe()
			if tc.wantStatus == http.StatusOK {
				tx.On("UpdateNodePool", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					written := args.Get(1).(*db.NodePool)
					require.JSONEq(t, string(before), string(written.Capi.InfraMachineTemplate))
				}).Return(pool, nil).Once()
			}
			response := launchLayerHTTP(t, launchLayerHandler(t, database, false), http.MethodPatch,
				"/api/nodePools/"+pool.ID.String(), tc.patch)
			require.Equal(t, tc.wantStatus, response.Code, response.Body.String())
			if tc.wantStatus == http.StatusBadRequest {
				tx.AssertNotCalled(t, "UpdateNodePool", mock.Anything, mock.Anything)
			}
			tx.AssertNotCalled(t, "CreateWorkflowOutboxEntry", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			tx.AssertExpectations(t)
		})
	}
}

func TestLaunchLayerControlPlanePatchRejectsInvalidEffectiveTemplate(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		template                             map[string]any
		extraBadPreference, badStoredBinding bool
	}{
		{"clear template", map[string]any{}, false, false},
		{"missing machine spec", map[string]any{"apiVersion": "infrastructure.cluster.x-k8s.io/v1alpha1", "kind": "LaunchLayerMachineTemplate"}, false, false},
		{"wrong binding", launchLayerTemplate(), false, true},
		{"invalid later preference", nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := launchLayerStoredCluster(t)
			raw, err := json.Marshal(launchLayerTemplate())
			require.NoError(t, err)
			provider := db.CAPIControlPlaneProvider{
				InfraMachineTemplatePreferences: []json.RawMessage{raw}, SpireAttestationMethod: db.SpireAttestationMethodJoinToken,
			}
			if tc.extraBadPreference {
				provider.InfraMachineTemplatePreferences = append(provider.InfraMachineTemplatePreferences, json.RawMessage(`{"kind":"LaunchLayerMachineTemplate"}`))
			}
			cluster.ControlPlaneConfig.SetCapi(provider)
			cluster.ControlPlaneConfig.DesiredCount = 1
			if tc.badStoredBinding {
				cluster.Capi.InfraCluster = json.RawMessage(`{"kind":"LaunchLayerCluster"}`)
			}
			infra := map[string]any{"spireAttestationMethod": "tpm"}
			if tc.template != nil {
				infra["machineTemplate"] = tc.template
			}
			body := map[string]any{
				"controlPlaneConfig":   map[string]any{"infra": infra},
				"enableEtcdEncryption": true,
			}
			tx := &testutil.MockTx{}
			database := &testutil.MockDB{MockTx: tx}
			database.On("RunInTx", mock.Anything, mock.Anything).Return(nil).Maybe()
			tx.On("GetClusterForUpdate", mock.Anything, cluster.ID).Return(cluster, nil).Maybe()
			response := launchLayerHTTP(t, launchLayerHandler(t, database, true), http.MethodPatch,
				fmt.Sprintf("/api/clusters/%s", cluster.ID), body)
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			tx.AssertNotCalled(t, "UpdateCluster", mock.Anything, mock.Anything)
			tx.AssertNotCalled(t, "CreateControlPlaneConfiguration", mock.Anything, mock.Anything)
			tx.AssertNotCalled(t, "CreateWorkflowOutboxEntry", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

type launchLayerLifecycleTx struct {
	launchLayerRecordingTx
	cluster db.Cluster
}

func (tx *launchLayerLifecycleTx) GetClusterForUpdate(context.Context, uuid.UUID) (db.Cluster, error) {
	return tx.cluster, nil
}

func (tx *launchLayerLifecycleTx) ListNodePools(context.Context, db.NodePoolFilters) ([]db.NodePool, error) {
	return tx.pools, nil
}

func (tx *launchLayerLifecycleTx) GetControlPlaneConfiguration(_ context.Context, id uuid.UUID) (db.ControlPlaneConfiguration, error) {
	for _, row := range tx.configs {
		if row.ID == id {
			return row, nil
		}
	}
	return db.ControlPlaneConfiguration{}, db.ErrNotFound
}

func TestLaunchLayerStandaloneCreatePersistsIntent(t *testing.T) {
	cluster := launchLayerStoredCluster(t)
	tx := &launchLayerLifecycleTx{cluster: cluster}
	database := &testutil.MockDB{GetTx: func() db.Tx { return tx }}
	database.On("GetCluster", mock.Anything, cluster.ID).Return(cluster, nil).Once()
	database.On("RunInTx", mock.Anything, mock.Anything).Return(nil).Once()
	body := api.CreateNodePoolRequest{
		ClusterID: cluster.ID, Name: "later-pool", NodeNamePrefix: "later", DesiredNodeCount: 1,
		Infra: &api.InfraNodePoolProvider{MachineTemplate: launchLayerTemplate(), SpireAttestationMethod: "joinToken"},
	}
	response := launchLayerHTTP(t, launchLayerHandler(t, database, false), http.MethodPost, "/api/nodePools", body)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	require.Len(t, tx.pools, 1)
	expected, err := json.Marshal(body.Infra.MachineTemplate)
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(tx.pools[0].Capi.InfraMachineTemplate))
	require.Equal(t, []string{"ProvisionNodePool"}, tx.outboxes)
	database.AssertExpectations(t)
}

func TestLaunchLayerControlPlanePatchPreservesOmittedTemplate(t *testing.T) {
	cluster := launchLayerStoredCluster(t)
	raw, err := json.Marshal(launchLayerTemplate())
	require.NoError(t, err)
	cluster.ControlPlaneConfig.SetCapi(db.CAPIControlPlaneProvider{
		InfraMachineTemplatePreferences: []json.RawMessage{raw}, SpireAttestationMethod: db.SpireAttestationMethodJoinToken,
	})
	cluster.ControlPlaneConfig.DesiredCount = 1
	tx := &launchLayerLifecycleTx{cluster: cluster}
	database := &testutil.MockDB{GetTx: func() db.Tx { return tx }}
	database.On("RunInTx", mock.Anything, mock.Anything).Return(nil).Once()
	body := map[string]any{"controlPlaneConfig": map[string]any{"infra": map[string]any{"spireAttestationMethod": "tpm"}}}
	response := launchLayerHTTP(t, launchLayerHandler(t, database, true), http.MethodPatch, "/api/clusters/"+cluster.ID.String(), body)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Len(t, tx.configs, 1)
	require.JSONEq(t, string(raw), string(tx.configs[0].Config.Capi.InfraMachineTemplatePreferences[0]))
	require.Equal(t, db.SpireAttestationMethodTPM, tx.configs[0].Config.Capi.SpireAttestationMethod)
	require.Empty(t, tx.outboxes)
	database.AssertExpectations(t)
}
