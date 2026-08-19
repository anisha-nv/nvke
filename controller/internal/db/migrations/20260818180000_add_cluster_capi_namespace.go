// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: LicenseRef-NvidiaProprietary
//
// NVIDIA CORPORATION, its affiliates and licensors retain all intellectual
// property and proprietary rights in and to this material, related
// documentation and any modifications thereto. Any use, reproduction,
// disclosure or distribution of this material and related documentation
// without an express license agreement from NVIDIA CORPORATION or
// its affiliates is strictly prohibited.

//nolint:gochecknoinits // this is the typical way to register bun migrations
package migrations

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

func init() {
	migrations.MustRegister(func(ctx context.Context, db *bun.DB) error {
		return db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			if _, err := tx.ExecContext(ctx, `
				ALTER TABLE clusters
				ADD COLUMN IF NOT EXISTS capi_namespace text
			`); err != nil {
				return fmt.Errorf("failed to add clusters.capi_namespace column: %w", err)
			}

			// Existing CAPI-backed rows already live in
			// cluster-<name>. Everything else (Forge-direct with no CAPI objects
			// yet) gets the shared default so adopt/greenfield land in nke-system.
			if _, err := tx.ExecContext(ctx, `
				UPDATE clusters
				SET capi_namespace = CASE
					WHEN provider_type = 'capi' THEN 'cluster-' || name
					ELSE 'nke-system'
				END
				WHERE capi_namespace IS NULL OR capi_namespace = ''
			`); err != nil {
				return fmt.Errorf("failed to backfill clusters.capi_namespace: %w", err)
			}

			if _, err := tx.ExecContext(ctx, `
				ALTER TABLE clusters
				ALTER COLUMN capi_namespace SET NOT NULL
			`); err != nil {
				return fmt.Errorf("failed to set clusters.capi_namespace not null: %w", err)
			}

			if _, err := tx.ExecContext(ctx, `
				ALTER TABLE clusters
				ADD CONSTRAINT clusters_capi_namespace_nonempty
				CHECK (length(trim(capi_namespace)) > 0)
			`); err != nil {
				return fmt.Errorf("failed to add clusters.capi_namespace nonempty check: %w", err)
			}

			return nil
		})
	}, func(ctx context.Context, db *bun.DB) error {
		return fmt.Errorf("down migration not supported")
	})
}
