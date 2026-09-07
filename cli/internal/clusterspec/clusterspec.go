// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package clusterspec builds the YAML request consumed by "nkectl create cluster".
package clusterspec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/NVIDIA/nke/cli/internal/util"
	api "github.com/NVIDIA/nke/controller/api"
)

const (
	// ProviderCAPD names the local Docker-backed CAPI provider.
	ProviderCAPD = "capd"
	// ProviderNico names the NICo provider payload accepted by CWE.
	ProviderNico = "nico"
)

var (
	dnsLabelRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	semverRE   = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

// Input holds the explicit common and provider-specific values used to build a
// cluster create request.
type Input struct {
	Provider             string
	Name                 string
	KubernetesVersion    string
	CNIPlugin            string
	CNIVersion           string
	EtcdEncrypted        bool
	ControlPlaneCount    int
	WorkerCount          int
	ControlPlaneVIP      string
	SystemReservedMemory string
	Nico                 NicoInput
	CAPD                 CAPDInput
}

// NicoInput holds the identifiers and profiles required for a NICo cluster.
type NicoInput struct {
	OrgID                     string
	SiteID                    string
	VpcID                     string
	VpcPrefixID               string
	SubnetID                  string
	InstanceTypeID            string
	SSHKeyGroupID             string
	OSProfile                 string
	ControlPlaneConfigProfile string
	WorkerConfigProfile       string
}

// CAPDInput holds the local CAPD image used by generated DockerMachineTemplates.
type CAPDInput struct {
	MachineImage string
}

// Build validates input and returns the matching CWE create-cluster request.
func Build(in Input) (*api.CreateClusterRequest, error) {
	if err := validateCommon(in); err != nil {
		return nil, err
	}

	req := &api.CreateClusterRequest{
		Name:                          in.Name,
		ControlPlaneKubernetesVersion: in.KubernetesVersion,
		CniPlugin:                     in.CNIPlugin,
		EtcdEncrypted:                 in.EtcdEncrypted,
		ControlPlaneVIP:               in.ControlPlaneVIP,
	}
	if in.CNIVersion != "" {
		req.CniVersion = &in.CNIVersion
	}

	switch in.Provider {
	case ProviderNico:
		if err := buildNico(req, in); err != nil {
			return nil, err
		}
	case ProviderCAPD:
		if err := buildCAPD(req, in); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported provider %q; supported providers are %s and %s", in.Provider, ProviderNico, ProviderCAPD)
	}
	return req, nil
}

// Render serializes req as YAML and confirms the create command's strict
// decoder accepts the result.
func Render(req *api.CreateClusterRequest) ([]byte, error) {
	jsonBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal cluster spec: %w", err)
	}
	var raw any
	if err := json.Unmarshal(jsonBytes, &raw); err != nil {
		return nil, fmt.Errorf("decode cluster spec: %w", err)
	}
	result, err := yaml.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("render cluster spec: %w", err)
	}
	if _, err := util.YAMLToRequest[api.CreateClusterJSONRequestBody](result); err != nil {
		return nil, fmt.Errorf("rendered cluster spec is not accepted by create cluster: %w", err)
	}
	return result, nil
}

func validateCommon(in Input) error {
	var errs []error
	if !dnsLabelRE.MatchString(in.Name) {
		errs = append(errs, fmt.Errorf("cluster name %q must be a lowercase DNS label", in.Name))
	}
	if !semverRE.MatchString(in.KubernetesVersion) {
		errs = append(errs, fmt.Errorf("kubernetes version %q must be plain semantic version X.Y.Z", in.KubernetesVersion))
	}
	if in.CNIVersion != "" && !semverRE.MatchString(in.CNIVersion) {
		errs = append(errs, fmt.Errorf("CNI version %q must be plain semantic version X.Y.Z", in.CNIVersion))
	}
	if in.CNIPlugin == "" {
		errs = append(errs, errors.New("CNI plugin is required"))
	}
	if in.ControlPlaneCount < 1 {
		errs = append(errs, errors.New("control-plane count must be at least 1"))
	}
	if in.WorkerCount < 0 {
		errs = append(errs, errors.New("worker count cannot be negative"))
	}
	if in.SystemReservedMemory == "" {
		errs = append(errs, errors.New("system reserved memory is required"))
	}
	return errors.Join(errs...)
}

func buildNico(req *api.CreateClusterRequest, in Input) error {
	n := in.Nico
	var errs []error
	for _, field := range []struct{ name, value string }{
		{"nico.orgID", n.OrgID},
		{"nico.siteID", n.SiteID},
		{"nico.vpcID", n.VpcID},
		{"nico.instanceTypeID", n.InstanceTypeID},
		{"nico.sshKeyGroupID", n.SSHKeyGroupID},
		{"nico.osProfile", n.OSProfile},
		{"nodePools.controlPlane.nico.configProfile", n.ControlPlaneConfigProfile},
	} {
		if strings.TrimSpace(field.value) == "" {
			errs = append(errs, fmt.Errorf("%s is required for provider nico", field.name))
		}
	}
	if in.WorkerCount > 0 && strings.TrimSpace(n.WorkerConfigProfile) == "" {
		errs = append(errs, errors.New("nodePools.worker.nico.configProfile is required for provider nico when worker count is nonzero"))
	}
	if n.VpcPrefixID == "" && n.SubnetID == "" {
		errs = append(errs, errors.New("one of nico.vpcPrefixID or nico.subnetID is required for provider nico"))
	}
	if n.VpcPrefixID != "" && n.SubnetID != "" {
		errs = append(errs, errors.New("nico.vpcPrefixID and nico.subnetID are mutually exclusive"))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}

	siteID, err := parseUUID("nico.siteID", n.SiteID)
	if err != nil {
		return err
	}
	vpcID, err := parseUUID("nico.vpcID", n.VpcID)
	if err != nil {
		return err
	}
	instanceTypeID, err := parseUUID("nico.instanceTypeID", n.InstanceTypeID)
	if err != nil {
		return err
	}
	sshKeyGroupID, err := parseUUID("nico.sshKeyGroupID", n.SSHKeyGroupID)
	if err != nil {
		return err
	}

	provider := api.NicoNodePoolProvider{
		InstanceTypeID: instanceTypeID,
		OSProfile:      n.OSProfile,
		SSHKeyGroupIDs: &[]uuid.UUID{sshKeyGroupID},
	}
	if n.VpcPrefixID != "" {
		prefixID, err := parseUUID("nico.vpcPrefixID", n.VpcPrefixID)
		if err != nil {
			return err
		}
		provider.VpcPrefixIDs = []uuid.UUID{prefixID}
	} else {
		subnetID, err := parseUUID("nico.subnetID", n.SubnetID)
		if err != nil {
			return err
		}
		provider.SubnetIDs = []uuid.UUID{subnetID}
	}
	req.Nico = &api.NicoClusterProvider{OrgID: n.OrgID, SiteID: siteID, VpcID: vpcID}
	pools := []api.NodePoolCreateConfig{
		nicoPool("control-plane", "cp", in.ControlPlaneCount, true, in.SystemReservedMemory, provider, n.ControlPlaneConfigProfile),
	}
	if in.WorkerCount > 0 {
		pools = append(pools, nicoPool("worker", "w", in.WorkerCount, false, in.SystemReservedMemory, provider, n.WorkerConfigProfile))
	}
	req.NodePools = &pools
	return nil
}

func nicoPool(name, prefix string, count int, controlPlane bool, memory string, provider api.NicoNodePoolProvider, configProfile string) api.NodePoolCreateConfig {
	provider.ConfigProfile = configProfile
	return api.NodePoolCreateConfig{
		Name:                 name,
		NodeNamePrefix:       prefix,
		DesiredNodeCount:     count,
		ControlPlane:         controlPlane,
		SystemReservedMemory: memory,
		Nico:                 &provider,
	}
}

func buildCAPD(req *api.CreateClusterRequest, in Input) error {
	image := in.CAPD.MachineImage
	if image == "" {
		image = "cwe-capd-kindest-node:local"
	}
	req.Infra = &api.InfraClusterProvider{Cluster: dockerCluster(req.Name)}
	pools := []api.NodePoolCreateConfig{
		capdPool(req.Name, "control-plane", "cp", in.ControlPlaneCount, true, in.SystemReservedMemory, image),
	}
	if in.WorkerCount > 0 {
		pools = append(pools, capdPool(req.Name, "worker", "w", in.WorkerCount, false, in.SystemReservedMemory, image))
	}
	req.NodePools = &pools
	return nil
}

func capdPool(clusterName, name, prefix string, count int, controlPlane bool, memory, image string) api.NodePoolCreateConfig {
	return api.NodePoolCreateConfig{
		Name:                 name,
		NodeNamePrefix:       prefix,
		DesiredNodeCount:     count,
		ControlPlane:         controlPlane,
		SystemReservedMemory: memory,
		Infra: &api.InfraNodePoolProvider{
			SpireAttestationMethod: api.SpireAttestationMethodCreateJoinToken,
			PreKubeadmCommands: []string{
				"mkdir -p /run/cluster-api",
				"echo success > /run/cluster-api/bootstrap-success.complete",
			},
			MachineTemplate: dockerMachineTemplate(clusterName, name, image),
		},
	}
}

func dockerCluster(name string) map[string]any {
	return map[string]any{
		"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta2",
		"kind":       "DockerCluster",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{},
	}
}

func dockerMachineTemplate(clusterName, pool, image string) map[string]any {
	return map[string]any{
		"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta2",
		"kind":       "DockerMachineTemplate",
		"metadata":   map[string]any{"name": clusterName + "-" + pool + "-template"},
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"customImage": image,
					"extraMounts": []any{map[string]any{
						"containerPath": "/var/run/docker.sock",
						"hostPath":      "/var/run/docker.sock",
					}},
				},
			},
		},
	}
}

func parseUUID(flag, value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%s must be a UUID: %w", flag, err)
	}
	return parsed, nil
}

// EqualRequest reports whether rendered YAML decodes to the request it was built from.
func EqualRequest(rendered []byte, want *api.CreateClusterRequest) error {
	got, err := util.YAMLToRequest[api.CreateClusterJSONRequestBody](bytes.TrimSpace(rendered))
	if err != nil {
		return err
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		return errors.New("rendered request differs from built request")
	}
	return nil
}
