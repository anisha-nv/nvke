// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"

	"github.com/dsx-ai-factory/nke/controller/api"
	"github.com/dsx-ai-factory/nke/controller/internal/capi/validation"
	"github.com/dsx-ai-factory/nke/controller/internal/db"
)

// API mode is explicit. Raw manifests never select typed admission by kind.
func requestProvider(nico, launchlayer, infra, required bool) (db.APIProvider, error) {
	var mode db.APIProvider
	count := 0
	for _, candidate := range []struct {
		present bool
		mode    db.APIProvider
	}{
		{nico, db.APIProviderNico}, {launchlayer, db.APIProviderLaunchLayer}, {infra, db.APIProviderInfra},
	} {
		if candidate.present {
			count++
			mode = candidate.mode
		}
	}
	if count > 1 || (required && count != 1) {
		return "", fmt.Errorf("exactly one provider must be set")
	}
	return mode, nil
}

func matchProvider(cluster db.Cluster, mode db.APIProvider) error {
	bound, err := cluster.ResolveAPIProvider()
	if err != nil {
		return err
	}
	if bound == db.APIProviderNico && mode == db.APIProviderInfra {
		return fmt.Errorf("controlPlaneConfig.infra is not supported, please provide controlPlaneConfig.nico")
	}
	if bound == db.APIProviderInfra && mode == db.APIProviderNico {
		return fmt.Errorf("controlPlaneConfig.nico is not supported, please provide controlPlaneConfig.infra")
	}
	if mode != "" && mode != bound {
		return fmt.Errorf("provider block must match the cluster's immutable %s API mode", bound)
	}
	return nil
}

// validateLaunchLayerSpireAttestationMethod rejects tpm in addition to the
// generic CAPI attestation check. tpm attestation depends on a TPM
// endorsement key hash that only CAPNICo's controller reports on
// NicoMachine.Status; CAPLL reports no such value, so a LaunchLayer node
// requesting tpm would never receive its SPIRE registration entry and would
// hang rather than fail.
func validateLaunchLayerSpireAttestationMethod(method, path string) error {
	if err := validateCAPISpireAttestationMethod(method); err != nil {
		return fmt.Errorf("%s.spireAttestationMethod must be tpm or joinToken", path)
	}
	if method == string(db.SpireAttestationMethodTPM) {
		return fmt.Errorf("%s.spireAttestationMethod: tpm is not supported for launchlayer, only joinToken", path)
	}
	return nil
}

func validateLaunchLayerNodeProvider(p *api.LaunchLayerNodePoolProvider, path string) error {
	if p == nil {
		return fmt.Errorf("%s is required", path)
	}
	if err := validation.LaunchLayerMachineSpec(launchLayerMachineSpec(p.Machine), path+".machine"); err != nil {
		return err
	}
	if err := validateLaunchLayerSpireAttestationMethod(string(p.SpireAttestationMethod), path); err != nil {
		return err
	}
	if err := validateCloudInitFiles(sliceValue(p.BootstrapFiles)); err != nil {
		return fmt.Errorf("%s.bootstrapFiles contains invalid or controller-managed file paths", path)
	}
	return nil
}

func validateLaunchLayerNodePatch(p *api.LaunchLayerNodePoolProviderUpdate, path string) error {
	if p == nil {
		return nil
	}
	if p.Machine != nil {
		if err := validation.LaunchLayerMachineSpec(launchLayerMachineSpec(*p.Machine), path+".machine"); err != nil {
			return err
		}
	}
	if p.SpireAttestationMethod != nil {
		if err := validateLaunchLayerSpireAttestationMethod(string(*p.SpireAttestationMethod), path); err != nil {
			return err
		}
	}
	if err := validateCloudInitFiles(sliceValue(p.BootstrapFiles)); err != nil {
		return fmt.Errorf("%s.bootstrapFiles contains invalid or controller-managed file paths", path)
	}
	return nil
}

func validateLaunchLayerCreateIntent(req *api.CreateClusterRequest) error {
	if req.Launchlayer == nil {
		return nil
	}
	return validation.LaunchLayerClusterSpec(launchLayerClusterSpec(req.Launchlayer), "launchlayer")
}

func validateStoredLaunchLayerCluster(cluster db.Cluster) error {
	if cluster.Capi == nil {
		return errStoredLaunchLayerIntent
	}
	_, err := apiLaunchLayerCluster(cluster.Capi.InfraCluster)
	return err
}

func validateLaunchLayerNodePoolUpdate(cluster db.Cluster, pool *db.NodePool, req *api.UpdateNodePoolRequest) error {
	if req.Launchlayer == nil {
		return nil
	}
	if err := validateStoredLaunchLayerCluster(cluster); err != nil {
		return err
	}
	_, err := apiLaunchLayerNodeProvider(pool.Capi)
	return err
}

func validateLaunchLayerControlPlaneUpdate(cluster db.Cluster, spec db.ControlPlaneConfigSpec) error {
	if cluster.APIProvider != db.APIProviderLaunchLayer {
		return nil
	}
	if err := validateStoredLaunchLayerCluster(cluster); err != nil {
		return err
	}
	_, err := apiLaunchLayerControlPlane(spec.Capi)
	return err
}

// Validate a copy under the parent row lock before a combined PATCH can enqueue
// any other cluster operation. Unrelated maintenance does not re-admit intent.
func (s *clusterService) preflightInfraControlPlanePatch(ctx context.Context, cluster db.Cluster, req *api.UpdateClusterControlPlaneConfig) error {
	if req == nil {
		return nil
	}
	mode, err := requestProvider(req.Nico != nil, req.Launchlayer != nil, req.Infra != nil, false)
	if err != nil {
		return newBadRequestErrorf("%s", err)
	}
	if err := matchProvider(cluster, mode); err != nil {
		return newBadRequestErrorf("%s", err)
	}
	if mode == "" {
		return nil
	}
	if err := s.providers.require(string(mode)); err != nil {
		return newBadRequestErrorf("%s", err)
	}
	if mode != db.APIProviderInfra && mode != db.APIProviderLaunchLayer {
		return nil
	}
	if !s.useControlPlaneController {
		return errControlPlaneConfigNotEnabled
	}
	if err := validateLaunchLayerNodePatch(req.Launchlayer, "controlPlaneConfig.launchlayer"); err != nil {
		return newBadRequestErrorf("%s", err)
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
