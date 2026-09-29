// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package osprofileconvert_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"

	enginev1 "github.com/dsx-ai-factory/nke/management-engine/apis/v1"
	"github.com/dsx-ai-factory/nke/management-engine/pkg/osprofileconvert"
)

const (
	activeName = "nke-1.31.12-251119"
	oldName    = "nke-260318-22.04-amd64"

	versionAnnotation  = "internal.nke.nvidia.com/machine-init-kubernetes-version"
	injectedAnnotation = "internal.nke.nvidia.com/machine-init-kubernetes-version-injected"

	testIPXE = "#!ipxe\nboot\n"
	testUser = "#cloud-config\nhostname: {{ .Hostname }}\n"
)

func TestConvertActiveProfile(t *testing.T) {
	if enginev1.MachineInitKubernetesVersionAnnotation != versionAnnotation ||
		enginev1.MachineInitKubernetesVersionInjectedAnnotation != injectedAnnotation {
		t.Fatal("kubernetes version annotations changed prefix or name")
	}
	root := writeTree(t, activeMetadata(), "", map[string][2]string{
		activeName: {testIPXE, testUser},
	})

	catalog := mustConvert(t, root)
	again := mustConvert(t, root)
	first, err := catalog.Manifests()
	if err != nil {
		t.Fatal(err)
	}
	second, err := again.Manifests()
	if err != nil {
		t.Fatal(err)
	}
	if !manifestsEqual(first, second) {
		t.Fatal("rendering the same profile tree twice produced different YAML")
	}
	if len(catalog.Profiles) != 1 {
		t.Fatalf("profiles = %d, want 1", len(catalog.Profiles))
	}

	profile := catalog.Profiles[0]
	if profile.Name != activeName || profile.Deprecated {
		t.Fatalf("profile = %+v, want active %s", profile.Name, activeName)
	}
	image := profile.MachineImage
	if image.Name != activeName || image.Kind != "MachineImage" || image.APIVersion != enginev1.GroupVersion.String() {
		t.Fatalf("machine image identity = %s %s %s", image.APIVersion, image.Kind, image.Name)
	}
	if image.Spec.OperatingSystem != (enginev1.OperatingSystem{Family: "ubuntu", Version: "22.04"}) {
		t.Fatalf("operating system = %+v", image.Spec.OperatingSystem)
	}
	if image.Spec.Architecture != "amd64" || image.Spec.Variant != "fde" || image.Spec.BuildDate != "2025-11-19" {
		t.Fatalf("image identity fields = %+v", image.Spec)
	}
	if image.Spec.NICo.IPXEScript != testIPXE {
		t.Fatalf("ipxe = %q", image.Spec.NICo.IPXEScript)
	}
	if len(image.Annotations) != 0 {
		t.Fatalf("machine image annotations = %v, want none", image.Annotations)
	}

	if profile.ClusterRelease.Name != activeName || profile.NodePoolRelease.Name != activeName {
		t.Fatal("release names must match the profile name")
	}
	for _, got := range []struct {
		kind       string
		deprecated bool
		userData   string
		version    string
		injected   string
		ref        enginev1.ResourceName
	}{
		{
			kind: profile.ClusterRelease.Kind, deprecated: profile.ClusterRelease.Spec.Lifecycle.Deprecated,
			userData: profile.ClusterRelease.Spec.MachineInit.UserDataTemplate,
			version:  profile.ClusterRelease.Annotations[versionAnnotation],
			injected: profile.ClusterRelease.Annotations[injectedAnnotation],
			ref:      profile.ClusterRelease.Spec.MachineImageRef.Name,
		},
		{
			kind: profile.NodePoolRelease.Kind, deprecated: profile.NodePoolRelease.Spec.Lifecycle.Deprecated,
			userData: profile.NodePoolRelease.Spec.MachineInit.UserDataTemplate,
			version:  profile.NodePoolRelease.Annotations[versionAnnotation],
			injected: profile.NodePoolRelease.Annotations[injectedAnnotation],
			ref:      profile.NodePoolRelease.Spec.MachineImageRef.Name,
		},
	} {
		if got.deprecated {
			t.Fatalf("%s deprecated = true, want false", got.kind)
		}
		if got.userData != testUser || got.ref != activeName || got.version != "1.31.12" || got.injected != "false" {
			t.Fatalf("%s release = %+v", got.kind, got)
		}
	}
	if profile.ClusterRelease.Kind != "ClusterRelease" || profile.NodePoolRelease.Kind != "NodePoolRelease" {
		t.Fatalf("kinds = %s %s", profile.ClusterRelease.Kind, profile.NodePoolRelease.Kind)
	}

	wantKeys := []string{
		"machineimages/" + activeName + ".yaml",
		"clusterreleases/" + activeName + ".yaml",
		"nodepoolreleases/" + activeName + ".yaml",
	}
	if len(first) != len(wantKeys) {
		t.Fatalf("manifests = %d, want %d", len(first), len(wantKeys))
	}
	for _, key := range wantKeys {
		body := first[key]
		if !bytes.HasPrefix(body, []byte("# SPDX-FileCopyrightText:")) {
			t.Fatalf("%s missing license header", key)
		}
		if key == wantKeys[0] {
			var decoded enginev1.MachineImage
			if err := yaml.Unmarshal(body, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Spec.NICo.IPXEScript != testIPXE {
				t.Fatalf("yaml round trip ipxe = %q", decoded.Spec.NICo.IPXEScript)
			}
			continue
		}
		var decoded enginev1.ClusterRelease
		if err := yaml.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Spec.MachineInit.UserDataTemplate != testUser || decoded.Spec.Lifecycle.Deprecated {
			t.Fatalf("yaml round trip %s user data = %q deprecated = %v", key, decoded.Spec.MachineInit.UserDataTemplate, decoded.Spec.Lifecycle.Deprecated)
		}
		if decoded.Annotations[versionAnnotation] != "1.31.12" || decoded.Annotations[injectedAnnotation] != "false" {
			t.Fatalf("yaml round trip %s annotations = %v", key, decoded.Annotations)
		}
	}
}

func TestConvertDeprecatedProfile(t *testing.T) {
	const ipxe = "#!ipxe\necho deprecated\n"
	const userData = "#cloud-config\nhostname: {{ .Hostname }}\n"
	root := writeTree(t, "profiles: {}\n", deprecatedMetadata(), map[string][2]string{
		oldName: {ipxe, userData},
	})

	catalog := mustConvert(t, root)
	if len(catalog.Profiles) != 1 {
		t.Fatalf("profiles = %d, want 1", len(catalog.Profiles))
	}
	profile := catalog.Profiles[0]
	if profile.Name != oldName || !profile.Deprecated {
		t.Fatalf("profile %s deprecated=%v", profile.Name, profile.Deprecated)
	}
	if !profile.ClusterRelease.Spec.Lifecycle.Deprecated || !profile.NodePoolRelease.Spec.Lifecycle.Deprecated {
		t.Fatal("deprecated profile must set lifecycle.deprecated on both releases")
	}
	if profile.ClusterRelease.Annotations[injectedAnnotation] != "true" || profile.ClusterRelease.Annotations[versionAnnotation] != "" {
		t.Fatalf("annotations = %v", profile.ClusterRelease.Annotations)
	}
	if profile.MachineImage.Spec.Variant != "" || profile.MachineImage.Spec.Architecture != "amd64" {
		t.Fatalf("image = %+v", profile.MachineImage.Spec)
	}
	if profile.ClusterRelease.Spec.MachineImageRef.Name != oldName || profile.NodePoolRelease.Spec.MachineImageRef.Name != oldName {
		t.Fatal("release image ref must keep the profile name")
	}
	files, err := catalog.Manifests()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"clusterreleases/" + oldName + ".yaml", "nodepoolreleases/" + oldName + ".yaml"} {
		if !bytes.Contains(files[key], []byte("deprecated: true")) {
			t.Fatalf("%s does not mark the release deprecated:\n%s", key, files[key])
		}
	}
}

func TestConvertReadsBothIndexesInNameOrder(t *testing.T) {
	root := writeTree(t, activeMetadata(), deprecatedMetadata(), map[string][2]string{
		activeName: {testIPXE, testUser},
		oldName:    {"#!ipxe\necho deprecated\n", testUser},
	})
	catalog := mustConvert(t, root)
	if len(catalog.Profiles) != 2 {
		t.Fatalf("profiles = %d, want 2", len(catalog.Profiles))
	}
	if catalog.Profiles[0].Name != activeName || catalog.Profiles[1].Name != oldName {
		t.Fatalf("order = %s, %s", catalog.Profiles[0].Name, catalog.Profiles[1].Name)
	}
	if catalog.Profiles[0].Deprecated || !catalog.Profiles[1].Deprecated {
		t.Fatal("deprecation state was not taken from the index that listed the profile")
	}
}

func TestConvertPreservesScriptBytes(t *testing.T) {
	const ipxe = "#!ipxe\nboot"
	const userData = "#!/bin/bash\nhostnamectl set-hostname {{ .Hostname }}"
	root := writeTree(t, activeMetadata(), "", map[string][2]string{
		activeName: {ipxe, userData},
	})
	files, err := mustConvert(t, root).Manifests()
	if err != nil {
		t.Fatal(err)
	}
	var image enginev1.MachineImage
	if err := yaml.Unmarshal(files["machineimages/"+activeName+".yaml"], &image); err != nil {
		t.Fatal(err)
	}
	if image.Spec.NICo.IPXEScript != ipxe {
		t.Fatalf("ipxe = %q, want %q", image.Spec.NICo.IPXEScript, ipxe)
	}
	var release enginev1.NodePoolRelease
	if err := yaml.Unmarshal(files["nodepoolreleases/"+activeName+".yaml"], &release); err != nil {
		t.Fatal(err)
	}
	if release.Spec.MachineInit.UserDataTemplate != userData {
		t.Fatalf("user data = %q, want %q", release.Spec.MachineInit.UserDataTemplate, userData)
	}
}

func TestConvertRejectsBadInput(t *testing.T) {
	t.Run("duplicate name", func(t *testing.T) {
		root := writeTree(t, activeMetadata(), activeAsDeprecated(), map[string][2]string{
			activeName: {testIPXE, testUser},
		})
		_, err := osprofileconvert.Convert(root)
		if err == nil || !bytes.Contains([]byte(err.Error()), []byte("both metadata.yaml and deprecated.yaml")) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing ipxe script", func(t *testing.T) {
		root := writeTree(t, activeMetadata(), "", nil)
		_, err := osprofileconvert.Convert(root)
		if err == nil || !bytes.Contains([]byte(err.Error()), []byte("ipxeScript")) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("ipxe without magic", func(t *testing.T) {
		root := writeTree(t, activeMetadata(), "", map[string][2]string{
			activeName: {"boot\n", testUser},
		})
		_, err := osprofileconvert.Convert(root)
		if err == nil || !bytes.Contains([]byte(err.Error()), []byte("#!ipxe")) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing ubuntu version", func(t *testing.T) {
		meta := `profiles:
  nke-1.31.12-251119:
    kubernetes_version: "1.31.12"
    architecture: amd64
    build_date: "2025-11-19"
`
		root := writeTree(t, meta, "", map[string][2]string{
			activeName: {testIPXE, testUser},
		})
		_, err := osprofileconvert.Convert(root)
		if err == nil || !bytes.Contains([]byte(err.Error()), []byte("ubuntu_version")) {
			t.Fatalf("err = %v", err)
		}
	})
}

func activeMetadata() string {
	return `profiles:
  nke-1.31.12-251119:
    kubernetes_version: "1.31.12"
    kubernetes_version_injected: false
    architecture: amd64
    build_date: "2025-11-19"
    ubuntu_version: "22.04"
    variant: fde
    image_base_url: "https://example.test/boot"
    bundle_url: "https://example.test/bundle"
`
}

func deprecatedMetadata() string {
	return `profiles:
  nke-260318-22.04-amd64:
    kubernetes_version: ""
    kubernetes_version_injected: true
    architecture: amd64
    build_date: "2026-03-18"
    ubuntu_version: "22.04"
`
}

func activeAsDeprecated() string {
	return `profiles:
  nke-1.31.12-251119:
    kubernetes_version: "1.31.12"
    architecture: amd64
    build_date: "2025-11-19"
    ubuntu_version: "22.04"
`
}

func writeTree(t *testing.T, metadata, deprecated string, files map[string][2]string) string {
	t.Helper()
	root := t.TempDir()
	osDir := filepath.Join(root, "osProfiles")
	if err := os.MkdirAll(osDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(osDir, "metadata.yaml"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}
	if deprecated != "" {
		if err := os.WriteFile(filepath.Join(osDir, "deprecated.yaml"), []byte(deprecated), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, pair := range files {
		dir := filepath.Join(osDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ipxeScript"), []byte(pair[0]), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "userData.tmpl"), []byte(pair[1]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func mustConvert(t *testing.T, root string) osprofileconvert.Catalog {
	t.Helper()
	catalog, err := osprofileconvert.Convert(root)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	return catalog
}

func manifestsEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for key, body := range a {
		if !bytes.Equal(body, b[key]) {
			return false
		}
	}
	return true
}
