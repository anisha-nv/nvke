// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/nke/controller/api"
	"github.com/dsx-ai-factory/nke/controller/internal/capi/validation"
	"github.com/dsx-ai-factory/nke/controller/internal/config"
	"github.com/dsx-ai-factory/nke/controller/internal/db"
	"github.com/dsx-ai-factory/nke/controller/internal/server"
	"github.com/dsx-ai-factory/nke/controller/internal/testutil"
)

func launchLayerTemplate() map[string]any {
	return map[string]any{
		"apiVersion": validation.LaunchLayerAPIVersion, "kind": "LaunchLayerMachineTemplate",
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"instanceType": "example-type", "hostOSProfile": "example-host:1", "vmProfile": "example-vm:1",
			"securityGroups": map[string]any{
				"primaryNetworkInterface": []any{"example-primary"},
				"gpuNetworkInterface":     []any{"example-gpu"},
			},
		}}},
	}
}

func launchLayerCreateRequest(controllerMode, managed bool) api.CreateClusterRequest {
	p := &api.LaunchLayerClusterProvider{
		VpcID: testutil.PtrTo("example-network"), CloudProviderEnabled: testutil.PtrTo(false),
		Owner: api.LaunchLayerOwner{UserID: "example-user", Username: "example-operator"},
	}
	if managed {
		p.VpcID = nil
		p.OrgID = testutil.PtrTo("example-network")
	}
	body := api.CreateClusterRequest{
		Name: "ll-validation", CniPlugin: "calico", ControlPlaneKubernetesVersion: "1.35.0",
		Launchlayer: p,
		NodePools: &[]api.NodePoolCreateConfig{
			{Name: "cp", NodeNamePrefix: "cp", ControlPlane: true, DesiredNodeCount: 1, Launchlayer: launchLayerNodeIntent()},
			{Name: "workers", NodeNamePrefix: "worker", DesiredNodeCount: 2, Launchlayer: launchLayerNodeIntent()},
		},
	}
	if controllerMode {
		body.ControlPlaneConfig = &api.CreateClusterControlPlaneConfig{DesiredCount: 1, Launchlayer: launchLayerNodeIntent()}
		*body.NodePools = (*body.NodePools)[1:]
	}
	return body
}

func launchLayerNodeIntent() *api.LaunchLayerNodePoolProvider {
	return &api.LaunchLayerNodePoolProvider{
		SpireAttestationMethod: "joinToken",
		Machine: api.LaunchLayerMachine{
			InstanceType: "example-type", HostOSProfile: testutil.PtrTo("example-host:1"), VMProfile: testutil.PtrTo("example-vm:1"),
			SecurityGroups: &api.LaunchLayerSecurityGroups{PrimaryNetworkInterface: &[]string{"example-primary"}, GpuNetworkInterface: &[]string{"example-gpu"}},
		},
	}
}

func launchLayerSpec(template map[string]any) map[string]any {
	return template["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
}

func launchLayerHandler(t *testing.T, database *testutil.MockDB, controllerMode bool) http.Handler {
	t.Helper()
	// Same production OpenAPI/strict-handler stack as newTestServerWithValidator,
	// without its optional Forge-only transaction stubs; every write is asserted.
	srv, err := server.NewServer(config.Config{
		ProvisionControlPlaneWithControlPlaneController: controllerMode,
		SupportedInfraProviders:                         []string{"nico", "launchlayer", "infra"},
	}, discardLogger(), database, nil, nil)
	require.NoError(t, err)
	return srv.Handler
}

func launchLayerHTTP(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func TestLaunchLayerCreateHTTPRejectsBeforeTransaction(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*api.CreateClusterRequest)
		path   string
	}{
		{"blank owner", func(r *api.CreateClusterRequest) { r.Launchlayer.Owner.UserID = " " }, "launchlayer.owner.userID"},
		{"both networks", func(r *api.CreateClusterRequest) { r.Launchlayer.OrgID = testutil.PtrTo("other") }, "launchlayer"},
		{"last pool", func(r *api.CreateClusterRequest) {
			p := *r.NodePools
			p[len(p)-1].Launchlayer.Machine.InstanceType = " "
		}, "launchlayer.machine.instanceType"},
		{"disabled empty pool", func(r *api.CreateClusterRequest) {
			p := *r.NodePools
			p[len(p)-1].DesiredNodeCount = 0
			p[len(p)-1].AutoProvisionDisabled = testutil.PtrTo(true)
			p[len(p)-1].Launchlayer.Machine.InstanceType = " "
		}, "launchlayer.machine.instanceType"},
		{"mixed root", func(r *api.CreateClusterRequest) { r.Infra = &api.InfraClusterProvider{} }, ""},
		{"mixed pool", func(r *api.CreateClusterRequest) { (*r.NodePools)[0].Infra = &api.InfraNodePoolProvider{} }, ""},
		{"wrong pool mode", func(r *api.CreateClusterRequest) {
			p := &(*r.NodePools)[0]
			p.Launchlayer = nil
			p.Infra = &api.InfraNodePoolProvider{MachineTemplate: launchLayerTemplate(), SpireAttestationMethod: "joinToken"}
		}, "immutable"},
		{"shared SPIRE", func(r *api.CreateClusterRequest) { (*r.NodePools)[0].Launchlayer.SpireAttestationMethod = "invalid" }, "spireAttestationMethod"},
		{"shared CNI", func(r *api.CreateClusterRequest) { r.CniPlugin = "invalid" }, "invalid CNI plugin"},
		{"reserved bootstrap", func(r *api.CreateClusterRequest) {
			(*r.NodePools)[0].Launchlayer.BootstrapFiles = &[]api.CloudInitFile{{Path: "/etc/spire-agent/agent.conf", Content: "example-secret-value"}}
		}, "bootstrapFiles"},
		{"shared network", func(r *api.CreateClusterRequest) {
			r.Network = &api.ClusterNetworkCreateConfig{PodCIDR: "192.168.0.0/16", ServiceCIDR: "192.168.0.0/24"}
		}, "overlap"},
		{"shared version", func(r *api.CreateClusterRequest) { r.ControlPlaneKubernetesVersion = "invalid" }, "controlPlaneKubernetesVersion"},
	}
	for _, mode := range []bool{false, true} {
		for _, tc := range tests {
			t.Run(fmt.Sprintf("controller=%t/%s", mode, tc.name), func(t *testing.T) {
				body := launchLayerCreateRequest(mode, false)
				tc.mutate(&body)
				database := &testutil.MockDB{}
				response := launchLayerHTTP(t, launchLayerHandler(t, database, mode), http.MethodPost, "/api/clusters", body)
				require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
				require.Contains(t, response.Body.String(), tc.path)
				require.NotContains(t, response.Body.String(), "example-secret-value")
				database.AssertNotCalled(t, "RunInTx", mock.Anything, mock.Anything)
			})
		}
	}
}

// Return the actual written values, not a canned 201 fixture. Unimplemented
// methods panic, so unexpected DB operations cannot be silently accepted.
type launchLayerRecordingTx struct {
	db.Tx
	clusters []db.Cluster
	pools    []db.NodePool
	configs  []db.ControlPlaneConfiguration
	outboxes []string
}

func (tx *launchLayerRecordingTx) CreateCluster(_ context.Context, cluster *db.Cluster) (db.Cluster, error) {
	result := *cluster
	result.ID = uuid.New()
	tx.clusters = append(tx.clusters, result)
	return result, nil
}

func (tx *launchLayerRecordingTx) UpdateCluster(_ context.Context, cluster *db.Cluster) (db.Cluster, error) {
	return *cluster, nil
}

func (tx *launchLayerRecordingTx) CreateNodePool(_ context.Context, pool *db.NodePool) (db.NodePool, error) {
	result := *pool
	result.ID = uuid.New()
	tx.pools = append(tx.pools, result)
	return result, nil
}

func (tx *launchLayerRecordingTx) CreateControlPlaneConfiguration(_ context.Context, row *db.ControlPlaneConfiguration) (db.ControlPlaneConfiguration, error) {
	result := *row
	result.ID = uuid.New()
	tx.configs = append(tx.configs, result)
	return result, nil
}

func (tx *launchLayerRecordingTx) CreateWorkflowOutboxEntry(_ context.Context, workflowType, _ string, _ any) (db.WorkflowOutbox, error) {
	tx.outboxes = append(tx.outboxes, workflowType)
	return db.WorkflowOutbox{ID: uuid.New()}, nil
}

func TestLaunchLayerCreateHTTPPersistsIntent(t *testing.T) {
	for _, mode := range []bool{false, true} {
		for _, managed := range []bool{false, true} {
			t.Run(fmt.Sprintf("controller=%t/managed=%t", mode, managed), func(t *testing.T) {
				body := launchLayerCreateRequest(mode, managed)
				tx := &launchLayerRecordingTx{}
				database := &testutil.MockDB{GetTx: func() db.Tx { return tx }}
				database.On("RunInTx", mock.Anything, mock.Anything).Return(nil).Once()
				response := launchLayerHTTP(t, launchLayerHandler(t, database, mode), http.MethodPost, "/api/clusters", body)
				require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
				require.Len(t, tx.clusters, 1)
				require.Equal(t, db.APIProviderLaunchLayer, tx.clusters[0].APIProvider)
				var responseCluster api.Cluster
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &responseCluster))
				require.Equal(t, body.Launchlayer, responseCluster.Launchlayer)
				require.Nil(t, responseCluster.Infra)
				want, err := json.Marshal(map[string]any{"apiVersion": validation.LaunchLayerAPIVersion, "kind": "LaunchLayerCluster", "spec": body.Launchlayer})
				require.NoError(t, err)
				require.JSONEq(t, string(want), string(tx.clusters[0].Capi.InfraCluster))
				require.Len(t, tx.pools, len(*body.NodePools))
				for _, pool := range tx.pools {
					require.Equal(t, tx.clusters[0].ID, pool.ClusterID)
					expected, err := json.Marshal(launchLayerTemplate())
					require.NoError(t, err)
					require.JSONEq(t, string(expected), string(pool.Capi.InfraMachineTemplate))
					require.Equal(t, db.SpireAttestationMethodJoinToken, pool.Capi.SpireAttestationMethod)
				}
				require.Len(t, tx.outboxes, 1+len(tx.pools))
				require.Equal(t, "ProvisionCluster", tx.outboxes[0])
				if mode {
					require.Len(t, tx.configs, 1)
					expected, err := json.Marshal(launchLayerTemplate())
					require.NoError(t, err)
					require.JSONEq(t, string(expected), string(tx.configs[0].Config.Capi.InfraMachineTemplatePreferences[0]))
				} else {
					require.Empty(t, tx.configs)
				}
				database.AssertExpectations(t)
			})
		}
	}
}

func TestLaunchLayerSecondPoolErrorHasFullPath(t *testing.T) {
	body := launchLayerCreateRequest(false, false)
	(*body.NodePools)[1].Launchlayer.Machine.InstanceType = " "
	database := &testutil.MockDB{}
	response := launchLayerHTTP(t, launchLayerHandler(t, database, false), http.MethodPost, "/api/clusters", body)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.JSONEq(t, `{"message":"nodePools[1].launchlayer.machine.instanceType: required nonblank string"}`, response.Body.String())
	database.AssertNotCalled(t, "RunInTx", mock.Anything, mock.Anything)
}

func TestLaunchLayerValidationPreservesGenericCreate(t *testing.T) {
	body := launchLayerCreateRequest(false, false)
	body.Launchlayer = nil
	body.Infra = &api.InfraClusterProvider{Cluster: map[string]any{"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta2", "kind": "DockerCluster"}}
	for i := range *body.NodePools {
		(*body.NodePools)[i].Launchlayer = nil
		(*body.NodePools)[i].Infra = &api.InfraNodePoolProvider{SpireAttestationMethod: "joinToken", MachineTemplate: map[string]any{"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta2", "kind": "DockerMachineTemplate"}}
	}
	tx := &launchLayerRecordingTx{}
	database := &testutil.MockDB{GetTx: func() db.Tx { return tx }}
	database.On("RunInTx", mock.Anything, mock.Anything).Return(nil).Once()
	response := launchLayerHTTP(t, launchLayerHandler(t, database, false), http.MethodPost, "/api/clusters", body)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	require.Len(t, tx.clusters, 1)
	require.Len(t, tx.pools, 2)
	database.AssertExpectations(t)
}

func TestLaunchLayerPreflightAlsoRejectsInvalidNicoInitialPool(t *testing.T) {
	body := openAPIForgeCreateClusterRequest()
	(*body.NodePools)[0].Nico.VpcPrefixIDs = []uuid.UUID{uuid.New()}
	database := &testutil.MockDB{}
	response := launchLayerHTTP(t, launchLayerHandler(t, database, false), http.MethodPost, "/api/clusters", body)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "nodePools[0]")
	database.AssertNotCalled(t, "RunInTx", mock.Anything, mock.Anything)
}

func TestLaunchLayerDocumentedRequestPassesHTTPAdmission(t *testing.T) {
	data, err := os.ReadFile("../../../docs/examples/launchlayer-cluster.json")
	require.NoError(t, err)
	var body api.CreateClusterRequest
	require.NoError(t, json.Unmarshal(data, &body))
	tx := &launchLayerRecordingTx{}
	database := &testutil.MockDB{GetTx: func() db.Tx { return tx }}
	database.On("RunInTx", mock.Anything, mock.Anything).Return(nil).Once()
	response := launchLayerHTTP(t, launchLayerHandler(t, database, false), http.MethodPost, "/api/clusters", body)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	require.Len(t, tx.clusters, 1)
	require.Len(t, tx.pools, 2)
	database.AssertExpectations(t)
}
