// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"

	"github.com/NVIDIA/nke/controller/api"
	"github.com/NVIDIA/nke/controller/internal/capi/validation"
	"github.com/NVIDIA/nke/controller/internal/db"
)

// Dispatch from either side before generic envelope checks: a LaunchLayer
// template cannot select a different provider from the parent cluster binding.
func validateLaunchLayerPair(cluster, template map[string]any, clusterPath, templatePath string) error {
	if !validation.IsLaunchLayer(cluster) && !validation.IsLaunchLayer(template) {
		return nil
	}
	if err := validation.LaunchLayerCluster(cluster, clusterPath); err != nil {
		return err
	}
	return validation.LaunchLayerMachineTemplate(template, templatePath)
}

func validateLaunchLayerCreateIntent(req *api.CreateClusterRequest) error {
	if req.Infra == nil {
		return nil
	}
	cluster := req.Infra.Cluster
	if validation.IsLaunchLayer(cluster) {
		if err := validation.LaunchLayerCluster(cluster, "infra.cluster"); err != nil {
			return err
		}
	}
	if req.ControlPlaneConfig != nil && req.ControlPlaneConfig.Infra != nil {
		if err := validateLaunchLayerPair(cluster, req.ControlPlaneConfig.Infra.MachineTemplate,
			"infra.cluster", "controlPlaneConfig.infra.machineTemplate"); err != nil {
			return err
		}
	}
	if req.NodePools != nil {
		for i, pool := range *req.NodePools {
			if pool.Infra == nil {
				continue // The shared request-mode validator reports the missing infra.
			}
			path := fmt.Sprintf("nodePools[%d].infra.machineTemplate", i)
			if err := validateLaunchLayerPair(cluster, pool.Infra.MachineTemplate, "infra.cluster", path); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateStoredLaunchLayerBinding(cluster db.Cluster, template map[string]any, templatePath string) error {
	// Persisted provider payloads are authoritative; do not infer a missing
	// parent binding from the new template or silently rewrite stored identity.
	var manifest map[string]any
	if cluster.Capi != nil {
		var err error
		manifest, err = objectFromRawManifest(cluster.Capi.InfraCluster)
		if err != nil {
			return fmt.Errorf("stored infra.cluster: cannot read provider binding; repair the stored cluster before changing provider intent")
		}
	}
	if !validation.IsLaunchLayer(manifest) && !validation.IsLaunchLayer(template) {
		return nil
	}
	if err := validation.LaunchLayerCluster(manifest, "infra.cluster"); err != nil {
		return fmt.Errorf("stored provider binding needs repair before changing provider intent: %w", err)
	}
	return validation.LaunchLayerMachineTemplate(template, templatePath)
}

func validateLaunchLayerNodePoolUpdate(cluster db.Cluster, pool *db.NodePool, req *api.UpdateNodePoolRequest) error {
	if cluster.ProviderType != db.ProviderTypeCAPI || req.Infra == nil {
		return nil // Unrelated maintenance must not re-admit legacy provider intent.
	}
	provider, err := pool.CAPIProvider()
	if err != nil {
		return newBadRequestErrorf("infra: stored node-pool provider payload needs repair")
	}
	template, err := objectFromRawManifest(provider.InfraMachineTemplate)
	if err != nil {
		return newBadRequestErrorf("infra.machineTemplate: stored template needs repair")
	}
	if err := validateStoredLaunchLayerBinding(cluster, template, "infra.machineTemplate"); err != nil {
		return newBadRequestErrorf("%s", err)
	}
	return nil
}

func validateLaunchLayerControlPlaneUpdate(cluster db.Cluster, spec db.ControlPlaneConfigSpec) error {
	if spec.Capi == nil {
		return newBadRequestErrorf("controlPlaneConfig.infra: required provider configuration")
	}
	for i, raw := range spec.Capi.InfraMachineTemplatePreferences {
		template, err := objectFromRawManifest(raw)
		if err != nil {
			return newBadRequestErrorf("controlPlaneConfig.infra.machineTemplate: stored template needs repair")
		}
		path := "controlPlaneConfig.infra.machineTemplate"
		if i > 0 {
			path = fmt.Sprintf("%sPreferences[%d]", path, i)
		}
		if err := validateStoredLaunchLayerBinding(cluster, template, path); err != nil {
			return newBadRequestErrorf("%s", err)
		}
	}
	if err := validateCAPISpireAttestationMethod(string(spec.Capi.SpireAttestationMethod)); err != nil {
		return newBadRequestErrorf("controlPlaneConfig.infra.spireAttestationMethod: %s", err)
	}
	return nil
}

// Validate a copy before a combined PATCH can enqueue other cluster operations.
// The normal update path still merges and validates defensively before saving.
func (s *clusterService) preflightInfraControlPlanePatch(ctx context.Context, cluster db.Cluster, req *api.UpdateClusterControlPlaneConfig) error {
	if req == nil || req.Infra == nil || cluster.ProviderType != db.ProviderTypeCAPI {
		return nil
	}
	if !s.useControlPlaneController {
		return errControlPlaneConfigNotEnabled
	}
	spec := db.ConfigSpecFromFull(cluster.ControlPlaneConfig)
	if err := s.applyClusterLevelControlPlaneSpecUpdate(ctx, &cluster, req, &spec); err != nil {
		return err
	}
	if err := validateLaunchLayerControlPlaneUpdate(cluster, spec); err != nil {
		return err
	}
	return validateMergedClusterControlPlaneSpec(cluster, spec)
}
