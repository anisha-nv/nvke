// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package workflows

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/dsx-ai-factory/nke/controller/internal/db"
	"github.com/dsx-ai-factory/nke/controller/internal/temporal/activities"
)

func TestSelfRegisterClusterAppliesClusterResourcesThenEachNode(t *testing.T) {
	clusterID := uuid.New()
	node1 := uuid.New()
	node2 := uuid.New()

	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestWorkflowEnvironment()
	var a *activities.Activities

	env.OnActivity(a.ApplyCAPIClusterResources, mock.Anything, activities.ApplyCAPIClusterResourcesInput{
		ClusterID: clusterID,
	}).Return(nil).Once()

	env.OnActivity(a.ListNodes, mock.Anything, db.NodeFilters{ClusterID: &clusterID}).
		Return([]db.Node{{ID: node1}, {ID: node2}}, nil).Once()

	env.OnActivity(a.CreateCAPIInfraMachineForNode, mock.Anything, activities.CreateCAPIInfraMachineForNodeInput{
		NodeID: node1,
	}).Return(nil).Once()
	env.OnActivity(a.CreateCAPIInfraMachineForNode, mock.Anything, activities.CreateCAPIInfraMachineForNodeInput{
		NodeID: node2,
	}).Return(nil).Once()

	env.ExecuteWorkflow(SelfRegisterCluster, SelfRegisterClusterInput{ClusterID: clusterID})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

func TestSelfRegisterClusterFailsIfClusterResourcesFail(t *testing.T) {
	clusterID := uuid.New()

	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestWorkflowEnvironment()
	var a *activities.Activities

	// No .Once(): the activity's own retry policy (MaximumAttempts: 5) replays
	// it against this mock several times before the workflow gives up, same as
	// it would against the real activity.
	env.OnActivity(a.ApplyCAPIClusterResources, mock.Anything, activities.ApplyCAPIClusterResourcesInput{
		ClusterID: clusterID,
	}).Return(errors.New("boom"))

	env.ExecuteWorkflow(SelfRegisterCluster, SelfRegisterClusterInput{ClusterID: clusterID})

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	// ListNodes/CreateCAPIInfraMachineForNode must never run after
	// ApplyCAPIClusterResources fails -- AssertExpectations fails the test if
	// an OnActivity mock we didn't register above was called.
	env.AssertExpectations(t)
}

func TestSelfRegisterClusterFailsIfANodeFails(t *testing.T) {
	clusterID := uuid.New()
	node1 := uuid.New()

	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestWorkflowEnvironment()
	var a *activities.Activities

	env.OnActivity(a.ApplyCAPIClusterResources, mock.Anything, activities.ApplyCAPIClusterResourcesInput{
		ClusterID: clusterID,
	}).Return(nil).Once()
	env.OnActivity(a.ListNodes, mock.Anything, db.NodeFilters{ClusterID: &clusterID}).
		Return([]db.Node{{ID: node1}}, nil).Once()
	// No .Once(): see the comment in TestSelfRegisterClusterFailsIfClusterResourcesFail.
	env.OnActivity(a.CreateCAPIInfraMachineForNode, mock.Anything, activities.CreateCAPIInfraMachineForNodeInput{
		NodeID: node1,
	}).Return(errors.New("boom"))

	env.ExecuteWorkflow(SelfRegisterCluster, SelfRegisterClusterInput{ClusterID: clusterID})

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}
