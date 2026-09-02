// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sitecluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/NVIDIA/nke/cli/internal/clusterstate"
	"github.com/NVIDIA/nke/cli/internal/inventory"
	api "github.com/NVIDIA/nke/controller/api"
)

const (
	remainingControlPlanePoolName       = "control-plane-join"
	remainingControlPlaneNodeNamePrefix = "cpj"
)

// ExpansionAPI is the part of CWE used after the initial control plane is
// provisioned and its bootstrap identity route is ready.
type ExpansionAPI interface {
	GetCluster(ctx context.Context, id openapi_types.UUID) (*api.Cluster, error)
	CreateNodePool(ctx context.Context, req api.CreateNodePoolJSONRequestBody) (*api.NodePool, error)
	ListNodePools(ctx context.Context, params *api.ListNodePoolsParams) ([]api.NodePool, error)
	ListNodes(ctx context.Context, params *api.ListNodesParams) ([]api.Node, error)
}

// EnsureRemainingControlPlanePool creates the second control-plane node pool.
// It is idempotent by cluster and pool name so an interrupted bootstrap can be
// retried without creating duplicate Machines.
func EnsureRemainingControlPlanePool(
	ctx context.Context,
	client ExpansionAPI,
	clusterID openapi_types.UUID,
	inv *inventory.Inventory,
	log *slog.Logger,
) (*api.NodePool, error) {
	if inv == nil {
		return nil, fmt.Errorf("sitecluster: inventory is nil")
	}
	desired := len(inv.Cluster.JoinNodes)
	if desired == 0 {
		return nil, nil
	}

	existing, err := findRemainingControlPlanePool(ctx, client, clusterID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if err := validateRemainingControlPlanePool(existing, desired); err != nil {
			return nil, err
		}
		log.Info("remaining control-plane node pool already exists",
			"nodePool", existing.Name, "desiredNodes", desired)
		return existing, nil
	}

	req := api.CreateNodePoolJSONRequestBody{
		ClusterID:                  clusterID,
		Name:                       remainingControlPlanePoolName,
		NodeNamePrefix:             remainingControlPlaneNodeNamePrefix,
		DesiredNodeCount:           desired,
		ControlPlane:               true,
		CapiSpireAttestationMethod: api.SpireAttestationMethodCreateJoinToken,
		SystemReservedMemory:       inv.Cluster.KubeletConfig.SystemReservedMemory,
		CapiInfraMachineTemplate:   provisionedMachineTemplate(),
	}
	log.Info("creating the remaining control-plane node pool",
		"nodePool", req.Name, "desiredNodes", desired)
	created, createErr := client.CreateNodePool(ctx, req)
	if createErr == nil {
		return created, nil
	}

	// The server can commit the request and lose the response. Reconcile by the
	// globally unique cluster/name pair before treating that ambiguous result as
	// a failure.
	existing, findErr := findRemainingControlPlanePool(ctx, client, clusterID)
	if findErr == nil && existing != nil {
		if err := validateRemainingControlPlanePool(existing, desired); err != nil {
			return nil, fmt.Errorf("sitecluster: create of node pool %q returned an error and reconciliation found an incompatible pool: %w",
				req.Name, err)
		}
		log.Warn("node-pool create response failed, but CWE recorded the requested pool",
			"nodePool", existing.Name, "error", createErr)
		return existing, nil
	}
	return nil, fmt.Errorf("sitecluster: creating remaining control-plane node pool %q: %w", req.Name, createErr)
}

func findRemainingControlPlanePool(
	ctx context.Context,
	client ExpansionAPI,
	clusterID openapi_types.UUID,
) (*api.NodePool, error) {
	name := remainingControlPlanePoolName
	pools, err := client.ListNodePools(ctx, &api.ListNodePoolsParams{ClusterID: &clusterID, Name: &name})
	if err != nil {
		return nil, fmt.Errorf("sitecluster: finding node pool %q: %w", name, err)
	}
	switch len(pools) {
	case 0:
		return nil, nil
	case 1:
		return &pools[0], nil
	default:
		return nil, fmt.Errorf("sitecluster: %d node pools named %q exist for cluster %s", len(pools), name, clusterID)
	}
}

func validateRemainingControlPlanePool(pool *api.NodePool, desired int) error {
	if !pool.ControlPlane {
		return fmt.Errorf("sitecluster: existing node pool %q is not a control-plane pool", pool.Name)
	}
	if pool.DesiredNodeCount != desired {
		return fmt.Errorf("sitecluster: existing node pool %q desires %d nodes, inventory requires %d",
			pool.Name, pool.DesiredNodeCount, desired)
	}
	return nil
}

// WaitControlPlaneReady waits until CWE reports every requested control-plane
// node as Kubernetes-ready. Cluster Provisioned only covers the initial node.
func WaitControlPlaneReady(
	ctx context.Context,
	client ExpansionAPI,
	clusterID openapi_types.UUID,
	expected int,
	timeout time.Duration,
	log *slog.Logger,
) error {
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	controlPlane := true
	lastReady := -1
	var lastErr error
	for {
		nodes, err := client.ListNodes(ctx, &api.ListNodesParams{
			ClusterID:    &clusterID,
			ControlPlane: &controlPlane,
		})
		if err != nil {
			lastErr = err
		} else {
			ready := 0
			for i := range nodes {
				if nodes[i].ControlPlane && clusterstate.IsTrue(&nodes[i].Conditions.KubernetesReady) {
					ready++
				}
			}
			if ready != lastReady {
				log.Info("control-plane readiness", "readyNodes", ready, "expectedNodes", expected)
				lastReady = ready
			}
			if ready >= expected {
				return nil
			}
		}

		cluster, clusterErr := client.GetCluster(ctx, clusterID)
		if clusterErr == nil {
			if clusterstate.IsTrue(&cluster.Conditions.Deleting) {
				return fmt.Errorf("%w: cluster %q started deleting while control-plane nodes were joining",
					ErrClusterFailed, cluster.Name)
			}
			if clusterstate.IsTrue(cluster.Conditions.UpdateFailed) {
				return fmt.Errorf("%w: cluster %q reports updateFailed while control-plane nodes were joining: %s",
					ErrClusterFailed, cluster.Name, cluster.Conditions.UpdateFailed.Message)
			}
		} else if lastErr == nil {
			lastErr = clusterErr
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("sitecluster: %d/%d control-plane nodes became ready within %s: %w",
					lastReady, expected, timeout, lastErr)
			}
			return fmt.Errorf("sitecluster: %d/%d control-plane nodes became ready within %s",
				lastReady, expected, timeout)
		case <-time.After(pollInterval):
		}
	}
}
