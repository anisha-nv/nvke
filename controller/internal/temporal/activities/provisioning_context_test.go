// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package activities

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/nvke/controller/internal/config"
	"github.com/dsx-ai-factory/nvke/controller/internal/db"
	"github.com/dsx-ai-factory/nvke/controller/internal/testutil"
)

func TestUnit_GetNodeProvisioningContextMachineConfigurationFlag(t *testing.T) {
	type parameters struct {
		nico    bool
		enabled bool
	}
	tests := map[string]parameters{
		"CAPI with flag disabled": {},
		"CAPI with flag enabled":  {enabled: true},
		"NICo with flag disabled": {nico: true},
		"NICo with flag enabled":  {nico: true, enabled: true},
	}
	for name, params := range tests {
		t.Run(name, func(t *testing.T) {
			nodeID, nodeSetID, nodePoolID := uuid.New(), uuid.New(), uuid.New()
			node := db.Node{ID: nodeID, NodeSetID: nodeSetID, NodePoolID: nodePoolID}
			nodeSet := db.NodeSet{ID: nodeSetID, NodePoolID: nodePoolID}
			if params.nico {
				nodeSet.SetNico(db.NicoNodePoolProvider{ConfigProfile: "worker-profile"})
			} else {
				nodeSet.SetCapi(db.CAPINodePoolProvider{})
			}
			database := &testutil.MockDB{}
			database.On("GetNode", mock.Anything, nodeID).Return(node, nil).Once()
			database.On("GetNodeSet", mock.Anything, nodeSetID).Return(nodeSet, nil).Once()
			database.On("GetNodePool", mock.Anything, nodePoolID).Return(db.NodePool{ID: nodePoolID}, nil).Once()
			a := &Activities{cfg: config.Config{MachineConfigurationControllerEnabled: params.enabled}, db: database}

			got, err := a.GetNodeProvisioningContext(context.Background(), nodeID)
			require.NoError(t, err)
			require.Equal(t, params.enabled, got.MachineConfigurationEnabled)
			if params.nico {
				require.Equal(t, "worker-profile", got.ConfigProfile)
			} else {
				require.Empty(t, got.ConfigProfile)
			}
			database.AssertExpectations(t)
		})
	}
}
