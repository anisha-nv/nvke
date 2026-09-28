// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dbreconcile

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	crsource "sigs.k8s.io/controller-runtime/pkg/source"

	dbsource "github.com/dsx-ai-factory/nke/controller/internal/controllers/dbreconcile/source"
)

// Register creates a controller-runtime controller for a reconciler and registers its event sources.
func Register(mgr manager.Manager, registration Registration) error {
	if registration.Workers < 0 {
		return fmt.Errorf("controller %q: Workers must not be negative", registration.Name)
	}
	if registration.ReconcileTimeout < 0 {
		return fmt.Errorf("controller %q: ReconcileTimeout must not be negative", registration.Name)
	}

	sources := append([]crsource.TypedSource[Key](nil), registration.Sources...)
	for index, watch := range registration.Watches {
		if watch.Object == nil {
			return fmt.Errorf("controller %q: Watches[%d].Object must not be nil", registration.Name, index)
		}
		if watch.Map == nil {
			return fmt.Errorf("controller %q: Watches[%d].Map must not be nil", registration.Name, index)
		}

		selector := labels.Everything()
		if watch.LabelSelector != nil {
			var err error
			selector, err = metav1.LabelSelectorAsSelector(watch.LabelSelector)
			if err != nil {
				return fmt.Errorf("controller %q: Watches[%d] invalid label selector: %w", registration.Name, index, err)
			}
		}

		mapFunc := watch.Map
		logger := mgr.GetLogger().WithValues("controller", registration.Name)
		sources = append(sources, dbsource.NewKubernetesSource(
			mgr.GetCache(),
			watch.Object,
			func(ctx context.Context, object client.Object) []Key {
				mapLog := logger.WithValues("mapObject", client.ObjectKeyFromObject(object))
				return mapFunc(crlog.IntoContext(ctx, mapLog), object)
			},
			dbsource.KubernetesSourceOptions{
				Namespace:     watch.Namespace,
				LabelSelector: selector,
			},
		))
	}

	if len(sources) == 0 {
		return fmt.Errorf("must specify Source for DB reconciler")
	}
	for _, source := range sources {
		if source == nil {
			return fmt.Errorf("must specify Source for DB reconciler")
		}
	}

	ctrl, err := crcontroller.NewTyped[Key](registration.Name, mgr, crcontroller.TypedOptions[Key]{
		Reconciler:              registration.Reconciler,
		MaxConcurrentReconciles: registration.Workers,
		ReconciliationTimeout:   registration.ReconcileTimeout,
	})
	if err != nil {
		return err
	}
	for _, source := range sources {
		if err := ctrl.Watch(source); err != nil {
			return err
		}
	}
	return nil
}
