// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package clusterstate

import (
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/dsx-ai-factory/nke/controller/api"
)

func TestReachability(t *testing.T) {
	require.Equal(t, "Unknown", Reachability(nil))
	for _, tc := range []struct {
		name      string
		condition *api.Condition
		want      string
	}{
		{"absent", nil, "Unknown"},
		{"true", &api.Condition{Status: api.ConditionStatusTrue}, "True"},
		{"false", &api.Condition{Status: api.ConditionStatusFalse}, "False"},
		{"unknown", &api.Condition{Status: api.ConditionStatusUnknown}, "Unknown"},
		{"future status", &api.Condition{Status: "FutureStatus"}, "Unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster := &api.Cluster{Conditions: api.ClusterConditions{
				Provisioned: api.Condition{Status: api.ConditionStatusTrue}, APIServerReachable: tc.condition,
			}}
			require.Equal(t, tc.want, Reachability(cluster))
			require.Equal(t, "Provisioned", Status(cluster))
			require.True(t, IsProvisioned(cluster))
		})
	}
}
