// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/nke/controller/api"
	"github.com/dsx-ai-factory/nke/controller/internal/config"
	"github.com/dsx-ai-factory/nke/controller/internal/db"
	"github.com/dsx-ai-factory/nke/controller/internal/server"
	"github.com/dsx-ai-factory/nke/controller/internal/testutil"
)

func TestLaunchLayerClosedSchemaAndRedactedErrors(t *testing.T) {
	// Each typed write location passes through the production OpenAPI stack.
	for _, surface := range []string{"cluster", "initialPool", "controlPlane", "laterPool", "poolPatch", "controlPlanePatch"} {
		for _, invalid := range []string{"unknown", "null", "raw", "credential", "bad-type"} {
			t.Run(surface+"/"+invalid, func(t *testing.T) {
				body := launchLayerCreateRequest(surface == "controlPlane", false)
				data, err := json.Marshal(body)
				require.NoError(t, err)
				var root map[string]any
				require.NoError(t, json.Unmarshal(data, &root))
				method, path := http.MethodPost, "/api/clusters"
				target := root["launchlayer"].(map[string]any)
				switch surface {
				case "initialPool":
					target = root["nodePools"].([]any)[0].(map[string]any)["launchlayer"].(map[string]any)
				case "controlPlane":
					target = root["controlPlaneConfig"].(map[string]any)["launchlayer"].(map[string]any)
				case "laterPool":
					root = root["nodePools"].([]any)[0].(map[string]any)
					root["clusterID"] = uuid.NewString()
					target = root["launchlayer"].(map[string]any)
					path = "/api/nodePools"
				case "poolPatch", "controlPlanePatch":
					target = map[string]any{"machine": map[string]any{"instanceType": "example-type"}}
					root = map[string]any{"launchlayer": target}
					method = http.MethodPatch
					path = "/api/nodePools/" + uuid.NewString()
					if surface == "controlPlanePatch" {
						root = map[string]any{"controlPlaneConfig": root}
						path = "/api/clusters/" + uuid.NewString()
					}
				}
				switch invalid {
				case "unknown":
					target["example-secret-key"] = "example-secret-value"
				case "raw":
					target["apiVersion"] = "example-secret-value"
				case "credential":
					target["credentials"] = map[string]any{"token": "example-secret-value"}
				case "null":
					if surface == "cluster" {
						target["owner"] = nil
					} else {
						target["machine"] = nil
					}
				case "bad-type":
					if surface == "cluster" {
						target["owner"] = map[string]any{"userID": []any{"example-secret-value"}, "username": "user"}
					} else {
						target["machine"] = map[string]any{"instanceType": []any{"example-secret-value"}}
					}
				}
				database := &testutil.MockDB{}
				var logs bytes.Buffer
				srv, err := server.NewServer(config.Config{SupportedInfraProviders: []string{"launchlayer"}, ProvisionControlPlaneWithControlPlaneController: true},
					slog.New(slog.NewTextHandler(&logs, nil)), database, nil, nil)
				require.NoError(t, err)
				response := launchLayerHTTP(t, srv.Handler, method, path, root)
				require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
				require.Less(t, len(response.Body.String()), 512)
				for _, secret := range []string{"example-secret-key", "example-secret-value"} {
					require.NotContains(t, response.Body.String(), secret)
					require.NotContains(t, logs.String(), secret)
				}
				database.AssertNotCalled(t, "RunInTx", mock.Anything, mock.Anything)
			})
		}
	}
}

func TestLaunchLayerAndGenericAdmissionAreIndependent(t *testing.T) {
	for _, tc := range []struct {
		enabled  []string
		generic  bool
		accepted bool
	}{
		{[]string{"launchlayer"}, false, true},
		{[]string{"infra"}, false, false},
		{[]string{"nico"}, false, false},
		{[]string{"launchlayer", "infra"}, false, true},
		{[]string{"infra"}, true, true},
		{[]string{"launchlayer"}, true, false},
	} {
		t.Run(stringJoinModes(tc.enabled, tc.generic), func(t *testing.T) {
			body := launchLayerCreateRequest(false, false)
			if tc.generic {
				// Deliberately incomplete CAPLL specs are still valid generic envelopes.
				body.Launchlayer = nil
				body.Infra = &api.InfraClusterProvider{Cluster: map[string]any{"apiVersion": "infrastructure.cluster.x-k8s.io/v1alpha1", "kind": "LaunchLayerCluster"}}
				for i := range *body.NodePools {
					p := &(*body.NodePools)[i]
					p.Launchlayer = nil
					p.Infra = &api.InfraNodePoolProvider{SpireAttestationMethod: "joinToken", MachineTemplate: map[string]any{"apiVersion": "infrastructure.cluster.x-k8s.io/v1alpha1", "kind": "LaunchLayerMachineTemplate"}}
				}
			}
			tx := &launchLayerRecordingTx{}
			database := &testutil.MockDB{GetTx: func() db.Tx { return tx }}
			if tc.accepted {
				database.On("RunInTx", mock.Anything, mock.Anything).Return(nil).Once()
			}
			srv, err := server.NewServer(config.Config{SupportedInfraProviders: tc.enabled}, discardLogger(), database, nil, nil)
			require.NoError(t, err)
			response := launchLayerHTTP(t, srv.Handler, http.MethodPost, "/api/clusters", body)
			if tc.accepted {
				require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
				var got api.Cluster
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &got))
				if tc.generic {
					require.Nil(t, got.Launchlayer)
					require.NotNil(t, got.Infra)
				} else {
					require.NotNil(t, got.Launchlayer)
					require.Nil(t, got.Infra)
				}
			} else {
				require.Equal(t, http.StatusBadRequest, response.Code)
				require.Empty(t, tx.outboxes)
			}
			database.AssertExpectations(t)
		})
	}
}

func stringJoinModes(modes []string, generic bool) string {
	return fmt.Sprintf("%v/generic=%t", modes, generic)
}

func TestGenericManifestExtensionsSurviveAdmission(t *testing.T) {
	for _, kind := range []string{"LaunchLayer", "Future"} {
		t.Run(kind, func(t *testing.T) {
			body := launchLayerCreateRequest(false, false)
			body.Launchlayer = nil
			body.Infra = &api.InfraClusterProvider{Cluster: map[string]any{
				"apiVersion": "infrastructure.cluster.x-k8s.io/v1alpha1", "kind": kind + "Cluster",
				"spec": map[string]any{"futureExtension": map[string]any{"arbitrary": []any{"one", false, 12}}},
			}}
			for i := range *body.NodePools {
				p := &(*body.NodePools)[i]
				p.Launchlayer = nil
				p.Infra = &api.InfraNodePoolProvider{SpireAttestationMethod: "joinToken", MachineTemplate: map[string]any{
					"apiVersion": "infrastructure.cluster.x-k8s.io/v1alpha1", "kind": kind + "MachineTemplate",
					"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"futureExtension": "preserve-me"}}},
				}}
			}
			tx := &launchLayerRecordingTx{}
			database := &testutil.MockDB{GetTx: func() db.Tx { return tx }}
			database.On("RunInTx", mock.Anything, mock.Anything).Return(nil).Once()
			srv, err := server.NewServer(config.Config{SupportedInfraProviders: []string{"infra"}}, discardLogger(), database, nil, nil)
			require.NoError(t, err)
			response := launchLayerHTTP(t, srv.Handler, http.MethodPost, "/api/clusters", body)
			require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
			var got api.Cluster
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &got))
			wantCluster, err := json.Marshal(body.Infra.Cluster)
			require.NoError(t, err)
			gotCluster, err := json.Marshal(got.Infra.Cluster)
			require.NoError(t, err)
			require.JSONEq(t, string(wantCluster), string(gotCluster))
			for _, pool := range tx.pools {
				require.Contains(t, string(pool.Capi.InfraMachineTemplate), "preserve-me")
			}
			database.AssertExpectations(t)
		})
	}
}

func TestLaunchLayerLaterWriteGatesAndImmutableModes(t *testing.T) {
	for _, operation := range []string{"createPool", "patchPool", "patchControlPlane"} {
		for _, scenario := range []string{"disabled", "typed-on-generic", "generic-on-typed"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				cluster := launchLayerStoredCluster(t)
				pool := launchLayerStoredPool(t, cluster.ID)
				if scenario == "typed-on-generic" {
					cluster.APIProvider = db.APIProviderInfra
					pool.ClusterAPIProvider = db.APIProviderInfra
				}
				enabled := []string{"launchlayer", "infra"}
				if scenario == "disabled" {
					enabled = []string{"infra"}
				}
				tx := &testutil.MockTx{}
				database := &testutil.MockDB{MockTx: tx}
				nodeIntent := any(launchLayerNodeIntent())
				providerKey := "launchlayer"
				if scenario == "generic-on-typed" {
					providerKey = "infra"
					nodeIntent = &api.InfraNodePoolProvider{MachineTemplate: launchLayerTemplate(), SpireAttestationMethod: "tpm"}
				}
				body := map[string]any{providerKey: nodeIntent}
				method, path := http.MethodPatch, "/api/nodePools/"+pool.ID.String()
				if operation == "createPool" {
					method, path = http.MethodPost, "/api/nodePools"
					body["clusterID"] = cluster.ID
					body["name"] = "later"
					body["nodeNamePrefix"] = "later"
					body["desiredNodeCount"] = 1
					database.On("GetCluster", mock.Anything, cluster.ID).Return(cluster, nil).Once()
				} else {
					database.On("RunInTx", mock.Anything, mock.Anything).Return(nil).Once()
					tx.On("GetClusterForUpdate", mock.Anything, cluster.ID).Return(cluster, nil).Once()
					if operation == "patchPool" {
						tx.On("GetNodePool", mock.Anything, pool.ID).Return(pool, nil).Once()
						tx.On("GetNodePoolForUpdate", mock.Anything, pool.ID).Return(pool, nil).Once()
						tx.On("ListNodePools", mock.Anything, mock.Anything).Return([]db.NodePool{pool}, nil).Maybe()
					} else {
						body = map[string]any{"controlPlaneConfig": body, "enableEtcdEncryption": true}
						path = "/api/clusters/" + cluster.ID.String()
					}
				}
				srv, err := server.NewServer(config.Config{SupportedInfraProviders: enabled, ProvisionControlPlaneWithControlPlaneController: operation == "patchControlPlane"}, discardLogger(), database, nil, nil)
				require.NoError(t, err)
				response := launchLayerHTTP(t, srv.Handler, method, path, body)
				require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
				tx.AssertNotCalled(t, "CreateNodePool", mock.Anything, mock.Anything)
				tx.AssertNotCalled(t, "UpdateNodePool", mock.Anything, mock.Anything)
				tx.AssertNotCalled(t, "CreateControlPlaneConfiguration", mock.Anything, mock.Anything)
				tx.AssertNotCalled(t, "CreateWorkflowOutboxEntry", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
				database.AssertExpectations(t)
				tx.AssertExpectations(t)
			})
		}
	}
}

func TestLaunchLayerReadSurfacesPreserveModeAndAllPreferences(t *testing.T) {
	cluster := launchLayerStoredCluster(t)
	pool := launchLayerStoredPool(t, cluster.ID)
	second := launchLayerTemplate()
	launchLayerSpec(second)["instanceType"] = "second-preference"
	raw, err := json.Marshal(second)
	require.NoError(t, err)
	cluster.ControlPlaneConfig.SetCapi(db.CAPIControlPlaneProvider{InfraMachineTemplatePreferences: []json.RawMessage{pool.Capi.InfraMachineTemplate, raw}, SpireAttestationMethod: db.SpireAttestationMethodJoinToken})
	cluster.ControlPlaneConfig.DesiredCount = 1
	set := db.NodeSet{ID: uuid.New(), NodePoolID: pool.ID, ClusterAPIProvider: db.APIProviderLaunchLayer}
	set.SetCapi(*pool.Capi)
	for _, legacy := range []bool{false, true} {
		c, p, ns := cluster, pool, set
		if legacy {
			c.APIProvider = ""
			p.ClusterAPIProvider = ""
			ns.ClusterAPIProvider = ""
		}
		database := &testutil.MockDB{}
		database.On("GetCluster", mock.Anything, c.ID).Return(c, nil)
		database.On("ListClusters", mock.Anything, mock.Anything).Return([]db.Cluster{c}, nil)
		database.On("GetNodePool", mock.Anything, p.ID).Return(p, nil)
		database.On("ListNodePools", mock.Anything, mock.Anything).Return([]db.NodePool{p}, nil)
		database.On("GetNodeSet", mock.Anything, ns.ID).Return(ns, nil)
		database.On("ListNodeSets", mock.Anything, mock.Anything).Return([]db.NodeSet{ns}, nil)
		database.On("ListNodes", mock.Anything, mock.Anything).Return([]db.Node{}, nil)
		handler := launchLayerHandler(t, database, true)
		for _, path := range []string{"/api/clusters", "/api/clusters/" + c.ID.String(), "/api/clusters/" + c.ID.String() + "/control-plane", "/api/nodePools", "/api/nodePools/" + p.ID.String(), "/api/nodeSets", "/api/nodeSets/" + ns.ID.String()} {
			response := launchLayerHTTP(t, handler, http.MethodGet, path, nil)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			if legacy {
				require.Contains(t, response.Body.String(), "\"infra\"")
				require.NotContains(t, response.Body.String(), "\"launchlayer\"")
			} else {
				require.Contains(t, response.Body.String(), "\"launchlayer\"")
				require.NotContains(t, response.Body.String(), "\"infra\"")
				if path == "/api/clusters/"+c.ID.String()+"/control-plane" {
					require.Contains(t, response.Body.String(), "second-preference")
					require.Contains(t, response.Body.String(), "machinePreferences")
				}
			}
		}
	}
}
