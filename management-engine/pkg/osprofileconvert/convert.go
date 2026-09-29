// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package osprofileconvert

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	enginev1 "github.com/dsx-ai-factory/nke/management-engine/apis/v1"
)

const osFamilyUbuntu = "ubuntu"

// Catalog is the deterministic release set for one OS profile tree.
// Profiles are ordered by name.
type Catalog struct {
	// Profiles is every converted profile, ordered by name.
	Profiles []Profile
}

// Profile is the three resources that replace one OS profile.
type Profile struct {
	// Name is the OS profile name and the name of each generated resource.
	Name string
	// Deprecated is true when the profile was listed in deprecated.yaml.
	Deprecated bool
	// MachineImage is the boot image for Name.
	MachineImage enginev1.MachineImage
	// ClusterRelease is the control-plane release for Name.
	ClusterRelease enginev1.ClusterRelease
	// NodePoolRelease is the worker release for Name.
	NodePoolRelease enginev1.NodePoolRelease
}

// profileMetadata is one osProfiles index entry. The JSON names match the
// snake_case keys in metadata.yaml, because sigs.k8s.io/yaml decodes through
// encoding/json. image_base_url and bundle_url are intentionally ignored: boot
// URLs live in the iPXE script, and the release CRDs have no field for them.
type profileMetadata struct {
	KubernetesVersion         string `json:"kubernetes_version"`
	KubernetesVersionInjected bool   `json:"kubernetes_version_injected"`
	Architecture              string `json:"architecture"`
	BuildDate                 string `json:"build_date"`
	UbuntuVersion             string `json:"ubuntu_version"`
	Variant                   string `json:"variant"`
}

type metadataIndex struct {
	Profiles map[string]profileMetadata `json:"profiles"`
}

type listedProfile struct {
	name       string
	meta       profileMetadata
	deprecated bool
}

// Convert reads profilesDir/osProfiles and returns one MachineImage, one
// ClusterRelease, and one NodePoolRelease for every profile in metadata.yaml
// and optional deprecated.yaml. The result does not depend on map iteration
// order.
func Convert(profilesDir string) (Catalog, error) {
	if profilesDir == "" {
		return Catalog{}, errors.New("profiles directory is required")
	}
	osDir := filepath.Join(profilesDir, "osProfiles")
	active, err := loadIndex(filepath.Join(osDir, "metadata.yaml"), false)
	if err != nil {
		return Catalog{}, err
	}
	deprecated, err := loadIndex(filepath.Join(osDir, "deprecated.yaml"), true)
	if err != nil {
		return Catalog{}, err
	}

	listed := make([]listedProfile, 0, len(active)+len(deprecated))
	for name, meta := range active {
		listed = append(listed, listedProfile{name: name, meta: meta})
	}
	for name, meta := range deprecated {
		if _, ok := active[name]; ok {
			return Catalog{}, fmt.Errorf("profile %q is listed in both metadata.yaml and deprecated.yaml", name)
		}
		listed = append(listed, listedProfile{name: name, meta: meta, deprecated: true})
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].name < listed[j].name })

	profiles := make([]Profile, 0, len(listed))
	var errs []error
	for _, item := range listed {
		profile, err := convertOne(osDir, item)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		profiles = append(profiles, profile)
	}
	if err := errors.Join(errs...); err != nil {
		return Catalog{}, err
	}
	return Catalog{Profiles: profiles}, nil
}

func loadIndex(path string, optional bool) (map[string]profileMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if optional && errors.Is(err, os.ErrNotExist) {
			return map[string]profileMetadata{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var index metadataIndex
	if err := yaml.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if index.Profiles == nil {
		return map[string]profileMetadata{}, nil
	}
	return index.Profiles, nil
}

func convertOne(osDir string, item listedProfile) (Profile, error) {
	if err := validateProfile(item.name, item.meta); err != nil {
		return Profile{}, err
	}
	ipxe, err := readProfileFile(osDir, item.name, "ipxeScript")
	if err != nil {
		return Profile{}, err
	}
	userData, err := readProfileFile(osDir, item.name, "userData.tmpl")
	if err != nil {
		return Profile{}, err
	}
	if err := validateIPXE(ipxe); err != nil {
		return Profile{}, fmt.Errorf("profile %q: %w", item.name, err)
	}
	if err := validateUserData(userData); err != nil {
		return Profile{}, fmt.Errorf("profile %q: %w", item.name, err)
	}
	return buildProfile(item, ipxe, userData), nil
}

func readProfileFile(osDir, name, file string) (string, error) {
	path := filepath.Join(osDir, name, file)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("profile %q: read %s: %w", name, file, err)
	}
	return string(data), nil
}

func buildProfile(item listedProfile, ipxe, userData string) Profile {
	ref := enginev1.MachineImageReference{Name: enginev1.ResourceName(item.name)}
	init := enginev1.MachineInit{UserDataTemplate: userData}
	lifecycle := enginev1.ReleaseLifecycle{Deprecated: item.deprecated}
	return Profile{
		Name:       item.name,
		Deprecated: item.deprecated,
		MachineImage: enginev1.MachineImage{
			TypeMeta:   typeMeta("MachineImage"),
			ObjectMeta: metav1.ObjectMeta{Name: item.name},
			Spec: enginev1.MachineImageSpec{
				OperatingSystem: enginev1.OperatingSystem{Family: osFamilyUbuntu, Version: item.meta.UbuntuVersion},
				Architecture:    item.meta.Architecture,
				Variant:         item.meta.Variant,
				BuildDate:       item.meta.BuildDate,
				NICo:            enginev1.NICoBoot{IPXEScript: ipxe},
			},
		},
		ClusterRelease: enginev1.ClusterRelease{
			TypeMeta:   typeMeta("ClusterRelease"),
			ObjectMeta: releaseMeta(item.name, item.meta),
			Spec: enginev1.ClusterReleaseSpec{
				MachineImageRef: ref,
				MachineInit:     init,
				Lifecycle:       lifecycle,
			},
		},
		NodePoolRelease: enginev1.NodePoolRelease{
			TypeMeta:   typeMeta("NodePoolRelease"),
			ObjectMeta: releaseMeta(item.name, item.meta),
			Spec: enginev1.NodePoolReleaseSpec{
				MachineImageRef: ref,
				MachineInit:     init,
				Lifecycle:       lifecycle,
			},
		},
	}
}

func typeMeta(kind string) metav1.TypeMeta {
	return metav1.TypeMeta{APIVersion: enginev1.GroupVersion.String(), Kind: kind}
}

func releaseMeta(name string, meta profileMetadata) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name: name,
		Annotations: map[string]string{
			enginev1.MachineInitKubernetesVersionAnnotation:         meta.KubernetesVersion,
			enginev1.MachineInitKubernetesVersionInjectedAnnotation: strconv.FormatBool(meta.KubernetesVersionInjected),
		},
	}
}
