// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sitecluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	api "github.com/NVIDIA/nke/controller/api"
)

type fakeExpansionAPI struct {
	pools         []api.NodePool
	createRequest *api.CreateNodePoolJSONRequestBody
	createErr     error
	recordOnError bool
	nodeStates    [][]api.Node
	nodeListCalls int
	cluster       api.Cluster
}

func (f *fakeExpansionAPI) GetCluster(context.Context, openapi_types.UUID) (*api.Cluster, error) {
	return &f.cluster, nil
}

func (f *fakeExpansionAPI) CreateNodePool(
	_ context.Context,
	req api.CreateNodePoolJSONRequestBody,
) (*api.NodePool, error) {
	f.createRequest = &req
	pool := api.NodePool{
		ID:               uuid.New(),
		ClusterID:        req.ClusterID,
		Name:             req.Name,
		ControlPlane:     req.ControlPlane,
		DesiredNodeCount: req.DesiredNodeCount,
	}
	if f.createErr == nil || f.recordOnError {
		f.pools = []api.NodePool{pool}
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &pool, nil
}

func (f *fakeExpansionAPI) ListNodePools(
	context.Context,
	*api.ListNodePoolsParams,
) ([]api.NodePool, error) {
	return append([]api.NodePool(nil), f.pools...), nil
}

func (f *fakeExpansionAPI) ListNodes(context.Context, *api.ListNodesParams) ([]api.Node, error) {
	idx := f.nodeListCalls
	f.nodeListCalls++
	if idx >= len(f.nodeStates) {
		idx = len(f.nodeStates) - 1
	}
	return append([]api.Node(nil), f.nodeStates[idx]...), nil
}

func readyControlPlaneNodes(count int) []api.Node {
	nodes := make([]api.Node, count)
	for i := range nodes {
		nodes[i] = api.Node{
			Name:         "node",
			ControlPlane: true,
			Conditions: api.NodeConditions{
				KubernetesReady: condition(api.ConditionStatusTrue),
			},
		}
	}
	return nodes
}

func TestEnsureRemainingControlPlanePoolCreatesOnlyJoinNodes(t *testing.T) {
	client := &fakeExpansionAPI{}
	clusterID := uuid.New()
	pool, err := EnsureRemainingControlPlanePool(
		context.Background(), client, clusterID, testInventory(), quietLogger(),
	)
	if err != nil {
		t.Fatalf("EnsureRemainingControlPlanePool returned error: %v", err)
	}
	if pool == nil || pool.DesiredNodeCount != 2 {
		t.Fatalf("created pool = %#v, want two remaining nodes", pool)
	}
	request := client.createRequest
	if request == nil {
		t.Fatal("no create-node-pool request was made")
	}
	if !request.ControlPlane || request.Name != remainingControlPlanePoolName ||
		request.NodeNamePrefix != remainingControlPlaneNodeNamePrefix {
		t.Errorf("create request = %#v, want the dedicated control-plane join pool", request)
	}
	if request.CapiSpireAttestationMethod != api.SpireAttestationMethodCreateJoinToken {
		t.Errorf("attestation method = %q, want joinToken", request.CapiSpireAttestationMethod)
	}
}

func TestEnsureRemainingControlPlanePoolIsIdempotent(t *testing.T) {
	clusterID := uuid.New()
	client := &fakeExpansionAPI{pools: []api.NodePool{{
		ClusterID: clusterID, Name: remainingControlPlanePoolName,
		ControlPlane: true, DesiredNodeCount: 2,
	}}}
	pool, err := EnsureRemainingControlPlanePool(
		context.Background(), client, clusterID, testInventory(), quietLogger(),
	)
	if err != nil {
		t.Fatalf("EnsureRemainingControlPlanePool returned error: %v", err)
	}
	if pool == nil || client.createRequest != nil {
		t.Errorf("existing pool was not reused: pool=%#v create=%#v", pool, client.createRequest)
	}
}

func TestEnsureRemainingControlPlanePoolReconcilesLostCreateResponse(t *testing.T) {
	client := &fakeExpansionAPI{
		createErr:     errors.New("connection reset after request"),
		recordOnError: true,
	}
	pool, err := EnsureRemainingControlPlanePool(
		context.Background(), client, uuid.New(), testInventory(), quietLogger(),
	)
	if err != nil {
		t.Fatalf("committed create with a lost response returned error: %v", err)
	}
	if pool == nil || pool.DesiredNodeCount != 2 {
		t.Errorf("reconciled pool = %#v, want the committed pool", pool)
	}
}

func TestWaitControlPlaneReadyWaitsForEveryNode(t *testing.T) {
	shortPollInterval(t)
	client := &fakeExpansionAPI{
		cluster: clusterWith(api.ConditionStatusTrue, api.ConditionStatusFalse),
		nodeStates: [][]api.Node{
			readyControlPlaneNodes(1),
			readyControlPlaneNodes(2),
			readyControlPlaneNodes(3),
		},
	}
	if err := WaitControlPlaneReady(
		context.Background(), client, uuid.New(), 3, time.Second, quietLogger(),
	); err != nil {
		t.Fatalf("WaitControlPlaneReady returned error: %v", err)
	}
	if client.nodeListCalls != 3 {
		t.Errorf("ListNodes calls = %d, want 3", client.nodeListCalls)
	}
}
