// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/NVIDIA/nke/controller/internal/config"
	"github.com/NVIDIA/nke/controller/internal/testutil"
)

func TestNewControllerManagerGatesCAPIClusterScaffold(t *testing.T) {
	tests := map[string]struct {
		enableForgeCapi bool
		wantScaffold    bool
	}{
		"disabled": {
			enableForgeCapi: false,
			wantScaffold:    false,
		},
		"enabled": {
			enableForgeCapi: true,
			wantScaffold:    true,
		},
	}

	for name, params := range tests {
		t.Run(name, func(t *testing.T) {
			manager, err := NewControllerManager(
				config.Config{EnableForgeCapi: params.enableForgeCapi},
				slog.Default(),
				&testutil.MockDB{},
				fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build(),
				nil,
				nil,
				nil,
				nil,
				"",
				nil,
			)
			require.NoError(t, err)

			if params.wantScaffold {
				require.NotNil(t, manager.capiClusterScaffold)
				return
			}
			require.Nil(t, manager.capiClusterScaffold)
		})
	}
}

func TestNewControllerManagerConstructsTenantAccess(t *testing.T) {
	t.Run("k8s client present even when TenantAccessEnabled is false", func(t *testing.T) {
		manager, err := NewControllerManager(
			config.Config{TenantAccessEnabled: false},
			slog.Default(),
			&testutil.MockDB{},
			fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build(),
			nil,
			nil,
			nil,
			nil,
			"",
			nil,
		)
		require.NoError(t, err)
		require.NotNil(t, manager.tenantAccessController)
	})

	t.Run("nil k8s client returns error", func(t *testing.T) {
		manager, err := NewControllerManager(
			config.Config{TenantAccessEnabled: true},
			slog.Default(),
			&testutil.MockDB{},
			nil,
			nil,
			nil,
			nil,
			nil,
			"",
			nil,
		)
		require.Error(t, err)
		require.Nil(t, manager)
		require.ErrorContains(t, err, "kubernetes client is required")
	})
}
