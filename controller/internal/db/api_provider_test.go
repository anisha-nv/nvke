// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package db_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nke/controller/internal/db"
	"github.com/NVIDIA/nke/controller/internal/testutil"
)

func TestClusterAPIProviderResolution(t *testing.T) {
	for _, tc := range []struct {
		mode      db.APIProvider
		execution db.ProviderType
		want      db.APIProvider
		invalid   bool
	}{
		{"", db.ProviderTypeCAPI, db.APIProviderInfra, false},
		{"", db.ProviderTypeNico, db.APIProviderNico, false},
		{db.APIProviderLaunchLayer, db.ProviderTypeCAPI, db.APIProviderLaunchLayer, false},
		{db.APIProviderInfra, db.ProviderTypeCAPI, db.APIProviderInfra, false},
		{db.APIProviderNico, db.ProviderTypeNico, db.APIProviderNico, false},
		{db.APIProviderLaunchLayer, db.ProviderTypeNico, "", true},
		{db.APIProviderNico, db.ProviderTypeCAPI, "", true},
		{"unknown", db.ProviderTypeCAPI, "", true},
	} {
		c := db.Cluster{APIProvider: tc.mode, ProviderType: tc.execution, CapiInfraCluster: json.RawMessage(`{"kind":"LaunchLayerCluster"}`)}
		got, err := c.ResolveAPIProvider()
		if tc.invalid {
			require.ErrorIs(t, err, db.ErrInvalidAPIProviderBinding)
		} else {
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		}
	}
}

func TestClusterAPIProviderPersistenceAndParentJoins(t *testing.T) {
	for _, mode := range []db.APIProvider{"", db.APIProviderInfra, db.APIProviderLaunchLayer} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := t.Context()
			database := testutil.SetupDB(ctx, t, (*db.Cluster)(nil), (*db.NodePool)(nil), (*db.NodeSet)(nil))
			q := db.NewDB(database)
			cluster := newValidCluster()
			cluster.SiteID = uuid.Nil
			cluster.APIProvider = mode
			cluster.SetCAPIProvider(db.CAPIClusterProvider{InfraCluster: json.RawMessage(`{"kind":"LaunchLayerCluster"}`)})
			created, err := q.CreateCluster(ctx, &cluster)
			require.NoError(t, err)
			got, err := q.GetCluster(ctx, created.ID)
			require.NoError(t, err)
			require.Equal(t, mode, got.APIProvider)
			got.TeleportEnabled = true
			updated, err := q.UpdateCluster(ctx, &got)
			require.NoError(t, err)
			require.Equal(t, mode, updated.APIProvider)
			got.APIProvider = db.APIProviderNico
			_, err = q.UpdateCluster(ctx, &got)
			require.ErrorIs(t, err, db.ErrClusterAPIProviderImmutable)
			got, err = q.GetCluster(ctx, created.ID)
			require.NoError(t, err)
			require.Equal(t, mode, got.APIProvider)
			pool := db.NodePool{ID: uuid.New(), ClusterID: created.ID, Name: "workers", NodeNamePrefix: "worker"}
			pool.SetCapi(db.CAPINodePoolProvider{InfraMachineTemplate: json.RawMessage(`{"kind":"LaunchLayerMachineTemplate"}`), SpireAttestationMethod: db.SpireAttestationMethodJoinToken})
			_, err = q.CreateNodePool(ctx, &pool)
			require.NoError(t, err)
			loaded, err := q.GetNodePool(ctx, pool.ID)
			require.NoError(t, err)
			require.Equal(t, mode, loaded.ClusterAPIProvider)
			pools, err := q.ListNodePools(ctx, db.NodePoolFilters{ClusterID: &created.ID})
			require.NoError(t, err)
			require.Len(t, pools, 1)
			require.Equal(t, mode, pools[0].ClusterAPIProvider)
			pools, err = q.ListNodePools(ctx, db.NodePoolFilters{})
			require.NoError(t, err)
			require.Len(t, pools, 1)
			require.Equal(t, mode, pools[0].ClusterAPIProvider)
			set := db.NodeSet{ID: uuid.New(), NodePoolID: pool.ID}
			set.SetCapi(*pool.Capi)
			_, err = q.CreateNodeSet(ctx, &set)
			require.NoError(t, err)
			loadedSet, err := q.GetNodeSet(ctx, set.ID)
			require.NoError(t, err)
			require.Equal(t, mode, loadedSet.ClusterAPIProvider)
			sets, err := q.ListNodeSets(ctx, db.NodeSetFilters{NodePoolID: &pool.ID})
			require.NoError(t, err)
			require.Len(t, sets, 1)
			require.Equal(t, mode, sets[0].ClusterAPIProvider)
			sets, err = q.ListNodeSets(ctx, db.NodeSetFilters{})
			require.NoError(t, err)
			require.Len(t, sets, 1)
			require.Equal(t, mode, sets[0].ClusterAPIProvider)
			// The discriminator is a parent-only column, not persisted on child rows.
			var childColumns int
			err = database.NewRaw("SELECT COUNT(*) FROM pragma_table_info('node_pools') WHERE name = 'cluster_api_provider'").Scan(ctx, &childColumns)
			require.NoError(t, err)
			require.Zero(t, childColumns)
		})
	}
}
