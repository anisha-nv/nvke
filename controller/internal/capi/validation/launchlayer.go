// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package validation checks supported infrastructure create intents without I/O.
package validation

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	kvalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	kindField                    = "kind"
	usernameField                = "username"
	primaryNetworkInterfaceField = "primaryNetworkInterface"
	namespaceField               = "namespace"
	labelsField                  = "labels"
	apiVersionField              = "apiVersion"
	specField                    = "spec"
	userIDField                  = "userID"
	gpuNetworkInterfaceField     = "gpuNetworkInterface"
	nameField                    = "name"
	annotationsField             = "annotations"
	// LaunchLayerAPIVersion is the supported CAPLL manifest version.
	LaunchLayerAPIVersion = "infrastructure.cluster.x-k8s.io/v1alpha1"
	// LaunchLayerRevision pins the schema and runtime requirements reviewed here.
	LaunchLayerRevision = "80a0d3da5d4af8ebd25cf3d8f897c4779248f50b"
)

type fieldRule struct {
	name  string
	check func(any, string) error
}

// IsLaunchLayer recognizes the kind family, including misspelled case and roles.
// It is a dispatch hint, not acceptance of that GVK or a generic provider catalog.
func IsLaunchLayer(obj map[string]any) bool {
	kind, _ := obj[kindField].(string)
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(kind)), "launchlayer")
}

// LaunchLayerCluster validates a cluster against the pinned create contract.
// It does not default or mutate the input, verify credentials, or contact a backend.
func LaunchLayerCluster(obj map[string]any, path string) error {
	if err := envelope(obj, path, "LaunchLayerCluster"); err != nil {
		return err
	}
	spec, err := object(obj[specField], path+".spec")
	if err != nil {
		return err
	}
	return LaunchLayerClusterSpec(spec, path+".spec")
}

// LaunchLayerClusterSpec validates cluster intent independently of its resource envelope.
func LaunchLayerClusterSpec(spec map[string]any, path string) error {
	if err := fields(spec, path, []fieldRule{
		{"vpcID", nonblank},
		{"orgID", nonblank},
		{"siteID", stringValue},
		{"owner", owner},
		{"cloudProviderEnabled", boolValue},
	}); err != nil {
		return err
	}
	_, vpc := spec["vpcID"]
	_, org := spec["orgID"]
	if vpc == org {
		return invalid(path, "exactly one of vpcID or orgID is required")
	}
	return owner(spec["owner"], path+".owner")
}

// LaunchLayerMachineTemplate validates a template, not an allocated Machine.
// Callers must also validate the parent cluster and enforce the paired GVK.
func LaunchLayerMachineTemplate(obj map[string]any, path string) error {
	if err := envelope(obj, path, "LaunchLayerMachineTemplate"); err != nil {
		return err
	}
	spec, err := object(obj[specField], path+".spec")
	if err != nil {
		return err
	}
	return requiredObjectField(spec, "template", path+".spec", func(value any, path string) error {
		template, err := object(value, path)
		if err != nil {
			return err
		}
		return requiredObjectField(template, specField, path, machineSpec)
	})
}

func machineSpec(value any, path string) error {
	spec, err := object(value, path)
	if err != nil {
		return err
	}
	return LaunchLayerMachineSpec(spec, path)
}

// LaunchLayerMachineSpec validates allocation intent independently of its template envelope.
func LaunchLayerMachineSpec(spec map[string]any, path string) error {
	if err := fields(spec, path, []fieldRule{
		{"instanceType", nonblank},
		{"hostOSProfile", stringValue},
		{"vmProfile", stringValue},
		{"fulfillmentStrategy", strategy},
		{"securityGroups", securityGroups},
	}); err != nil {
		return err
	}
	return nonblank(spec["instanceType"], path+".instanceType")
}

func envelope(obj map[string]any, path, kind string) error {
	if obj[apiVersionField] != LaunchLayerAPIVersion {
		return invalid(path+".apiVersion", "must be "+LaunchLayerAPIVersion)
	}
	if obj[kindField] != kind {
		return invalid(path+".kind", "must be "+kind)
	}
	return fields(obj, path, []fieldRule{
		{apiVersionField, stringValue},
		{kindField, stringValue},
		{"metadata", metadata},
		{specField, objectValue},
	})
}

func requiredObjectField(obj map[string]any, name, path string, check func(any, string) error) error {
	if err := fields(obj, path, []fieldRule{{name, check}}); err != nil {
		return err
	}
	if _, ok := obj[name]; !ok {
		return check(nil, path+"."+name)
	}
	return nil
}

// fields checks declared fields in a deterministic order. Unknown keys and raw
// values are never reflected into errors, including keys containing secrets.
func fields(obj map[string]any, path string, rules []fieldRule) error {
	known := 0
	for _, rule := range rules {
		if value, exists := obj[rule.name]; exists {
			known++
			if err := rule.check(value, path+"."+rule.name); err != nil {
				return err
			}
		}
	}
	if known != len(obj) {
		return invalid(path, "contains an unsupported field")
	}
	return nil
}

func owner(value any, path string) error {
	obj, err := object(value, path)
	if err != nil {
		return err
	}
	if err := fields(obj, path, []fieldRule{{userIDField, identity}, {usernameField, identity}}); err != nil {
		return err
	}
	for _, key := range []string{userIDField, usernameField} {
		if err := identity(obj[key], path+"."+key); err != nil {
			return err
		}
	}
	return nil
}

func identity(value any, path string) error {
	if err := nonblank(value, path); err != nil {
		return err
	}
	str, _ := value.(string) // nonblank already checked the type.
	if utf8.RuneCountInString(str) > 253 {
		return invalid(path, "must contain at most 253 characters")
	}
	return nil
}

func object(value any, path string) (map[string]any, error) {
	obj, ok := value.(map[string]any)
	if !ok || obj == nil {
		return nil, invalid(path, "required object")
	}
	return obj, nil
}

func objectValue(value any, path string) error {
	_, err := object(value, path)
	return err
}

func stringValue(value any, path string) error {
	if _, ok := value.(string); !ok {
		return invalid(path, "must be a string")
	}
	return nil
}

func nonblank(value any, path string) error {
	str, ok := value.(string)
	if !ok || strings.TrimSpace(str) == "" {
		return invalid(path, "required nonblank string")
	}
	return nil
}

func boolValue(value any, path string) error {
	if _, ok := value.(bool); !ok {
		return invalid(path, "must be a boolean")
	}
	return nil
}

func strategy(value any, path string) error {
	switch value {
	case "BEST_EFFORT", "ALL_OR_NOTHING", "WAIT_FOR_RESOURCE":
		return nil
	default:
		return invalid(path, "must be BEST_EFFORT, ALL_OR_NOTHING, or WAIT_FOR_RESOURCE")
	}
}

func securityGroups(value any, path string) error {
	obj, err := object(value, path)
	if err != nil {
		return err
	}
	return fields(obj, path, []fieldRule{
		{primaryNetworkInterfaceField, stringArray}, {gpuNetworkInterfaceField, stringArray},
	})
}

func stringArray(value any, path string) error {
	values, ok := value.([]any)
	if !ok {
		return invalid(path, "must be an array of strings")
	}
	for i, entry := range values {
		if err := stringValue(entry, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func metadata(value any, path string) error {
	obj, err := object(value, path)
	if err != nil {
		return err
	}
	return fields(obj, path, []fieldRule{
		{nameField, resourceName},
		{namespaceField, resourceNamespace},
		{labelsField, labels},
		{annotationsField, annotations},
	})
}

func resourceName(value any, path string) error {
	str, ok := value.(string)
	if !ok || len(kvalidation.IsDNS1123Subdomain(str)) != 0 {
		return invalid(path, "must be a Kubernetes resource name")
	}
	return nil
}

func resourceNamespace(value any, path string) error {
	str, ok := value.(string)
	if !ok || len(kvalidation.IsDNS1123Label(str)) != 0 {
		return invalid(path, "must be a Kubernetes namespace name")
	}
	return nil
}

func labels(value any, path string) error {
	return metadataMap(value, path, false)
}

func annotations(value any, path string) error {
	return metadataMap(value, path, true)
}

func metadataMap(value any, path string, annotation bool) error {
	obj, err := object(value, path)
	if err != nil {
		return err
	}
	total := 0
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		raw := obj[key]
		str, ok := raw.(string)
		qualifiedKey := key
		if annotation {
			qualifiedKey = strings.ToLower(key)
		}
		if !ok || len(kvalidation.IsQualifiedName(qualifiedKey)) != 0 {
			return invalid(path, "must contain valid Kubernetes keys and string values")
		}
		if !annotation && len(kvalidation.IsValidLabelValue(str)) != 0 {
			return invalid(path, "must contain valid Kubernetes label values")
		}
		total += len(key) + len(str)
	}
	if annotation && total > 256*1024 {
		return invalid(path, "must not exceed 256 KiB")
	}
	return nil
}

func invalid(path, reason string) error {
	return fmt.Errorf("%s: %s", path, reason)
}
