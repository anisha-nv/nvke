// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package activities

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/NVIDIA/nke/controller/internal/capi"
	"github.com/NVIDIA/nke/controller/internal/constants"
	"github.com/NVIDIA/nke/controller/internal/controllers/tenantaccess"
	"github.com/NVIDIA/nke/controller/internal/db"
)

type DeleteTenantAccessSecretsInput struct {
	ClusterID uuid.UUID

	// ClusterName keys the Secrets to delete. It is carried from the workflow
	// rather than looked up here because this step runs after the cluster row has
	// been deleted.
	ClusterName string
}

// DeleteTenantAccessSecrets removes the per-cluster Secrets the tenant-access
// publisher writes into nke-system (kubeconfig and CA trust bundle). Missing
// Secrets are ignored. Secrets without a matching cluster-id ownership label,
// or owned by a different cluster UUID, are left untouched. Cleanup is not
// gated on tenantAccess.enabled so OIDC-mode clusters can be torn down after
// the live flags are flipped off.
func (a *Activities) DeleteTenantAccessSecrets(ctx context.Context, input DeleteTenantAccessSecretsInput) error {
	if a.k8sClient == nil {
		return fmt.Errorf("kubernetes client is required to delete tenant-access secrets")
	}

	if input.ClusterName == "" {
		return fmt.Errorf("cluster name is required to delete tenant-access secrets for cluster %s", input.ClusterID)
	}

	cluster := db.Cluster{ID: input.ClusterID, Name: input.ClusterName}
	names := []string{
		tenantaccess.KubeconfigSecretName(input.ClusterName),
		tenantaccess.TrustBundleSecretName(input.ClusterName),
	}
	for _, name := range names {
		secret := &corev1.Secret{}
		key := types.NamespacedName{Namespace: tenantaccess.SecretsNamespace, Name: name}
		if err := a.k8sClient.Get(ctx, key, secret); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("getting tenant-access secret %s/%s: %w", tenantaccess.SecretsNamespace, name, err)
		}
		if err := capi.ValidateSecretOwnership(secret, cluster); err != nil {
			continue
		}
		if secret.Labels == nil || secret.Labels[constants.LabelKeyClusterID] == "" {
			continue
		}
		if err := a.k8sClient.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting tenant-access secret %s/%s: %w", secret.Namespace, name, err)
		}
	}

	return nil
}
