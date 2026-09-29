// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package clusterreachability

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/dsx-ai-factory/nke/controller/internal/controllers/dbreconcile"
	"github.com/dsx-ai-factory/nke/controller/internal/controllers/dbreconcile/dbwrapper"
	dbfilters "github.com/dsx-ai-factory/nke/controller/internal/controllers/dbreconcile/filters"
	"github.com/dsx-ai-factory/nke/controller/internal/db"
)

const (
	probeInterval = 2 * time.Minute
	probeWorkers  = 4
)

// Controller periodically stores the last completed reachability observation.
type Controller struct {
	db      *dbwrapper.DB
	prober  Prober
	logger  *slog.Logger
	now     func() time.Time
	mu      sync.Mutex
	nextDue map[uuid.UUID]time.Time
}

// NewController creates an observer that checks every cluster immediately on startup.
func NewController(logger *slog.Logger, database *dbwrapper.DB, prober Prober) *Controller {
	if logger == nil {
		logger = slog.Default()
	}
	return &Controller{
		db: database, prober: prober, logger: logger.With("controller", "cluster-api-reachability"),
		now: time.Now, nextDue: make(map[uuid.UUID]time.Time),
	}
}

// SetupWithManager registers the observer with the management manager's leader election.
func (c *Controller) SetupWithManager(mgr manager.Manager) error {
	return dbreconcile.AddController(mgr, dbreconcile.Controller{
		Name: "cluster-api-reachability", Reconciler: c, Workers: probeWorkers,
		Sources: []dbreconcile.Source{dbwrapper.NewClusterSource(c.db, dbfilters.Cluster{})},
	})
}

// Reconcile probes outside a transaction, then merges its observation into the locked latest row.
func (c *Controller) Reconcile(ctx context.Context, key dbreconcile.Key) (reconcile.Result, error) {
	cluster, err := c.db.GetCluster(ctx, key.ID)
	if errors.Is(err, db.ErrNotFound) {
		c.forget(key.ID)
		return reconcile.Result{}, nil
	}
	if err != nil {
		return reconcile.Result{}, errors.New("failed to read cluster for reachability observation")
	}
	if cluster.Conditions.Deleting.Status == db.ConditionStatusTrue {
		c.forget(key.ID)
		return reconcile.Result{}, nil
	}
	if delay := c.dueIn(key.ID); delay > 0 {
		return reconcile.Result{RequeueAfter: delay}, nil
	}
	started := c.now()
	observation, err := c.prober.Probe(ctx, cluster)
	if ctx.Err() != nil {
		return reconcile.Result{}, ctx.Err()
	}
	if err != nil {
		return reconcile.Result{}, errors.New("tenant API observation could not be completed")
	}
	active, changed, err := c.persist(ctx, key.ID, observation)
	if err != nil {
		// Do not advance the due time until persistence succeeds. A framework
		// retry must not silently discard this observation for two minutes.
		c.forget(key.ID)
		return reconcile.Result{}, errors.New("failed to persist tenant API observation")
	}
	if !active {
		c.forget(key.ID)
		return reconcile.Result{}, nil
	}
	c.mu.Lock()
	c.nextDue[key.ID] = c.now().Add(probeInterval)
	c.mu.Unlock()
	if changed {
		c.logger.Info("Tenant API reachability changed", "clusterID", key.ID, "status", observation.Status)
	}
	c.logger.Debug("Tenant API probe completed", "clusterID", key.ID, "status", observation.Status, "duration", c.now().Sub(started))
	return reconcile.Result{RequeueAfter: probeInterval}, nil
}

func (c *Controller) persist(ctx context.Context, id uuid.UUID, observation Observation) (bool, bool, error) {
	var active, changed bool
	err := c.db.RunInTx(ctx, func(ctx context.Context, tx db.Tx) error {
		cluster, err := tx.GetClusterForUpdate(ctx, id)
		if errors.Is(err, db.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if cluster.Conditions.Deleting.Status == db.ConditionStatusTrue {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		active = true
		changed = applyObservation(&cluster, observation)
		if !changed {
			return nil
		}
		_, err = tx.UpdateCluster(ctx, &cluster)
		return err
	})
	return active, changed, err
}

func (c *Controller) dueIn(id uuid.UUID) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nextDue[id].Sub(c.now())
}

func (c *Controller) forget(id uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.nextDue, id)
}
