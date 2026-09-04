// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nke/controller/api"
	"github.com/NVIDIA/nke/controller/internal/db"
	"github.com/NVIDIA/nke/controller/internal/testutil"
)

// Scenario 2: a legacy cluster (control-plane node pool, no cluster-level
// control-plane config) that patches only cluster-level maxSurge must stay
// legacy and must not silently convert to control-plane-controller mode.
func TestScenario2_LegacyMaxSurgeStaysLegacy(t *testing.T) {
	ctx := context.Background()
	// Allow-gate on: even with the feature enabled, a legacy maxSurge PATCH must
	// stay legacy (the gate permits opt-in; it must not force conversion).
	svc := clusterService{controlPlaneControllerAllowed: true}

	cluster := &db.Cluster{
		ID:                uuid.New(),
		SiteID:            uuid.New(),
		KubernetesVersion: "1.29.0",
	}
	require.False(t, cluster.UsesControlPlaneController(), "precondition: cluster starts legacy")

	mockTx := &testutil.MockTx{}
	// Permit (but do not require) a config-row insert so we observe the resulting
	// mode instead of panicking on an unexpected mock call.
	mockTx.On("CreateControlPlaneConfiguration", mock.Anything, mock.AnythingOfType("*db.ControlPlaneConfiguration")).
		Return(db.ControlPlaneConfiguration{ID: uuid.New()}, nil).Maybe()

	maxSurge := 1
	err := svc.applyClusterControlPlaneConfigUpdate(ctx, mockTx, cluster, &api.UpdateClusterControlPlaneConfig{MaxSurge: &maxSurge})
	require.NoError(t, err)
	require.Equal(t, 1, cluster.ControlPlaneConfig.MaxSurge)
	require.False(t, cluster.UsesControlPlaneController(),
		"patching cluster-level maxSurge on a legacy cluster must not convert it to controller mode")
	mockTx.AssertNotCalled(t, "CreateControlPlaneConfiguration", mock.Anything, mock.Anything)
}

// Scenario 3: once a cluster has a control-plane node pool (legacy), a PATCH
// carrying an otherwise-valid cluster-level controlPlaneConfig must not silently
// convert it to controller mode; conversion requires an explicit migration.
func TestScenario3_LegacyWithCPNodePoolRejectsControlPlaneConfig(t *testing.T) {
	ctx := context.Background()
	// Allow-gate on so the request reaches the migration guard rather than the
	// feature-disabled gate; conversion is still refused because a CP pool exists.
	svc := clusterService{controlPlaneControllerAllowed: true}
	clusterID := uuid.New()
	cluster := &db.Cluster{ID: clusterID, SiteID: uuid.New(), KubernetesVersion: "1.29.0"}

	mockTx := &testutil.MockTx{}
	mockTx.On("ListNodePools", mock.Anything, db.NodePoolFilters{ClusterID: &clusterID}).
		Return([]db.NodePool{{ClusterID: clusterID, ControlPlane: true, DesiredCount: 3}}, nil)
	// If the guard is missing, these would let the silent conversion proceed.
	mockTx.On("CreateControlPlaneConfiguration", mock.Anything, mock.AnythingOfType("*db.ControlPlaneConfiguration")).
		Return(db.ControlPlaneConfiguration{ID: uuid.New()}, nil).Maybe()

	// desiredCount is a cluster-level control-plane field, so this is a migration
	// request. The guard fires before any field validation, so the request need
	// not be otherwise complete.
	desired := 3
	err := svc.applyClusterControlPlaneConfigUpdate(ctx, mockTx, cluster, &api.UpdateClusterControlPlaneConfig{
		DesiredCount: &desired,
	})
	require.Error(t, err, "must reject cluster-level controlPlaneConfig while a control-plane node pool exists")
	require.ErrorContains(t, err, "explicit control-plane migration")
	require.False(t, cluster.UsesControlPlaneController())
	mockTx.AssertNotCalled(t, "CreateControlPlaneConfiguration", mock.Anything, mock.Anything)
}
