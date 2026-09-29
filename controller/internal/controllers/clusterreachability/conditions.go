// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package clusterreachability

import (
	"github.com/dsx-ai-factory/nke/controller/internal/db"
)

func applyObservation(cluster *db.Cluster, observation Observation) bool {
	var before db.Condition
	if cluster.Conditions.APIServerReachable != nil {
		before = *cluster.Conditions.APIServerReachable
	}
	after := before
	db.SetCondition(&after, db.Condition{Status: observation.Status, Message: observation.Message})
	if cluster.Conditions.APIServerReachable != nil && before.Equal(after) {
		return false
	}
	cluster.Conditions.APIServerReachable = &after
	return true
}
