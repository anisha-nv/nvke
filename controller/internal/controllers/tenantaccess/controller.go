// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package tenantaccess publishes each cluster's admin kubeconfig into the
// management-cluster tenant-access Secret plane so downstream consumers can
// reach tenant clusters.
package tenantaccess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"k8s.io/utils/ptr"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nke/controller/internal/agent"
	"github.com/NVIDIA/nke/controller/internal/config"
	"github.com/NVIDIA/nke/controller/internal/constants"
	"github.com/NVIDIA/nke/controller/internal/controllers/dbreconcile"
	"github.com/NVIDIA/nke/controller/internal/controllers/dbreconcile/dbwrapper"
	"github.com/NVIDIA/nke/controller/internal/db"
)

const (
	// KubeconfigSecretKey is the data key inside the per-cluster kubeconfig Secret.
	KubeconfigSecretKey = "value"

	// SecretsNamespace is the management-cluster namespace that holds the
	// per-cluster Secrets this controller owns (kubeconfig + trust bundle). It is
	// exported because the teardown activity deletes those Secrets on cluster
	// removal. The chart creates the namespace and scopes CWE's Secret grant to
	// it, so this is a constant rather than a config knob.
	SecretsNamespace = constants.NamespaceNKESystem

	resyncInterval = 3 * time.Minute
)

// KubeconfigSecretName returns the Secret name that holds the per-cluster
// kubeconfig published into SecretsNamespace.
func KubeconfigSecretName(clusterName string) string { return clusterName + "-kubeconfig" }

// TenantManagerSet stops the per-cluster controller manager when its cluster
// enters deletion.
type TenantManagerSet interface {
	RemoveCluster(clusterID uuid.UUID)
}

// Controller publishes one cluster's access material into the tenant-access
// Secret plane for each cluster-keyed reconciliation.
type Controller struct {
	cfg             config.Config
	logger          *slog.Logger
	db              *dbwrapper.DB
	k8sClient       ctrlclient.Client
	nodeAgentClient agent.Client
	tenantManagers  TenantManagerSet
	publisher       *Publisher
}

// NewController creates a new TenantAccessController.
func NewController(
	cfg config.Config,
	logger *slog.Logger,
	dbInstance *dbwrapper.DB,
	k8sClient ctrlclient.Client,
	nodeAgentClient agent.Client,
	tenantManagers TenantManagerSet,
) *Controller {
	c := &Controller{
		cfg:             cfg,
		logger:          logger.With("controller", "tenant-access"),
		db:              dbInstance,
		k8sClient:       k8sClient,
		nodeAgentClient: nodeAgentClient,
		tenantManagers:  tenantManagers,
	}
	c.publisher = NewPublisher(k8sClient, c.logger, cfg.SpiffeEndpointSocket, dbInstance)
	return c
}

// SetupWithManager registers the cluster-scoped tenant-access reconciler. A
// joined control-plane node write is the low-latency bootstrap trigger, while
// the cluster source performs startup and periodic scans for eventual repair.
func (c *Controller) SetupWithManager(mgr manager.Manager) error {
	return dbreconcile.SetupController(
		mgr,
		"tenant-access",
		c,
		c.db.ClusterSource(db.ClusterFilters{}, resyncInterval),
		c.db.ClusterForNodeSource(db.NodeFilters{
			ControlPlane: ptr.To(true),
			Joined:       ptr.To(true),
			Deleting:     ptr.To(false),
		}),
	)
}

// Reconcile publishes access material for one cluster. The cluster row is
// loaded at execution time so queued events are hints, never state snapshots.
func (c *Controller) Reconcile(ctx context.Context, key dbreconcile.Key) (reconcile.Result, error) {
	cluster, err := c.db.GetCluster(ctx, key.ID)
	if errors.Is(err, db.ErrNotFound) {
		return reconcile.Result{}, nil
	}
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("getting cluster %s: %w", key.ID, err)
	}
	if cluster.Conditions.Deleting.Status == db.ConditionStatusTrue {
		c.tenantManagers.RemoveCluster(cluster.ID)
		// Temporal owns Secret deletion after it no longer needs tenant access.
		return reconcile.Result{}, nil
	}
	return reconcile.Result{}, c.reconcileCluster(ctx, cluster)
}

// reconcileCluster fetches a control-plane node's admin kubeconfig and publishes
// it into the tenant-access Secret plane (admin.conf, or SPIRE when OIDC is on).
func (c *Controller) reconcileCluster(ctx context.Context, cluster db.Cluster) error {
	adminConf, ready, err := c.fetchAdminKubeconfig(ctx, cluster)
	if err != nil {
		return err
	}
	if !ready {
		// Not-ready gate: no joined control-plane node can serve a kubeconfig yet.
		// A matching node update normally re-enqueues immediately; the periodic
		// cluster resync provides bounded recovery when that notification is lost.
		c.logger.Debug("No ready control plane node for kubeconfig extraction; skipping", "clusterID", cluster.ID, "clusterName", cluster.Name)
		return nil
	}

	return c.publisher.EnsurePublished(ctx, cluster, adminConf)
}
