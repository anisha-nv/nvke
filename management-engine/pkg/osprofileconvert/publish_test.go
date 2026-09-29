// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package osprofileconvert_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nke/management-engine/pkg/osprofileconvert"
)

func TestSyncCheckMode(t *testing.T) {
	root := writeTree(t, activeMetadata(), "", map[string][2]string{
		activeName: {testIPXE, testUser},
	})
	catalog := mustConvert(t, root)
	out := t.TempDir()

	if _, err := catalog.Sync(out, osprofileconvert.SyncOptions{Check: true}); !errors.Is(err, osprofileconvert.ErrDrift) {
		t.Fatalf("check of empty output: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "machineimages")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("check mode created output: %v", err)
	}

	if _, err := catalog.Sync(out, osprofileconvert.SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Sync(out, osprofileconvert.SyncOptions{Check: true}); err != nil {
		t.Fatalf("check after write: %v", err)
	}
	result, err := catalog.Sync(out, osprofileconvert.SyncOptions{})
	if err != nil || result.Written != 0 {
		t.Fatalf("second write = %+v, %v", result, err)
	}

	published := readOutput(t, out)
	moveActiveToDeprecated(t, root)
	deprecated := mustConvert(t, root)
	if _, err := deprecated.Sync(out, osprofileconvert.SyncOptions{Check: true}); !errors.Is(err, osprofileconvert.ErrDrift) || errors.Is(err, osprofileconvert.ErrSpecMutation) {
		t.Fatalf("deprecation drift = %v", err)
	}
	if !bytes.Equal(readOutput(t, out)[releaseKey()], published[releaseKey()]) {
		t.Fatal("check mode rewrote a release while reporting drift")
	}

	if _, err := deprecated.Sync(out, osprofileconvert.SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(readOutput(t, out)[releaseKey()], []byte("deprecated: true")) {
		t.Fatal("deprecation write did not set lifecycle.deprecated")
	}
	if !bytes.Equal(readOutput(t, out)[imageKey()], published[imageKey()]) {
		t.Fatal("deprecation write changed the machine image")
	}
	if _, err := deprecated.Sync(out, osprofileconvert.SyncOptions{Check: true}); err != nil {
		t.Fatalf("check after deprecation write: %v", err)
	}
}

func TestSyncRejectsSpecMutation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(t *testing.T, root string)
	}{
		{name: "user data", mutate: func(t *testing.T, root string) {
			t.Helper()
			path := filepath.Join(root, "osProfiles", activeName, "userData.tmpl")
			if err := os.WriteFile(path, []byte("#cloud-config\nhostname: other\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "ipxe script", mutate: func(t *testing.T, root string) {
			t.Helper()
			path := filepath.Join(root, "osProfiles", activeName, "ipxeScript")
			if err := os.WriteFile(path, []byte("#!ipxe\necho changed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "kubernetes version", mutate: func(t *testing.T, root string) {
			t.Helper()
			meta := `profiles:
  nke-1.31.12-251119:
    kubernetes_version: "1.32.0"
    kubernetes_version_injected: false
    architecture: amd64
    build_date: "2025-11-19"
    ubuntu_version: "22.04"
    variant: fde
`
			if err := os.WriteFile(filepath.Join(root, "osProfiles", "metadata.yaml"), []byte(meta), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "build date", mutate: func(t *testing.T, root string) {
			t.Helper()
			meta := `profiles:
  nke-1.31.12-251119:
    kubernetes_version: "1.31.12"
    kubernetes_version_injected: false
    architecture: amd64
    build_date: "2026-01-01"
    ubuntu_version: "22.04"
    variant: fde
`
			if err := os.WriteFile(filepath.Join(root, "osProfiles", "metadata.yaml"), []byte(meta), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := writeTree(t, activeMetadata(), "", map[string][2]string{
				activeName: {testIPXE, testUser},
			})
			out := t.TempDir()
			if _, err := mustConvert(t, root).Sync(out, osprofileconvert.SyncOptions{}); err != nil {
				t.Fatal(err)
			}
			before := readOutput(t, out)
			tt.mutate(t, root)
			_, err := mustConvert(t, root).Sync(out, osprofileconvert.SyncOptions{})
			if !errors.Is(err, osprofileconvert.ErrSpecMutation) {
				t.Fatalf("err = %v", err)
			}
			after := readOutput(t, out)
			for key, body := range before {
				if !bytes.Equal(after[key], body) {
					t.Fatalf("%s changed despite a refused spec mutation", key)
				}
			}
		})
	}
}

func TestSyncSpecMutationWritesNothingElse(t *testing.T) {
	root := writeTree(t, activeMetadata()+`
  nke-260318-22.04-amd64:
    kubernetes_version: ""
    kubernetes_version_injected: true
    architecture: amd64
    build_date: "2026-03-18"
    ubuntu_version: "22.04"
`, "", map[string][2]string{
		activeName: {testIPXE, testUser},
		oldName:    {"#!ipxe\necho deprecated\n", testUser},
	})
	out := t.TempDir()
	if _, err := mustConvert(t, root).Sync(out, osprofileconvert.SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	before := readOutput(t, out)

	// Retire one profile and edit the other's user data. The deprecation would
	// be allowed on its own; the spec edit must keep both files as they were.
	if err := os.WriteFile(filepath.Join(root, "osProfiles", "metadata.yaml"), []byte(`profiles:
  nke-1.31.12-251119:
    kubernetes_version: "1.31.12"
    kubernetes_version_injected: false
    architecture: amd64
    build_date: "2025-11-19"
    ubuntu_version: "22.04"
    variant: fde
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "osProfiles", "deprecated.yaml"), []byte(deprecatedMetadata()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "osProfiles", activeName, "userData.tmpl"), []byte("#cloud-config\nhostname: other\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := mustConvert(t, root).Sync(out, osprofileconvert.SyncOptions{})
	if !errors.Is(err, osprofileconvert.ErrSpecMutation) {
		t.Fatalf("err = %v", err)
	}
	after := readOutput(t, out)
	for key, body := range before {
		if !bytes.Equal(after[key], body) {
			t.Fatalf("%s was written during a refused sync", key)
		}
	}
}

func TestSyncRejectsDroppingPublishedManifest(t *testing.T) {
	root := writeTree(t, activeMetadata(), "", map[string][2]string{
		activeName: {testIPXE, testUser},
	})
	out := t.TempDir()
	if _, err := mustConvert(t, root).Sync(out, osprofileconvert.SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	before := readOutput(t, out)
	if err := os.WriteFile(filepath.Join(root, "osProfiles", "metadata.yaml"), []byte("profiles: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := mustConvert(t, root).Sync(out, osprofileconvert.SyncOptions{Check: true})
	if !errors.Is(err, osprofileconvert.ErrSpecMutation) {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Equal(readOutput(t, out)[imageKey()], before[imageKey()]) {
		t.Fatal("refused removal deleted a published manifest")
	}
}

func imageKey() string {
	return "machineimages/" + activeName + ".yaml"
}

func releaseKey() string {
	return "clusterreleases/" + activeName + ".yaml"
}

func moveActiveToDeprecated(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "osProfiles", "metadata.yaml"), []byte("profiles: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `profiles:
  nke-1.31.12-251119:
    kubernetes_version: "1.31.12"
    kubernetes_version_injected: false
    architecture: amd64
    build_date: "2025-11-19"
    ubuntu_version: "22.04"
    variant: fde
`
	if err := os.WriteFile(filepath.Join(root, "osProfiles", "deprecated.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readOutput(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, dir := range []string{"machineimages", "clusterreleases", "nodepoolreleases"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			key := dir + "/" + entry.Name()
			body, err := os.ReadFile(filepath.Join(root, dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out[key] = body
		}
	}
	return out
}
