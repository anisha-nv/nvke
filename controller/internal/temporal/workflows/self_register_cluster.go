// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package workflows

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/dsx-ai-factory/nke/controller/internal/db"
	"github.com/dsx-ai-factory/nke/controller/internal/temporal/activities"
)

type SelfRegisterClusterInput struct {
	ClusterID uuid.UUID `json:"clusterID"`
}

// SelfRegisterCluster brings the live CAPI objects (Cluster, ProvisionedCluster,
// Machine, ProvisionedMachine) for an already-provisioned cluster back into
// existence on the Kubernetes API this CWE talks to, without touching the real
// nodes. It exists for the self-registration case: a cluster's DB rows (already
// marked Provisioned) were imported from another CWE instance, but the live K8s
// objects only ever existed on that instance's management cluster.
//
// Unlike ProvisionCluster/ProvisionNode, this workflow never runs
// NodePreflight, join hooks, CNI prerequisite configuration, or a node reboot:
// it only calls the two activities that create CAPI objects, both of which are
// server-side-apply patches and therefore safe no-ops if the objects already
// exist.
func SelfRegisterCluster(ctx workflow.Context, input SelfRegisterClusterInput) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("Self-registering cluster", "clusterID", input.ClusterID)

	var a *activities.Activities

	activityOptions := workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 10 * time.Second,
			MaximumInterval: 1 * time.Minute,
			MaximumAttempts: 5,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, activityOptions)

	if err := workflow.ExecuteActivity(ctx, a.ApplyCAPIClusterResources, activities.ApplyCAPIClusterResourcesInput{
		ClusterID: input.ClusterID,
	}).Get(ctx, nil); err != nil {
		return fmt.Errorf("failed to apply CAPI cluster resources: %w", err)
	}

	clusterID := input.ClusterID
	var nodes []db.Node
	if err := workflow.ExecuteActivity(ctx, a.ListNodes, db.NodeFilters{
		ClusterID: &clusterID,
	}).Get(ctx, &nodes); err != nil {
		return fmt.Errorf("failed to list cluster nodes: %w", err)
	}

	for _, node := range nodes {
		if err := workflow.ExecuteActivity(ctx, a.CreateCAPIInfraMachineForNode, activities.CreateCAPIInfraMachineForNodeInput{
			NodeID: node.ID,
		}).Get(ctx, nil); err != nil {
			return fmt.Errorf("failed to create CAPI infra machine for node %s: %w", node.ID, err)
		}
	}

	logger.Info("Self-registration complete", "clusterID", input.ClusterID, "nodeCount", len(nodes))
	return nil
}
