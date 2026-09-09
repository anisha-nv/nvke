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

	"github.com/NVIDIA/nke/controller/api"
	"github.com/NVIDIA/nke/controller/internal/capi/validation"
	"github.com/NVIDIA/nke/controller/internal/config"
	"github.com/NVIDIA/nke/controller/internal/db"
	"github.com/NVIDIA/nke/controller/internal/server"
	"github.com/NVIDIA/nke/controller/internal/testutil"
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
	identity := "vpcID"
	if managed {
		identity = "orgID"
	}
	body := api.CreateClusterRequest{
		Name: "ll-validation", CniPlugin: "calico", ControlPlaneKubernetesVersion: "1.35.0",
		Infra: &api.InfraClusterProvider{Cluster: map[string]any{
			"apiVersion": validation.LaunchLayerAPIVersion, "kind": "LaunchLayerCluster",
			"spec": map[string]any{
				identity: "example-network", "cloudProviderEnabled": false,
				"owner": map[string]any{"userID": "example-user", "username": "example-operator"},
			},
		}},
		NodePools: &[]api.NodePoolCreateConfig{
			{
				Name: "cp", NodeNamePrefix: "cp", ControlPlane: true, DesiredNodeCount: 1,
				Infra: &api.InfraNodePoolProvider{MachineTemplate: launchLayerTemplate(), SpireAttestationMethod: "joinToken"},
			},
			{
				Name: "workers", NodeNamePrefix: "worker", DesiredNodeCount: 2,
				Infra: &api.InfraNodePoolProvider{MachineTemplate: launchLayerTemplate(), SpireAttestationMethod: "joinToken"},
			},
		},
	}
	if controllerMode {
		body.ControlPlaneConfig = &api.CreateClusterControlPlaneConfig{
			DesiredCount: 1, Infra: &api.InfraControlPlaneProvider{
				MachineTemplate: launchLayerTemplate(), SpireAttestationMethod: "joinToken",
			},
		}
		*body.NodePools = (*body.NodePools)[1:]
	}
	return body
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
		{"owner", func(r *api.CreateClusterRequest) {
			delete(r.Infra.Cluster["spec"].(map[string]any), "owner")
		}, "infra.cluster.spec.owner"},
		{"both network modes", func(r *api.CreateClusterRequest) {
			r.Infra.Cluster["spec"].(map[string]any)["orgID"] = "example-org"
		}, "infra.cluster.spec"},
		{"unknown cluster field", func(r *api.CreateClusterRequest) {
			r.Infra.Cluster["spec"].(map[string]any)["example-secret-key"] = "example-secret-value"
		}, "infra.cluster.spec"},
		{"last pool", func(r *api.CreateClusterRequest) {
			pools := *r.NodePools
			delete(launchLayerSpec(pools[len(pools)-1].Infra.MachineTemplate), "instanceType")
		}, "infra.machineTemplate.spec.template.spec.instanceType"},
		{"zero-sized disabled pool", func(r *api.CreateClusterRequest) {
			pools := *r.NodePools
			pool := &pools[len(pools)-1]
			pool.DesiredNodeCount = 0
			disabled := true
			pool.AutoProvisionDisabled = &disabled
			launchLayerSpec(pool.Infra.MachineTemplate)["instanceType"] = nil
		}, "infra.machineTemplate.spec.template.spec.instanceType"},
		{"wrong version", func(r *api.CreateClusterRequest) {
			(*r.NodePools)[0].Infra.MachineTemplate["apiVersion"] = "example-secret-value"
		}, "infra.machineTemplate.apiVersion"},
		{"foreign template", func(r *api.CreateClusterRequest) {
			(*r.NodePools)[0].Infra.MachineTemplate["kind"] = "DockerMachineTemplate"
		}, "infra.machineTemplate.kind"},
		{"foreign cluster with LL template", func(r *api.CreateClusterRequest) {
			r.Infra.Cluster["kind"] = "DockerCluster"
		}, "infra.cluster.kind"},
		{"control plane intent", func(r *api.CreateClusterRequest) {
			template := (*r.NodePools)[0].Infra.MachineTemplate
			if r.ControlPlaneConfig != nil {
				template = r.ControlPlaneConfig.Infra.MachineTemplate
			}
			launchLayerSpec(template)["providerID"] = "example-secret-value"
		}, "infra.machineTemplate.spec.template.spec"},
		{"shared SPIRE", func(r *api.CreateClusterRequest) {
			(*r.NodePools)[0].Infra.SpireAttestationMethod = "invalid"
		}, "spireAttestationMethod"},
		{"shared CNI", func(r *api.CreateClusterRequest) { r.CniPlugin = "invalid" }, "invalid CNI plugin"},
		{"shared reserved bootstrap file", func(r *api.CreateClusterRequest) {
			(*r.NodePools)[0].Infra.BootstrapFiles = []api.CloudInitFile{{Path: "/etc/spire-agent/agent.conf", Content: "example-config"}}
		}, "managed by CWE"},
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
				require.NotContains(t, response.Body.String(), "example-secret-key")
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
				want, err := json.Marshal(body.Infra.Cluster)
				require.NoError(t, err)
				require.JSONEq(t, string(want), string(tx.clusters[0].Capi.InfraCluster))
				require.Len(t, tx.pools, len(*body.NodePools))
				for i, pool := range tx.pools {
					require.Equal(t, tx.clusters[0].ID, pool.ClusterID)
					expected, err := json.Marshal((*body.NodePools)[i].Infra.MachineTemplate)
					require.NoError(t, err)
					require.JSONEq(t, string(expected), string(pool.Capi.InfraMachineTemplate))
					require.Equal(t, db.SpireAttestationMethodJoinToken, pool.Capi.SpireAttestationMethod)
				}
				require.Len(t, tx.outboxes, 1+len(tx.pools))
				require.Equal(t, "ProvisionCluster", tx.outboxes[0])
				if mode {
					require.Len(t, tx.configs, 1)
					expected, err := json.Marshal(body.ControlPlaneConfig.Infra.MachineTemplate)
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
	delete(launchLayerSpec((*body.NodePools)[1].Infra.MachineTemplate), "instanceType")
	database := &testutil.MockDB{}
	response := launchLayerHTTP(t, launchLayerHandler(t, database, false), http.MethodPost, "/api/clusters", body)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.JSONEq(t, `{"message":"nodePools[1].infra.machineTemplate.spec.template.spec.instanceType: required nonblank string"}`, response.Body.String())
	database.AssertNotCalled(t, "RunInTx", mock.Anything, mock.Anything)
}

func TestLaunchLayerValidationPreservesGenericCreate(t *testing.T) {
	body := launchLayerCreateRequest(false, false)
	body.Infra.Cluster = map[string]any{"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta2", "kind": "DockerCluster"}
	for i := range *body.NodePools {
		(*body.NodePools)[i].Infra.MachineTemplate = map[string]any{
			"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta2", "kind": "DockerMachineTemplate",
		}
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
