// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dbreconcile_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/dsx-ai-factory/nke/controller/internal/controllers/dbreconcile"
)

func TestUnit_Register(t *testing.T) {
	mgr := &fakeManager{}
	reconciler := reconcile.TypedFunc[dbreconcile.Key](func(context.Context, dbreconcile.Key) (reconcile.Result, error) {
		return reconcile.Result{}, nil
	})

	err := dbreconcile.Register(mgr, dbreconcile.Registration{
		Name:       "dbreconcile-test",
		Reconciler: reconciler,
		Sources:    []dbreconcile.Source{testSource{}},
	})
	require.NoError(t, err)

	assert.Len(t, mgr.runnables, 1)
}

func TestUnit_RegisterRequiresSource(t *testing.T) {
	err := dbreconcile.Register(&fakeManager{}, dbreconcile.Registration{
		Name: "dbreconcile-test",
		Reconciler: reconcile.TypedFunc[dbreconcile.Key](
			func(context.Context, dbreconcile.Key) (reconcile.Result, error) {
				return reconcile.Result{}, nil
			},
		),
		Sources: []dbreconcile.Source{nil},
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "must specify Source")
}

func TestUnit_RegisterReturnsControllerError(t *testing.T) {
	err := dbreconcile.Register(&fakeManager{}, dbreconcile.Registration{
		Reconciler: reconcile.TypedFunc[dbreconcile.Key](
			func(context.Context, dbreconcile.Key) (reconcile.Result, error) {
				return reconcile.Result{}, nil
			},
		),
		Sources: []dbreconcile.Source{testSource{}},
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "must specify Name")
}

type fakeManager struct {
	manager.Manager

	runnables []manager.Runnable
}

func (m *fakeManager) Add(runnable manager.Runnable) error {
	m.runnables = append(m.runnables, runnable)
	return nil
}

func (m *fakeManager) GetControllerOptions() config.Controller {
	return config.Controller{}
}

type testSource struct{}

func (testSource) Start(context.Context, workqueue.TypedRateLimitingInterface[dbreconcile.Key]) error {
	return nil
}
