// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/nke/controller/api"
	"github.com/dsx-ai-factory/nke/controller/internal/db"
)

func selfRegisterTestCluster(id, configurationID uuid.UUID) api.Cluster {
	return api.Cluster{
		ID:                            id,
		Name:                          "dev-local",
		ControlPlaneKubernetesVersion: "1.34.8",
		CniPlugin:                     "calico",
		CniVersion:                    "3.30.1",
		EtcdEncrypted:                 true,
		CreatedAt:                     time.Now(),
		Conditions: api.ClusterConditions{
			Provisioning: api.Condition{Status: api.ConditionStatusFalse},
			Provisioned:  api.Condition{Status: api.ConditionStatusTrue},
			Deleting:     api.Condition{Status: api.ConditionStatusFalse},
		},
		Network: api.ClusterNetwork{
			PodCIDR:     "192.168.0.0/16",
			ServiceCIDR: "10.96.0.0/12",
		},
		Infra: &api.InfraClusterProvider{
			Cluster: map[string]interface{}{
				"apiVersion": "infrastructure.cluster.x-k8s.io/v1alpha1",
				"kind":       "ProvisionedCluster",
			},
		},
		ControlPlaneConfig: &api.ControlPlaneConfig{
			ConfigurationID:      configurationID,
			DesiredCount:         1,
			SystemReservedMemory: "512Mi",
			Infra: &api.InfraControlPlaneProvider{
				MachineTemplate: map[string]interface{}{
					"apiVersion": "infrastructure.cluster.x-k8s.io/v1alpha1",
					"kind":       "ProvisionedMachineTemplate",
				},
			},
		},
	}
}

func selfRegisterTestNode(id, configurationID uuid.UUID) api.Node {
	return api.Node{
		ID:           id,
		NodePoolID:   uuid.Nil,
		NodeSetID:    uuid.Nil,
		Name:         "dev-local-cp-1",
		IP:           "10.4.1.10",
		ControlPlane: true,
		Conditions: api.NodeConditions{
			Provisioned:     api.Condition{Status: api.ConditionStatusTrue},
			KubernetesReady: api.Condition{Status: api.ConditionStatusTrue},
		},
		ControlPlaneRollout: &api.ControlPlaneRolloutStatus{
			PinnedConfigurationID: configurationID,
		},
	}
}

func TestSelfRegisterCluster_HappyPath(t *testing.T) {
	clusterID := uuid.New()
	nodeID := uuid.New()
	configID := uuid.New()

	mockDB := &mockDB{}
	mockDB.MockTx = &mockTx{}
	mockDB.GetTx = func() db.Tx { return mockDB.MockTx }
	mockDB.On("RunInTx", mock.Anything, mock.Anything).Return(nil)

	mockDB.MockTx.On("ListClusters", mock.Anything, db.ClusterFilters{}).Return([]db.Cluster{}, nil).Once()
	mockDB.MockTx.On("CreateCluster", mock.Anything, mock.MatchedBy(func(c *db.Cluster) bool {
		return c.ID == clusterID && c.Name == "dev-local" && c.ControlPlaneConfig.ConfigurationID == uuid.Nil
	})).Return(db.Cluster{ID: clusterID}, nil).Once()
	mockDB.MockTx.On("CreateControlPlaneConfiguration", mock.Anything, mock.MatchedBy(func(c *db.ControlPlaneConfiguration) bool {
		return c.ID == configID && c.ClusterID == clusterID
	})).Return(db.ControlPlaneConfiguration{ID: configID, ClusterID: clusterID}, nil).Once()
	mockDB.MockTx.On("UpdateCluster", mock.Anything, mock.MatchedBy(func(c *db.Cluster) bool {
		return c.ID == clusterID && c.ControlPlaneConfig.ConfigurationID == configID
	})).Return(db.Cluster{ID: clusterID}, nil).Once()
	mockDB.MockTx.On("CreateNode", mock.Anything, mock.MatchedBy(func(n *db.Node) bool {
		return n.ID == nodeID && n.ClusterID == clusterID && n.ControlPlane
	})).Return(db.Node{ID: nodeID}, nil).Once()
	mockDB.MockTx.On("CreateWorkflowOutboxEntry", mock.Anything, "SelfRegisterCluster", mock.Anything, mock.Anything).
		Return(db.WorkflowOutbox{}, nil).Once()
	mockDB.MockTx.On("GetCluster", mock.Anything, clusterID).Return(db.Cluster{
		ID:   clusterID,
		Name: "dev-local",
	}, nil).Once()

	svc := newTestClusterService(discardLogger(), mockDB)
	resp, err := svc.SelfRegisterCluster(context.Background(), api.SelfRegisterClusterRequestObject{
		Body: &api.SelfRegisterClusterRequest{
			Cluster: selfRegisterTestCluster(clusterID, configID),
			Nodes:   []api.Node{selfRegisterTestNode(nodeID, configID)},
		},
	})
	require.NoError(t, err)

	body, ok := resp.(api.SelfRegisterCluster201JSONResponse)
	require.True(t, ok, "expected SelfRegisterCluster201JSONResponse, got %T", resp)
	require.Equal(t, clusterID, body.ID)
	require.Equal(t, "dev-local", body.Name)
	mockDB.MockTx.AssertExpectations(t)
}

func TestSelfRegisterCluster_RejectsSecondCluster(t *testing.T) {
	existingID := uuid.New()
	requestID := uuid.New()

	mockDB := &mockDB{}
	mockDB.MockTx = &mockTx{}
	mockDB.GetTx = func() db.Tx { return mockDB.MockTx }
	mockDB.On("RunInTx", mock.Anything, mock.Anything).Return(nil)

	mockDB.MockTx.On("ListClusters", mock.Anything, db.ClusterFilters{}).
		Return([]db.Cluster{{ID: existingID}}, nil).Once()

	svc := newTestClusterService(discardLogger(), mockDB)
	resp, err := svc.SelfRegisterCluster(context.Background(), api.SelfRegisterClusterRequestObject{
		Body: &api.SelfRegisterClusterRequest{
			Cluster: selfRegisterTestCluster(requestID, uuid.New()),
			Nodes:   nil,
		},
	})
	require.NoError(t, err)

	body, ok := resp.(api.SelfRegisterCluster409JSONResponse)
	require.True(t, ok, "expected SelfRegisterCluster409JSONResponse, got %T", resp)
	require.Contains(t, body.Message, existingID.String())

	mockDB.MockTx.AssertNotCalled(t, "CreateCluster", mock.Anything, mock.Anything)
}

func TestSelfRegisterCluster_SameIDRetryIsIdempotent(t *testing.T) {
	clusterID := uuid.New()

	mockDB := &mockDB{}
	mockDB.MockTx = &mockTx{}
	mockDB.GetTx = func() db.Tx { return mockDB.MockTx }
	mockDB.On("RunInTx", mock.Anything, mock.Anything).Return(nil)

	mockDB.MockTx.On("ListClusters", mock.Anything, db.ClusterFilters{}).
		Return([]db.Cluster{{ID: clusterID}}, nil).Once()
	mockDB.MockTx.On("GetCluster", mock.Anything, clusterID).
		Return(db.Cluster{ID: clusterID, Name: "dev-local"}, nil).Once()

	svc := newTestClusterService(discardLogger(), mockDB)
	resp, err := svc.SelfRegisterCluster(context.Background(), api.SelfRegisterClusterRequestObject{
		Body: &api.SelfRegisterClusterRequest{
			Cluster: selfRegisterTestCluster(clusterID, uuid.New()),
			Nodes:   []api.Node{selfRegisterTestNode(uuid.New(), uuid.New())},
		},
	})
	require.NoError(t, err)

	body, ok := resp.(api.SelfRegisterCluster201JSONResponse)
	require.True(t, ok, "expected SelfRegisterCluster201JSONResponse, got %T", resp)
	require.Equal(t, clusterID, body.ID)

	mockDB.MockTx.AssertNotCalled(t, "CreateCluster", mock.Anything, mock.Anything)
	mockDB.MockTx.AssertNotCalled(t, "CreateControlPlaneConfiguration", mock.Anything, mock.Anything)
	mockDB.MockTx.AssertNotCalled(t, "CreateNode", mock.Anything, mock.Anything)
	mockDB.MockTx.AssertNotCalled(t, "CreateWorkflowOutboxEntry", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestSelfRegisterCluster_PreservesControlPlaneConfigFields(t *testing.T) {
	clusterID := uuid.New()
	nodeID := uuid.New()
	configID := uuid.New()

	cluster := selfRegisterTestCluster(clusterID, configID)
	teleportEnabled := true
	provisioningMonitorDisabled := true
	cluster.ControlPlaneConfig.NodeLabels = []string{"role=cp"}
	cluster.ControlPlaneConfig.TeleportEnabled = &teleportEnabled
	cluster.ControlPlaneConfig.ProvisioningMonitorDisabled = &provisioningMonitorDisabled
	cluster.ControlPlaneConfig.Infra.BootstrapFiles = []api.CloudInitFile{
		{Path: "/etc/example.conf", Content: "hello"},
	}
	cluster.ControlPlaneConfig.Infra.PreKubeadmCommands = []string{"echo hi"}

	mockDB := &mockDB{}
	mockDB.MockTx = &mockTx{}
	mockDB.GetTx = func() db.Tx { return mockDB.MockTx }
	mockDB.On("RunInTx", mock.Anything, mock.Anything).Return(nil)

	mockDB.MockTx.On("ListClusters", mock.Anything, db.ClusterFilters{}).Return([]db.Cluster{}, nil).Once()
	mockDB.MockTx.On("CreateCluster", mock.Anything, mock.Anything).Return(db.Cluster{ID: clusterID}, nil).Once()
	mockDB.MockTx.On("CreateControlPlaneConfiguration", mock.Anything, mock.MatchedBy(func(c *db.ControlPlaneConfiguration) bool {
		return c.Config.NodeLabels[0] == "role=cp" &&
			c.Config.TeleportEnabled &&
			c.Config.ProvisioningMonitorDisabled &&
			c.Config.Capi != nil &&
			len(c.Config.Capi.BootstrapFiles) == 1 &&
			c.Config.Capi.BootstrapFiles[0].Path == "/etc/example.conf" &&
			len(c.Config.Capi.PreKubeadmCommands) == 1 &&
			c.Config.Capi.PreKubeadmCommands[0] == "echo hi"
	})).Return(db.ControlPlaneConfiguration{ID: configID, ClusterID: clusterID}, nil).Once()
	mockDB.MockTx.On("UpdateCluster", mock.Anything, mock.Anything).Return(db.Cluster{ID: clusterID}, nil).Once()
	mockDB.MockTx.On("CreateNode", mock.Anything, mock.Anything).Return(db.Node{ID: nodeID}, nil).Once()
	mockDB.MockTx.On("CreateWorkflowOutboxEntry", mock.Anything, "SelfRegisterCluster", mock.Anything, mock.Anything).
		Return(db.WorkflowOutbox{}, nil).Once()
	mockDB.MockTx.On("GetCluster", mock.Anything, clusterID).Return(db.Cluster{ID: clusterID}, nil).Once()

	svc := newTestClusterService(discardLogger(), mockDB)
	_, err := svc.SelfRegisterCluster(context.Background(), api.SelfRegisterClusterRequestObject{
		Body: &api.SelfRegisterClusterRequest{
			Cluster: cluster,
			Nodes:   []api.Node{selfRegisterTestNode(nodeID, configID)},
		},
	})
	require.NoError(t, err)
	mockDB.MockTx.AssertExpectations(t)
}

func TestSelfRegisterCluster_RejectsMissingClusterID(t *testing.T) {
	mockDB := &mockDB{}
	svc := newTestClusterService(discardLogger(), mockDB)

	cluster := selfRegisterTestCluster(uuid.New(), uuid.New())
	cluster.ID = uuid.Nil
	resp, err := svc.SelfRegisterCluster(context.Background(), api.SelfRegisterClusterRequestObject{
		Body: &api.SelfRegisterClusterRequest{Cluster: cluster},
	})
	require.NoError(t, err)

	body, ok := resp.(api.SelfRegisterCluster400JSONResponse)
	require.True(t, ok, "expected SelfRegisterCluster400JSONResponse, got %T", resp)
	require.Contains(t, body.Message, "cluster.id is required")
	mockDB.AssertNotCalled(t, "RunInTx", mock.Anything, mock.Anything)
}

func TestSelfRegisterCluster_RejectsMissingProvider(t *testing.T) {
	mockDB := &mockDB{}
	svc := newTestClusterService(discardLogger(), mockDB)

	cluster := selfRegisterTestCluster(uuid.New(), uuid.New())
	cluster.Infra = nil
	resp, err := svc.SelfRegisterCluster(context.Background(), api.SelfRegisterClusterRequestObject{
		Body: &api.SelfRegisterClusterRequest{Cluster: cluster},
	})
	require.NoError(t, err)

	body, ok := resp.(api.SelfRegisterCluster400JSONResponse)
	require.True(t, ok, "expected SelfRegisterCluster400JSONResponse, got %T", resp)
	require.Contains(t, body.Message, "CAPI-backed")
	mockDB.AssertNotCalled(t, "RunInTx", mock.Anything, mock.Anything)
}

func TestSelfRegisterCluster_RejectsNicoProvider(t *testing.T) {
	mockDB := &mockDB{}
	svc := newTestClusterService(discardLogger(), mockDB)

	cluster := selfRegisterTestCluster(uuid.New(), uuid.New())
	cluster.Infra = nil
	cluster.Nico = &api.NicoClusterProvider{OrgID: "org-1"}
	resp, err := svc.SelfRegisterCluster(context.Background(), api.SelfRegisterClusterRequestObject{
		Body: &api.SelfRegisterClusterRequest{Cluster: cluster},
	})
	require.NoError(t, err)

	body, ok := resp.(api.SelfRegisterCluster400JSONResponse)
	require.True(t, ok, "expected SelfRegisterCluster400JSONResponse, got %T", resp)
	require.Contains(t, body.Message, "CAPI-backed")
	mockDB.AssertNotCalled(t, "RunInTx", mock.Anything, mock.Anything)
}

func TestSelfRegisterCluster_RejectsNodePoolManagedNode(t *testing.T) {
	mockDB := &mockDB{}
	svc := newTestClusterService(discardLogger(), mockDB)

	clusterID := uuid.New()
	configID := uuid.New()
	node := selfRegisterTestNode(uuid.New(), configID)
	node.NodePoolID = uuid.New()

	resp, err := svc.SelfRegisterCluster(context.Background(), api.SelfRegisterClusterRequestObject{
		Body: &api.SelfRegisterClusterRequest{
			Cluster: selfRegisterTestCluster(clusterID, configID),
			Nodes:   []api.Node{node},
		},
	})
	require.NoError(t, err)

	body, ok := resp.(api.SelfRegisterCluster400JSONResponse)
	require.True(t, ok, "expected SelfRegisterCluster400JSONResponse, got %T", resp)
	require.Contains(t, body.Message, "node-pool-managed")
	mockDB.AssertNotCalled(t, "RunInTx", mock.Anything, mock.Anything)
}

func TestSelfRegisterCluster_RejectsNilBody(t *testing.T) {
	mockDB := &mockDB{}
	svc := newTestClusterService(discardLogger(), mockDB)

	resp, err := svc.SelfRegisterCluster(context.Background(), api.SelfRegisterClusterRequestObject{Body: nil})
	require.NoError(t, err)

	body, ok := resp.(api.SelfRegisterCluster400JSONResponse)
	require.True(t, ok, "expected SelfRegisterCluster400JSONResponse, got %T", resp)
	require.Contains(t, body.Message, "request body is required")
	mockDB.AssertNotCalled(t, "RunInTx", mock.Anything, mock.Anything)
}
