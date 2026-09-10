// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"errors"

	"github.com/NVIDIA/nke/controller/api"
	"github.com/NVIDIA/nke/controller/internal/capi/validation"
	"github.com/NVIDIA/nke/controller/internal/db"
)

const (
	launchLayerAPIVersionField = "apiVersion"
	launchLayerKindField       = "kind"
	launchLayerSpecField       = "spec"
	launchLayerTemplateField   = "template"
	launchLayerMetadataField   = "metadata"
)

var errStoredLaunchLayerIntent = errors.New("stored launchlayer intent needs repair")

func APIClusterFromDB(cluster *db.Cluster) (api.Cluster, error) {
	out := baseClusterFromDB(cluster)
	// Rows predating the discriminator retain their historical representation.
	if cluster.APIProvider == "" {
		return out, nil
	}
	mode, err := cluster.ResolveAPIProvider()
	if err != nil {
		return api.Cluster{}, err
	}
	if mode != db.APIProviderLaunchLayer {
		return out, nil
	}
	if cluster.Capi == nil || cluster.Nico != nil {
		return api.Cluster{}, errStoredLaunchLayerIntent
	}
	out.Launchlayer, err = apiLaunchLayerCluster(cluster.Capi.InfraCluster)
	if err != nil {
		return api.Cluster{}, err
	}
	out.Infra = nil
	if cluster.ControlPlaneConfig.Nico != nil || (cluster.ControlPlaneConfig.DesiredCount > 0 && cluster.ControlPlaneConfig.Capi == nil) {
		return api.Cluster{}, errStoredLaunchLayerIntent
	}
	if cluster.ControlPlaneConfig.Capi != nil {
		out.ControlPlaneConfig.Launchlayer, err = apiLaunchLayerControlPlane(cluster.ControlPlaneConfig.Capi)
		if err != nil {
			return api.Cluster{}, err
		}
		out.ControlPlaneConfig.Infra = nil
	}
	return out, nil
}

func apiNodePoolFromDB(pool *db.NodePool) (api.NodePool, error) {
	out := baseNodePoolFromDB(pool)
	if pool.ClusterAPIProvider == "" {
		return out, nil
	}
	providerType := db.ProviderTypeCAPI
	if pool.Nico != nil {
		providerType = db.ProviderTypeNico
	}
	mode, err := (db.Cluster{APIProvider: pool.ClusterAPIProvider, ProviderType: providerType}).ResolveAPIProvider()
	if err != nil {
		return api.NodePool{}, err
	}
	if mode == db.APIProviderLaunchLayer {
		out.Launchlayer, err = apiLaunchLayerNodeProvider(pool.Capi)
		if err != nil {
			return api.NodePool{}, err
		}
		out.Infra = nil
	}
	return out, nil
}

// Normalize only after typed admission and API-mode matching. These transient
// objects reuse CAPI persistence; they never select public request/response mode.
func infraNodePatch(p *api.LaunchLayerNodePoolProviderUpdate) *api.InfraNodePoolProviderUpdate {
	out := &api.InfraNodePoolProviderUpdate{
		SpireAttestationMethod: p.SpireAttestationMethod,
		BootstrapFiles:         sliceValue(p.BootstrapFiles),
		PreKubeadmCommands:     sliceValue(p.PreKubeadmCommands),
	}
	if p.Machine != nil {
		out.MachineTemplate = launchLayerMachineTemplate(*p.Machine)
	}
	return out
}

func infraControlPlanePatch(p *api.LaunchLayerControlPlaneProviderUpdate) *api.InfraControlPlaneProviderUpdate {
	n := infraNodePatch(p)
	return &api.InfraControlPlaneProviderUpdate{
		MachineTemplate: n.MachineTemplate, SpireAttestationMethod: n.SpireAttestationMethod,
		BootstrapFiles: n.BootstrapFiles, PreKubeadmCommands: n.PreKubeadmCommands,
	}
}

func infraControlPlaneCreate(p *api.LaunchLayerControlPlaneProviderCreate) *api.InfraControlPlaneProvider {
	return &api.InfraControlPlaneProvider{
		MachineTemplate: launchLayerMachineTemplate(p.Machine), SpireAttestationMethod: p.SpireAttestationMethod,
		BootstrapFiles: sliceValue(p.BootstrapFiles), PreKubeadmCommands: sliceValue(p.PreKubeadmCommands),
	}
}

func launchLayerClusterSpec(p *api.LaunchLayerClusterProvider) map[string]any {
	spec := map[string]any{
		"owner": map[string]any{"userID": p.Owner.UserID, "username": p.Owner.Username},
	}
	if p.VpcID != nil {
		spec["vpcID"] = *p.VpcID
	}
	if p.OrgID != nil {
		spec["orgID"] = *p.OrgID
	}
	if p.SiteID != nil {
		spec["siteID"] = *p.SiteID
	}
	if p.CloudProviderEnabled != nil {
		spec["cloudProviderEnabled"] = *p.CloudProviderEnabled
	}
	return spec
}

func launchLayerMachineSpec(p api.LaunchLayerMachine) map[string]any {
	spec := map[string]any{"instanceType": p.InstanceType}
	if p.HostOSProfile != nil {
		spec["hostOSProfile"] = *p.HostOSProfile
	}
	if p.VMProfile != nil {
		spec["vmProfile"] = *p.VMProfile
	}
	if p.FulfillmentStrategy != nil {
		spec["fulfillmentStrategy"] = string(*p.FulfillmentStrategy)
	}
	if p.SecurityGroups != nil {
		groups := map[string]any{}
		if p.SecurityGroups.PrimaryNetworkInterface != nil {
			groups["primaryNetworkInterface"] = launchLayerStringArray(*p.SecurityGroups.PrimaryNetworkInterface)
		}
		if p.SecurityGroups.GpuNetworkInterface != nil {
			groups["gpuNetworkInterface"] = launchLayerStringArray(*p.SecurityGroups.GpuNetworkInterface)
		}
		spec["securityGroups"] = groups
	}
	return spec
}

func launchLayerStringArray(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

func launchLayerClusterManifest(p *api.LaunchLayerClusterProvider) map[string]any {
	return map[string]any{launchLayerAPIVersionField: validation.LaunchLayerAPIVersion, launchLayerKindField: "LaunchLayerCluster", launchLayerSpecField: launchLayerClusterSpec(p)}
}

func launchLayerMachineTemplate(p api.LaunchLayerMachine) map[string]any {
	return map[string]any{
		launchLayerAPIVersionField: validation.LaunchLayerAPIVersion, launchLayerKindField: "LaunchLayerMachineTemplate",
		launchLayerSpecField: map[string]any{launchLayerTemplateField: map[string]any{launchLayerSpecField: launchLayerMachineSpec(p)}},
	}
}

func dbLaunchLayerNodeProvider(p *api.LaunchLayerNodePoolProvider) (db.CAPINodePoolProvider, error) {
	raw, err := rawManifestFromObject(launchLayerMachineTemplate(p.Machine))
	if err != nil {
		return db.CAPINodePoolProvider{}, err
	}
	return db.CAPINodePoolProvider{
		InfraMachineTemplate:   raw,
		SpireAttestationMethod: db.SpireAttestationMethod(p.SpireAttestationMethod),
		BootstrapFiles:         dbCloudInitFilesFromAPI(sliceValue(p.BootstrapFiles)),
		PreKubeadmCommands:     append([]string(nil), sliceValue(p.PreKubeadmCommands)...),
	}, nil
}

func sliceValue[T any](values *[]T) []T {
	if values == nil {
		return nil
	}
	return *values
}

func apiLaunchLayerCluster(raw json.RawMessage) (*api.LaunchLayerClusterProvider, error) {
	obj, err := objectFromRawManifest(raw)
	if err != nil || validation.LaunchLayerCluster(obj, "launchlayer") != nil {
		return nil, errStoredLaunchLayerIntent
	}
	// Typed writers never author resource metadata; dropping it would hide stored intent.
	if _, ok := obj[launchLayerMetadataField]; ok {
		return nil, errStoredLaunchLayerIntent
	}
	spec, err := json.Marshal(obj[launchLayerSpecField])
	if err != nil {
		return nil, errStoredLaunchLayerIntent
	}
	var out api.LaunchLayerClusterProvider
	if err := json.Unmarshal(spec, &out); err != nil {
		return nil, errStoredLaunchLayerIntent
	}
	return &out, nil
}

func apiLaunchLayerMachine(raw json.RawMessage) (api.LaunchLayerMachine, error) {
	obj, err := objectFromRawManifest(raw)
	if err != nil || validation.LaunchLayerMachineTemplate(obj, "launchlayer.machine") != nil {
		return api.LaunchLayerMachine{}, errStoredLaunchLayerIntent
	}
	if _, ok := obj[launchLayerMetadataField]; ok {
		return api.LaunchLayerMachine{}, errStoredLaunchLayerIntent
	}
	// The pinned validator has checked this complete object chain.
	outer, ok := obj[launchLayerSpecField].(map[string]any)
	if !ok {
		return api.LaunchLayerMachine{}, errStoredLaunchLayerIntent
	}
	template, ok := outer[launchLayerTemplateField].(map[string]any)
	if !ok {
		return api.LaunchLayerMachine{}, errStoredLaunchLayerIntent
	}
	spec := template[launchLayerSpecField]
	rawSpec, err := json.Marshal(spec)
	if err != nil {
		return api.LaunchLayerMachine{}, errStoredLaunchLayerIntent
	}
	var out api.LaunchLayerMachine
	if err := json.Unmarshal(rawSpec, &out); err != nil {
		return api.LaunchLayerMachine{}, errStoredLaunchLayerIntent
	}
	return out, nil
}

func apiLaunchLayerNodeProvider(p *db.CAPINodePoolProvider) (*api.LaunchLayerNodePoolProvider, error) {
	if p == nil {
		return nil, errStoredLaunchLayerIntent
	}
	machine, err := apiLaunchLayerMachine(p.InfraMachineTemplate)
	if err != nil {
		return nil, err
	}
	if validateCAPISpireAttestationMethod(string(p.SpireAttestationMethod)) != nil {
		return nil, errStoredLaunchLayerIntent
	}
	out := &api.LaunchLayerNodePoolProvider{
		Machine: machine, SpireAttestationMethod: api.SpireAttestationMethodCreate(p.SpireAttestationMethod),
		BootstrapFiles:     apiCloudInitFilesFromDB(p.BootstrapFiles),
		PreKubeadmCommands: cloneSlicePtr(p.PreKubeadmCommands),
	}
	if validateCloudInitFiles(sliceValue(out.BootstrapFiles)) != nil {
		return nil, errStoredLaunchLayerIntent
	}
	return out, nil
}

func cloneSlicePtr[T any](values []T) *[]T {
	if values == nil {
		return nil
	}
	out := append([]T{}, values...)
	return &out
}

func apiLaunchLayerControlPlane(p *db.CAPIControlPlaneProvider) (*api.LaunchLayerControlPlaneProvider, error) {
	if p == nil || len(p.InfraMachineTemplatePreferences) == 0 {
		return nil, errStoredLaunchLayerIntent
	}
	if validateCAPISpireAttestationMethod(string(p.SpireAttestationMethod)) != nil {
		return nil, errStoredLaunchLayerIntent
	}
	out := &api.LaunchLayerControlPlaneProvider{
		SpireAttestationMethod: api.SpireAttestationMethodCreate(p.SpireAttestationMethod),
		BootstrapFiles:         apiCloudInitFilesFromDB(p.BootstrapFiles), PreKubeadmCommands: cloneSlicePtr(p.PreKubeadmCommands),
		MachinePreferences: make([]api.LaunchLayerMachine, len(p.InfraMachineTemplatePreferences)),
	}
	if validateCloudInitFiles(sliceValue(out.BootstrapFiles)) != nil {
		return nil, errStoredLaunchLayerIntent
	}
	for i, raw := range p.InfraMachineTemplatePreferences {
		machine, err := apiLaunchLayerMachine(raw)
		if err != nil {
			return nil, err
		}
		out.MachinePreferences[i] = machine
	}
	return out, nil
}
