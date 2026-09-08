// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
// SPDX-License-Identifier: Apache-2.0

package clusterspec

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	api "github.com/NVIDIA/nke/controller/api"
)

// Field describes one supported --set path. Description is read from the
// versioned OpenAPI document compiled into the API module.
type Field struct {
	Path        string
	Description string
	Required    bool
	Default     string
	provider    string
	schema      string
	property    string
}

// Fields returns common and provider-specific template fields. Template paths
// intentionally express roles (controlPlane and worker) rather than array
// indexes, while Build expands them into the API's nodePools array.
func Fields(provider string) []Field {
	fields := append([]Field{}, commonFields...)
	switch provider {
	case ProviderNico:
		fields = append(fields, nicoFields...)
	case ProviderCAPD:
		fields = append(fields, capdFields...)
	}
	for i := range fields {
		if fields[i].schema != "" {
			fields[i].Description = openAPIPropertyDescription(fields[i].schema, fields[i].property)
		}
	}
	return fields
}

var commonFields = []Field{
	{Path: "controlPlaneKubernetesVersion", Default: "1.34.2", schema: "CreateClusterRequest", property: "controlPlaneKubernetesVersion"},
	{Path: "cniPlugin", Default: "calico", schema: "CreateClusterRequest", property: "cniPlugin"},
	{Path: "cniVersion", schema: "CreateClusterRequest", property: "cniVersion"},
	{Path: "etcdEncrypted", Default: "false", schema: "CreateClusterRequest", property: "etcdEncrypted"},
	{Path: "controlPlaneVIP", schema: "CreateClusterRequest", property: "controlPlaneVIP"},
	{Path: "nodePools.controlPlane.desiredNodeCount", Default: "1", schema: "NodePoolCreateConfig", property: "desiredNodeCount"},
	{Path: "nodePools.worker.desiredNodeCount", Default: "1", schema: "NodePoolCreateConfig", property: "desiredNodeCount"},
	{Path: "nodePools.controlPlane.systemReservedMemory", Default: "1Gi", schema: "NodePoolCreateConfig", property: "systemReservedMemory"},
	{Path: "nodePools.worker.systemReservedMemory", Default: "1Gi", schema: "NodePoolCreateConfig", property: "systemReservedMemory"},
}

var nicoFields = []Field{
	{Path: "nico.orgID", Required: true, provider: ProviderNico, schema: "NicoClusterProvider", property: "orgID"},
	{Path: "nico.siteID", Required: true, provider: ProviderNico, schema: "NicoClusterProvider", property: "siteID"},
	{Path: "nico.vpcID", Required: true, provider: ProviderNico, schema: "NicoClusterProvider", property: "vpcID"},
	{Path: "nico.vpcPrefixID", provider: ProviderNico, schema: "NicoNodePoolProvider", property: "vpcPrefixIDs"},
	{Path: "nico.subnetID", provider: ProviderNico, schema: "NicoNodePoolProvider", property: "subnetIDs"},
	{Path: "nico.instanceTypeID", Required: true, provider: ProviderNico, schema: "NicoNodePoolProvider", property: "instanceTypeID"},
	{Path: "nico.sshKeyGroupID", Required: true, provider: ProviderNico, schema: "NicoNodePoolProvider", property: "sshKeyGroupIDs"},
	{Path: "nico.osProfile", Required: true, provider: ProviderNico, schema: "NicoNodePoolProvider", property: "osProfile"},
	{Path: "nodePools.controlPlane.nico.configProfile", Required: true, provider: ProviderNico, schema: "NicoNodePoolProvider", property: "configProfile"},
	{Path: "nodePools.worker.nico.configProfile", Required: true, provider: ProviderNico, schema: "NicoNodePoolProvider", property: "configProfile"},
}

var capdFields = []Field{
	{Path: "capd.machineImage", Default: "cwe-capd-kindest-node:local", provider: ProviderCAPD, Description: "Container image for generated CAPD DockerMachineTemplates."},
}

// DefaultInput creates the provider template before --set values are applied.
func DefaultInput(provider, name string) Input {
	return Input{
		Provider: provider, Name: name, KubernetesVersion: "1.34.2", CNIPlugin: "calico",
		ControlPlaneCount: 1, WorkerCount: 1, SystemReservedMemory: "1Gi",
		CAPD: CAPDInput{MachineImage: "cwe-capd-kindest-node:local"},
	}
}

// ApplySets applies template field values. It rejects paths outside the selected
// provider template, so a generated document never contains a mixed provider.
func ApplySets(in *Input, sets []string) error {
	allowed := map[string]Field{}
	for _, field := range Fields(in.Provider) {
		allowed[field.Path] = field
	}
	for _, set := range sets {
		path, value, ok := strings.Cut(set, "=")
		if !ok {
			return fmt.Errorf("--set %q: expected path=value", set)
		}
		if _, ok := allowed[path]; !ok {
			return fmt.Errorf("--set %q: unknown field for provider %s; accepted fields are %s", path, in.Provider, strings.Join(fieldPaths(in.Provider), ", "))
		}
		if err := setInputField(in, path, value); err != nil {
			return fmt.Errorf("--set %s: %w", path, err)
		}
	}
	return nil
}

// MissingRequiredFields reports template fields that must be supplied by a
// prompt or --set. Defaults are not reported as missing.
func MissingRequiredFields(in Input) []Field {
	var missing []Field
	for _, field := range Fields(in.Provider) {
		if field.Required && fieldValue(in, field.Path) == "" {
			missing = append(missing, field)
		}
	}
	if in.Provider == ProviderNico && in.Nico.VpcPrefixID == "" && in.Nico.SubnetID == "" {
		missing = append(missing, Field{Path: "nico.vpcPrefixID", Description: "VPC prefix ID for native networking. Leave blank to provide nico.subnetID instead."})
	}
	return missing
}

func fieldPaths(provider string) []string {
	fields := Fields(provider)
	paths := make([]string, 0, len(fields))
	for _, field := range fields {
		paths = append(paths, field.Path)
	}
	sort.Strings(paths)
	return paths
}

func setInputField(in *Input, path, value string) error {
	switch path {
	case "controlPlaneKubernetesVersion":
		in.KubernetesVersion = value
	case "cniPlugin":
		in.CNIPlugin = value
	case "cniVersion":
		in.CNIVersion = value
	case "etcdEncrypted":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("must be true or false")
		}
		in.EtcdEncrypted = parsed
	case "controlPlaneVIP":
		in.ControlPlaneVIP = value
	case "nodePools.controlPlane.desiredNodeCount":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("must be an integer")
		}
		in.ControlPlaneCount = parsed
	case "nodePools.worker.desiredNodeCount":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("must be an integer")
		}
		in.WorkerCount = parsed
	case "nodePools.controlPlane.systemReservedMemory", "nodePools.worker.systemReservedMemory":
		in.SystemReservedMemory = value
	case "nico.orgID":
		in.Nico.OrgID = value
	case "nico.siteID":
		in.Nico.SiteID = value
	case "nico.vpcID":
		in.Nico.VpcID = value
	case "nico.vpcPrefixID":
		in.Nico.VpcPrefixID = value
	case "nico.subnetID":
		in.Nico.SubnetID = value
	case "nico.instanceTypeID":
		in.Nico.InstanceTypeID = value
	case "nico.sshKeyGroupID":
		in.Nico.SSHKeyGroupID = value
	case "nico.osProfile":
		in.Nico.OSProfile = value
	case "nodePools.controlPlane.nico.configProfile":
		in.Nico.ControlPlaneConfigProfile = value
	case "nodePools.worker.nico.configProfile":
		in.Nico.WorkerConfigProfile = value
	case "capd.machineImage":
		in.CAPD.MachineImage = value
	default:
		return fmt.Errorf("unknown field")
	}
	return nil
}

func fieldValue(in Input, path string) string {
	switch path {
	case "nico.orgID":
		return in.Nico.OrgID
	case "nico.siteID":
		return in.Nico.SiteID
	case "nico.vpcID":
		return in.Nico.VpcID
	case "nico.instanceTypeID":
		return in.Nico.InstanceTypeID
	case "nico.sshKeyGroupID":
		return in.Nico.SSHKeyGroupID
	case "nico.osProfile":
		return in.Nico.OSProfile
	case "nodePools.controlPlane.nico.configProfile":
		return in.Nico.ControlPlaneConfigProfile
	case "nodePools.worker.nico.configProfile":
		if in.WorkerCount == 0 {
			return "set"
		}
		return in.Nico.WorkerConfigProfile
	}
	return "set"
}

func openAPIPropertyDescription(schema, property string) string {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(api.OpenapiYAML), &doc); err != nil {
		return ""
	}
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	definition, _ := schemas[schema].(map[string]any)
	properties, _ := definition["properties"].(map[string]any)
	field, _ := properties[property].(map[string]any)
	description, _ := field["description"].(string)
	return strings.TrimSpace(description)
}
