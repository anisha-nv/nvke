// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/nke/controller/internal/testutil"
)

// Execute the actual additive migration against an isolated SQLite database.
// This tests preservation and CHECK behavior, not PostgreSQL deployment locking.
func TestAddClusterAPIProvider(t *testing.T) {
	ctx := t.Context()
	database := testutil.SetupDB(ctx, t)
	_, err := database.ExecContext(ctx, "CREATE TABLE clusters (id text PRIMARY KEY, provider_type text NOT NULL)")
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, "INSERT INTO clusters VALUES ('legacy-capi','capi'),('legacy-nico','nico')")
	require.NoError(t, err)
	require.NoError(t, addClusterAPIProvider(ctx, database))
	var count int
	require.NoError(t, database.NewRaw("SELECT COUNT(*) FROM clusters WHERE api_provider IS NULL").Scan(ctx, &count))
	require.Equal(t, 2, count)
	for _, tc := range []struct {
		mode, execution string
		valid           bool
	}{
		{"nico", "nico", true},
		{"infra", "capi", true},
		{"launchlayer", "capi", true},
		{"launchlayer", "nico", false},
		{"nico", "capi", false},
		{"future", "capi", false},
		{"", "capi", false},
	} {
		_, err := database.ExecContext(ctx, "INSERT INTO clusters (id, provider_type, api_provider) VALUES (?, ?, ?)", tc.mode+"-"+tc.execution, tc.execution, tc.mode)
		if tc.valid {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}
