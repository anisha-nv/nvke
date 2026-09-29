// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package clusterreachability

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/nke/controller/internal/db"
)

func TestReachabilityConditionTransitions(t *testing.T) {
	for _, status := range []db.ConditionStatus{db.ConditionStatusTrue, db.ConditionStatusFalse, db.ConditionStatusUnknown} {
		t.Run(string(status), func(t *testing.T) {
			cluster := db.Cluster{}
			observation := Observation{Status: status, Message: "Observation"}
			require.True(t, applyObservation(&cluster, observation))
			before := *cluster.Conditions.APIServerReachable
			require.False(t, before.LastTransitionTime.IsZero())
			require.False(t, applyObservation(&cluster, observation))
			observation.Message = "Updated message"
			require.True(t, applyObservation(&cluster, observation))
			require.True(t, before.LastTransitionTime.Equal(cluster.Conditions.APIServerReachable.LastTransitionTime))
			cluster.Conditions.APIServerReachable.LastTransitionTime = time.Now().Add(-time.Hour)
			observation.Status = db.ConditionStatusTrue
			if status == db.ConditionStatusTrue {
				observation.Status = db.ConditionStatusFalse
			}
			require.True(t, applyObservation(&cluster, observation))
			require.WithinDuration(t, time.Now(), cluster.Conditions.APIServerReachable.LastTransitionTime, time.Second)
		})
	}
}
