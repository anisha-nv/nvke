// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package workflows

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/dsx-ai-factory/nvke/controller/internal/config"
	"github.com/dsx-ai-factory/nvke/controller/internal/constants"
	"github.com/dsx-ai-factory/nvke/controller/internal/db"
	"github.com/dsx-ai-factory/nvke/controller/internal/temporal/activities"
)

// labelNodeTestWorkflow is a thin wrapper so the unexported labelNode helper can
// be driven through the Temporal test environment.
func labelNodeTestWorkflow(ctx workflow.Context, nodeID uuid.UUID, addConfigProfileLabels bool) error {
	return labelNode(ctx, nodeID, addConfigProfileLabels)
}

// cleanBeforeControlPlaneRejoinTestWorkflow exposes the unexported cleanup
// helper to Temporal's deterministic workflow test environment.
func cleanBeforeControlPlaneRejoinTestWorkflow(ctx workflow.Context, nodeID uuid.UUID) error {
	cleanBeforeControlPlaneRejoin(ctx, nodeID)
	return nil
}

func provisionCAPINodeTestWorkflow(ctx workflow.Context, nodeID uuid.UUID) error {
	return provisionCAPINode(ctx, nodeID)
}

func provisionNodeRebootTestWorkflow(ctx workflow.Context, nodeID uuid.UUID, machineConfigurationEnabled bool) error {
	return provisionNode(ctx, ProvisionNodeInput{NodeID: nodeID}, nodeMetricLabels{}, machineConfigurationEnabled)
}

func TestUnit_ProvisionNodeProfileRebootOwnership(t *testing.T) {
	type parameters struct {
		machineConfigurationEnabled bool
		wantCWEReboot               bool
	}
	for name, params := range map[string]parameters{
		"MCC enabled skips CWE profile reboot": {machineConfigurationEnabled: true},
		"MCC disabled retains CWE reboot":      {wantCWEReboot: true},
	} {
		t.Run(name, func(t *testing.T) {
			nodeID := uuid.New()
			env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
			env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: 5 * time.Second})
			env.RegisterWorkflow(provisionNodeRebootTestWorkflow)
			var a *activities.Activities
			env.OnActivity(a.WaitForCAPIMachineReady, mock.Anything, mock.Anything).Return("", nil).Once()
			env.OnActivity(a.NodePreflight, mock.Anything, mock.Anything).Return(nil).Once()
			if !params.machineConfigurationEnabled {
				env.OnActivity(a.GetNodeConfigProfile, mock.Anything, mock.Anything).
					Return(activities.ConfigProfile{PreJoinNodeReboot: true}, nil).Once()
			}
			env.OnActivity(a.GetClusterForNode, mock.Anything, nodeID).Return(db.Cluster{}, nil).Once()
			rebootCalled := false
			if params.wantCWEReboot {
				env.OnWorkflow(RebootNodeWaitForUptime, mock.Anything, mock.Anything).
					Return(nil).
					Run(func(mock.Arguments) { rebootCalled = true }).Once()
			}
			env.OnActivity(a.UpdateNodeConditions, mock.Anything, mock.Anything).Return(nil).Once()

			env.ExecuteWorkflow(provisionNodeRebootTestWorkflow, nodeID, params.machineConfigurationEnabled)
			require.True(t, env.IsWorkflowCompleted())
			require.NoError(t, env.GetWorkflowError())
			require.Equal(t, params.wantCWEReboot, rebootCalled)
			env.AssertExpectations(t)
		})
	}
}

func joinProvisionedNodeProfileLabelsTestWorkflow(ctx workflow.Context, nodeID uuid.UUID, profileName string) error {
	return joinProvisionedNode(ctx, nodeID, activities.NodeProvisioningContext{ConfigProfile: profileName}, nodeMetricLabels{}, false, true)
}

func TestUnit_JoinProvisionedNodeProfileLabelsWithMachineConfigurationEnabled(t *testing.T) {
	type parameters struct {
		profileName string
		wantLabels  bool
	}
	for name, params := range map[string]parameters{
		"selected profile still applies Node labels": {profileName: "worker-profile", wantLabels: true},
		"empty profile does not apply Node labels":   {},
	} {
		t.Run(name, func(t *testing.T) {
			nodeID := uuid.New()
			node := db.Node{ID: nodeID, Name: "worker-0", Nico: &db.NicoNodeProvider{}, Conditions: db.NodeConditions{Joined: db.NewCondition(true, "Joined")}}
			cluster := db.Cluster{ProviderType: db.ProviderTypeNico, Nico: &db.NicoClusterProvider{}}
			env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
			env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: 5 * time.Second})
			env.RegisterWorkflow(joinProvisionedNodeProfileLabelsTestWorkflow)
			var a *activities.Activities
			env.OnActivity(a.GetNode, mock.Anything, nodeID).Return(node, nil).Twice()
			env.OnActivity(a.GetClusterForNode, mock.Anything, nodeID).Return(cluster, nil).Once()
			labelCalled := false
			if params.wantLabels {
				env.OnActivity(a.LabelNode, mock.Anything, mock.Anything).
					Return(activities.LabelNodeResult{}, nil).
					Run(func(args mock.Arguments) {
						labelCalled = true
						input := args.Get(1).(activities.LabelNodeInput)
						require.True(t, input.AddConfigProfileLabels)
					}).Once()
			}
			env.OnActivity(a.ConfigureKubeVip, mock.Anything, mock.Anything).Return(activities.ConfigureKubeVipResult{}, nil).Once()
			env.OnActivity(a.TaintNode, mock.Anything, mock.Anything).Return(activities.TaintNodeResult{}, nil).Once()
			env.OnActivity(a.UpdateNodeConditions, mock.Anything, mock.Anything).Return(nil).Once()

			env.ExecuteWorkflow(joinProvisionedNodeProfileLabelsTestWorkflow, nodeID, params.profileName)
			require.True(t, env.IsWorkflowCompleted())
			require.NoError(t, env.GetWorkflowError())
			require.Equal(t, params.wantLabels, labelCalled)
			env.AssertExpectations(t)
		})
	}
}

// setupLabelNodeEnv builds a focused test env that registers the real DB
// activities labelNode needs and a capturing mock for LabelNode.
func setupLabelNodeEnv(t *testing.T, database db.DB, profilesDir string) (
	env *testsuite.TestWorkflowEnvironment,
	captured *activities.LabelNodeInput,
	called *bool,
) {
	t.Helper()

	testSuite := &testsuite.WorkflowTestSuite{}
	env = testSuite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: 5 * time.Second})

	a := activities.NewActivities(config.Config{ProfilesDir: profilesDir}, database, nil, nil, "", nil, "")

	env.RegisterActivity(a.GetNode)
	env.RegisterActivity(a.GetClusterForNode)
	env.RegisterWorkflow(labelNodeTestWorkflow)

	captured = &activities.LabelNodeInput{}
	wasCalled := false
	called = &wasCalled
	env.OnActivity(a.LabelNode, mock.Anything, mock.Anything).
		Return(activities.LabelNodeResult{}, nil).
		Run(func(args mock.Arguments) {
			*called = true
			*captured = args.Get(1).(activities.LabelNodeInput)
		}).Maybe()

	return env, captured, called
}

func TestLabelNode(t *testing.T) {
	type labelNodeEnv struct {
		env      *testsuite.TestWorkflowEnvironment
		captured *activities.LabelNodeInput
		called   *bool
		nodeID   uuid.UUID
	}
	type parameters struct {
		setup          func(t *testing.T) labelNodeEnv
		wantCalled     bool
		wantLabels     []string
		unwantedLabels []string
	}

	tests := map[string]parameters{
		"adds machine ID label": {
			setup: func(t *testing.T) labelNodeEnv {
				t.Helper()

				database, res := setupTestDB(t, seedOpts{forgeMachineID: "machine-abc123"})
				env, captured, called := setupLabelNodeEnv(t, database, res.profilesDir)
				return labelNodeEnv{env: env, captured: captured, called: called, nodeID: res.nodeID}
			},
			wantCalled: true,
			wantLabels: []string{constants.LabelKeyMachineID + "=machine-abc123"},
		},
		"adds machine ID label from nico node provider": {
			setup: func(t *testing.T) labelNodeEnv {
				t.Helper()

				nodeID := uuid.MustParse("50000000-0000-0000-0000-000000000001")
				clusterID := uuid.MustParse("50000000-0000-0000-0000-000000000002")
				siteID := uuid.MustParse("50000000-0000-0000-0000-000000000003")
				vpcID := uuid.MustParse("50000000-0000-0000-0000-000000000004")
				node := db.Node{
					ID:        nodeID,
					ClusterID: clusterID,
					Name:      "worker-0",
					Nico: &db.NicoNodeProvider{
						ForgeMachineID: "payload-machine",
					},
				}
				cluster := db.Cluster{
					ID:           clusterID,
					ProviderType: db.ProviderTypeNico,
					Nico: &db.NicoClusterProvider{
						OrgID:  "org-1",
						SiteID: siteID,
						VpcID:  vpcID,
					},
				}
				testSuite := &testsuite.WorkflowTestSuite{}
				env := testSuite.NewTestWorkflowEnvironment()
				env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: 5 * time.Second})
				env.RegisterWorkflow(labelNodeTestWorkflow)

				var a *activities.Activities
				captured := &activities.LabelNodeInput{}
				wasCalled := false
				env.OnActivity(a.GetClusterForNode, mock.Anything, nodeID).Return(cluster, nil)
				env.OnActivity(a.GetNode, mock.Anything, nodeID).Return(node, nil)
				env.OnActivity(a.LabelNode, mock.Anything, mock.Anything).
					Return(activities.LabelNodeResult{}, nil).
					Run(func(args mock.Arguments) {
						wasCalled = true
						*captured = args.Get(1).(activities.LabelNodeInput)
					})

				return labelNodeEnv{env: env, captured: captured, called: &wasCalled, nodeID: nodeID}
			},
			wantCalled:     true,
			wantLabels:     []string{constants.LabelKeyMachineID + "=payload-machine"},
			unwantedLabels: []string{constants.LabelKeyMachineID + "=legacy-machine"},
		},
		"skips machine ID label when empty": {
			setup: func(t *testing.T) labelNodeEnv {
				t.Helper()

				database, res := setupTestDB(t, seedOpts{forgeMachineID: ""})
				env, captured, called := setupLabelNodeEnv(t, database, res.profilesDir)
				return labelNodeEnv{env: env, captured: captured, called: called, nodeID: res.nodeID}
			},
			wantCalled: false,
		},
	}

	for name, params := range tests {
		t.Run(name, func(t *testing.T) {
			testEnv := params.setup(t)

			testEnv.env.ExecuteWorkflow(labelNodeTestWorkflow, testEnv.nodeID, false)

			require.True(t, testEnv.env.IsWorkflowCompleted())
			require.NoError(t, testEnv.env.GetWorkflowError())
			require.Equal(t, params.wantCalled, *testEnv.called)
			for _, label := range params.wantLabels {
				require.Contains(t, testEnv.captured.Labels, label)
			}
			for _, label := range params.unwantedLabels {
				require.NotContains(t, testEnv.captured.Labels, label)
			}
		})
	}
}

func TestCleanBeforeControlPlaneRejoinUsesTypedEtcdRemoval(t *testing.T) {
	nodeID := uuid.MustParse("50000000-0000-0000-0000-000000000011")
	clusterID := uuid.MustParse("50000000-0000-0000-0000-000000000012")
	node := db.Node{
		ID:           nodeID,
		ClusterID:    clusterID,
		Name:         "control-plane-1",
		ControlPlane: true,
	}

	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: 5 * time.Second})
	env.RegisterWorkflow(cleanBeforeControlPlaneRejoinTestWorkflow)

	var a *activities.Activities
	env.OnActivity(a.GetNode, mock.Anything, nodeID).Return(node, nil).Once()
	env.OnActivity(a.RemoveEtcdMember, mock.Anything, mock.MatchedBy(func(input activities.EtcdMemberRemovalInput) bool {
		return input.ClusterID == clusterID &&
			input.TargetNodeID == nodeID &&
			input.TargetMemberName == node.Name
	})).Return(nil).Once()
	env.OnActivity(a.KubeadmReset, mock.Anything, mock.MatchedBy(func(input activities.KubeadmResetInput) bool {
		return input.NodeID == nodeID &&
			len(input.SkipPhases) == 1 &&
			input.SkipPhases[0] == removeEtcdMember
	})).Return(activities.KubeadmResetResult{}, nil).Once()

	env.ExecuteWorkflow(cleanBeforeControlPlaneRejoinTestWorkflow, nodeID)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

func TestProvisionCAPINodeClaimsNodeBeforeCreatingInfraMachine(t *testing.T) {
	nodeID := uuid.MustParse("50000000-0000-0000-0000-000000000021")

	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: 5 * time.Second})
	env.RegisterWorkflow(provisionCAPINodeTestWorkflow)

	var a *activities.Activities
	env.OnActivity(a.UpdateNodeConditions, mock.Anything, mock.MatchedBy(func(update activities.NodeConditionsUpdate) bool {
		return update.NodeID == nodeID &&
			update.Conditions.Pending != nil &&
			update.Conditions.Pending.Status == db.ConditionStatusTrue &&
			update.Conditions.ProvisioningClaimed != nil &&
			update.Conditions.ProvisioningClaimed.Status == db.ConditionStatusTrue &&
			update.Conditions.Provisioning != nil &&
			update.Conditions.Provisioning.Status == db.ConditionStatusFalse
	})).Return(nil).Once()
	env.OnActivity(a.CreateCAPIInfraMachineForNode, mock.Anything, activities.CreateCAPIInfraMachineForNodeInput{
		NodeID: nodeID,
	}).Return(nil).Once()

	env.ExecuteWorkflow(provisionCAPINodeTestWorkflow, nodeID)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}
