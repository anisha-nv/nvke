// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package migrations

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

//nolint:gochecknoinits // Bun registers timestamped migrations during initialization.
func init() {
	migrations.MustRegister(addClusterAPIProvider, func(context.Context, *bun.DB) error {
		return fmt.Errorf("down migration not supported: preserve immutable cluster API mode")
	})
}

func addClusterAPIProvider(ctx context.Context, database *bun.DB) error {
	_, err := database.ExecContext(ctx, `
		ALTER TABLE clusters ADD COLUMN api_provider text
		CONSTRAINT clusters_api_provider_valid CHECK (
			api_provider IS NULL OR
			(api_provider = 'nico' AND provider_type = 'nico') OR
			(api_provider IN ('infra', 'launchlayer') AND provider_type = 'capi')
		)`)
	return err
}
