// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: LicenseRef-NvidiaProprietary
//
// NVIDIA CORPORATION, its affiliates and licensors retain all intellectual
// property and proprietary rights in and to this material, related
// documentation and any modifications thereto. Any use, reproduction,
// disclosure or distribution of this material and related documentation
// without an express license agreement from NVIDIA CORPORATION or
// its affiliates is strictly prohibited.

package tenantaccess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/clientcmd"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nke/controller/internal/constants"
	"github.com/NVIDIA/nke/controller/internal/db"
	"github.com/NVIDIA/nke/controller/internal/kube"
)

// Publisher upserts the per-cluster kubeconfig Secret in nke-system (admin or
// SPIRE). TenantAccess is the only writer.
type Publisher struct {
	ManagementClient     ctrlclient.Client
	Logger               *slog.Logger
	SpiffeEndpointSocket string
	DB                   db.DB
	TenantClientFor      func(adminConf []byte) (ctrlclient.Client, error)
	// Upsert overrides the DB-backed guarded write. Tests may set this; production
	// callers must leave it nil so guardedUpsert always runs.
	Upsert func(ctx context.Context, cluster db.Cluster, secretName, dataKey string, value []byte, mode string) error
}

func NewPublisher(managementClient ctrlclient.Client, logger *slog.Logger, spiffeSocket string, database db.DB) *Publisher {
	return &Publisher{
		ManagementClient:     managementClient,
		Logger:               logger,
		SpiffeEndpointSocket: spiffeSocket,
		DB:                   database,
		TenantClientFor:      newTenantClient,
	}
}

// EnsurePublished writes nke-system/<cluster>-kubeconfig. Mode follows the live
// persisted OIDCAuthEnabled (re-read from DB), not the caller snapshot: SPIRE
// (+ nke-rbac + trust-bundle) when true, admin otherwise.
func (p *Publisher) EnsurePublished(ctx context.Context, cluster db.Cluster, adminConf []byte) error {
	live, skip, err := p.refreshCluster(ctx, cluster)
	if err != nil || skip {
		return err
	}
	if live.OIDCAuthEnabled {
		return p.publishSPIRE(ctx, live, adminConf)
	}
	return p.upsert(ctx, live, KubeconfigSecretName(live.Name), KubeconfigSecretKey, adminConf, constants.LabelValueKubeconfigModeAdmin)
}

// refreshCluster returns the live DB row. skip means do not publish (gone or deleting).
func (p *Publisher) refreshCluster(ctx context.Context, cluster db.Cluster) (db.Cluster, bool, error) {
	if p.DB == nil {
		return db.Cluster{}, false, fmt.Errorf("publisher requires a DB to refresh cluster %s", cluster.ID)
	}
	live, err := p.DB.GetCluster(ctx, cluster.ID)
	if errors.Is(err, db.ErrNotFound) {
		p.Logger.Debug("Cluster no longer exists; skipping tenant-access publish", "clusterID", cluster.ID)
		return db.Cluster{}, true, nil
	}
	if err != nil {
		return db.Cluster{}, false, fmt.Errorf("re-reading cluster %s before publish: %w", cluster.ID, err)
	}
	if live.Conditions.Deleting.Status == db.ConditionStatusTrue {
		p.Logger.Debug("Cluster started deleting; skipping tenant-access publish", "clusterID", cluster.ID)
		return db.Cluster{}, true, nil
	}
	return live, false, nil
}

func (p *Publisher) publishSPIRE(ctx context.Context, cluster db.Cluster, adminConf []byte) error {
	if err := p.applyRBAC(ctx, cluster, adminConf); err != nil {
		return err
	}

	parsedAdmin, err := clientcmd.Load(adminConf)
	if err != nil {
		return fmt.Errorf("parsing kubeconfig for cluster %s: %w", cluster.Name, err)
	}
	ca, err := extractCAFromKubeconfig(parsedAdmin)
	if err != nil {
		return err
	}
	server, err := extractServerFromKubeconfig(parsedAdmin)
	if err != nil {
		return err
	}

	kubeconfig, err := buildSpireKubeconfig(cluster.Name, server, ca, p.SpiffeEndpointSocket)
	if err != nil {
		return fmt.Errorf("building SPIRE kubeconfig for cluster %s: %w", cluster.Name, err)
	}

	if err := p.upsert(ctx, cluster, KubeconfigSecretName(cluster.Name), KubeconfigSecretKey, kubeconfig, constants.LabelValueKubeconfigModeSPIRE); err != nil {
		return err
	}
	return p.upsert(ctx, cluster, TrustBundleSecretName(cluster.Name), TrustBundleSecretKey, ca, constants.LabelValueKubeconfigModeSPIRE)
}

func (p *Publisher) upsert(ctx context.Context, cluster db.Cluster, secretName, dataKey string, value []byte, mode string) error {
	if p.Upsert != nil {
		return p.Upsert(ctx, cluster, secretName, dataKey, value, mode)
	}
	return p.guardedUpsert(ctx, cluster, secretName, dataKey, value, mode)
}

func (p *Publisher) guardedUpsert(ctx context.Context, cluster db.Cluster, secretName, dataKey string, value []byte, mode string) error {
	live, skip, err := p.refreshCluster(ctx, cluster)
	if err != nil || skip {
		return err
	}
	return upsertOwnedSecret(ctx, p.ManagementClient, p.Logger, live, secretName, dataKey, value, mode)
}

func (p *Publisher) applyRBAC(ctx context.Context, cluster db.Cluster, adminConf []byte) error {
	manifest, err := readRBACManifest(ctx, p.ManagementClient)
	if apierrors.IsNotFound(err) {
		// Live nkeOIDCAuth=false stops rendering this ConfigMap. Do not publish a
		// SPIRE kubeconfig that cannot authenticate as nke:cwe / CAPI SPIFFE IDs.
		return fmt.Errorf("nke-rbac ConfigMap not found; refusing to publish SPIRE kubeconfig for cluster %s: %w", cluster.Name, err)
	}
	if err != nil {
		return err
	}

	tenantClientFor := p.TenantClientFor
	if tenantClientFor == nil {
		tenantClientFor = newTenantClient
	}
	tenantClient, err := tenantClientFor(adminConf)
	if err != nil {
		return fmt.Errorf("building tenant client for cluster %s: %w", cluster.Name, err)
	}
	if err := applyManifestSSA(ctx, tenantClient, manifest, rbacFieldManager); err != nil {
		return fmt.Errorf("applying NKE RBAC to cluster %s: %w", cluster.Name, err)
	}
	return nil
}

func newTenantClient(adminConf []byte) (ctrlclient.Client, error) {
	restConfig, err := kube.NewRESTConfigFromKubeconfig(adminConf)
	if err != nil {
		return nil, err
	}
	restConfig.Timeout = tenantRequestTimeout

	return kube.NewClientFromRESTConfig(restConfig)
}
