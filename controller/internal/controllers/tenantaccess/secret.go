// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package tenantaccess

import (
	"context"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/NVIDIA/nke/controller/internal/capi"
	"github.com/NVIDIA/nke/controller/internal/db"
)

// upsertSecret publishes one Controller-owned Secret.
//
// The live row is re-read here, immediately before the write, and not only once
// per publish: on the OIDC path publishSPIRE applies RBAC into the tenant
// cluster between the publish-time refresh and these writes, and that apply is
// bounded by tenantRequestTimeout, not by anything quick. A cluster that started
// deleting during it would otherwise have its Secrets re-created after teardown
// had already removed them, orphaning them for good once the row is gone.
func (c *Controller) upsertSecret(ctx context.Context, cluster db.Cluster, secretName, dataKey string, value []byte, mode string) error {
	live, skip, err := c.refreshCluster(ctx, cluster)
	if err != nil || skip {
		return err
	}

	return upsertOwnedSecret(ctx, c.k8sClient, c.logger, live, secretName, dataKey, value, mode)
}

func upsertOwnedSecret(
	ctx context.Context,
	k8sClient client.Client,
	logger *slog.Logger,
	cluster db.Cluster,
	secretName, dataKey string,
	value []byte,
	mode string,
) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: SecretsNamespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, k8sClient, secret, func() error {
		if err := capi.ValidateSecretOwnership(secret, cluster); err != nil {
			return err
		}
		capi.SetSecretOwnership(secret, cluster, mode)
		if dataKey == KubeconfigSecretKey {
			secret.Labels[clusterv1.ClusterNameLabel] = cluster.Name
			// CAPI indexes kubeconfig Secrets by this type in the Cluster namespace.
			secret.Type = clusterv1.ClusterSecretType
		} else {
			secret.Type = corev1.SecretTypeOpaque
		}
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[dataKey] = value
		return nil
	})
	if err != nil {
		return fmt.Errorf("upserting secret %s/%s: %w", SecretsNamespace, secretName, err)
	}

	switch op {
	case controllerutil.OperationResultCreated:
		logger.Info("Tenant-access secret created", "clusterName", cluster.Name, "secret", secretName)
	case controllerutil.OperationResultNone:
	case controllerutil.OperationResultUpdated,
		controllerutil.OperationResultUpdatedStatus,
		controllerutil.OperationResultUpdatedStatusOnly:
		logger.Debug("Tenant-access secret updated", "clusterName", cluster.Name, "secret", secretName, "operation", string(op))
	}
	return nil
}
