// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cwedeploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInstallCAPI_InstallsCoreThenTheProvider pins the two things that are easy
// to get wrong and impossible to see afterwards: clusterctl is asked for all
// three upstream providers at the build-supplied version, and the provisioned
// provider's placeholder image is replaced with the one the caller built.
func TestInstallCAPI_InstallsCoreThenTheProvider(t *testing.T) {
	const buildCAPIVersion = "v-test-build-version"
	previousCAPIVersion := capiVersion
	capiVersion = buildCAPIVersion
	t.Cleanup(func() { capiVersion = previousCAPIVersion })

	runner := &fakeRunner{}
	cfg := testConfig(t)

	if err := New(runner, nil).installCAPI(context.Background(), "/tmp/kubeconfig", cfg, time.Minute); err != nil {
		t.Fatalf("installCAPI returned error: %v", err)
	}

	init := ""
	var applied []string
	for _, call := range runner.calls {
		args := joinArgs(call.args)
		switch {
		case call.name == "clusterctl":
			init = args
		case call.name == "kubectl" && strings.Contains(args, "apply"):
			applied = append(applied, args)
		}
	}

	for _, want := range []string{
		"--core cluster-api:" + buildCAPIVersion,
		"--bootstrap kubeadm:" + buildCAPIVersion,
		"--control-plane kubeadm:" + buildCAPIVersion,
		"--kubeconfig /tmp/kubeconfig",
		"--wait-providers",
	} {
		if !strings.Contains(init, want) {
			t.Errorf("clusterctl init = %q, want it to contain %q", init, want)
		}
	}

	if len(applied) != 1 {
		t.Fatalf("expected one kubectl apply for the provider, got %d: %v", len(applied), applied)
	}
	// Server-side: the provider's CRDs are large enough that a client-side
	// apply can blow the last-applied-configuration annotation limit.
	if !strings.Contains(applied[0], "--server-side") {
		t.Errorf("provider apply = %q, want a server-side apply", applied[0])
	}
}

// TestInstallProvisionedProvider_RewritesTheImage checks the substitution
// itself, on the rendered bytes rather than on the command line: the image is
// inside the manifest, so an apply that "succeeded" with the placeholder still
// runs a provider image that does not exist.
func TestInstallProvisionedProvider_RewritesTheImage(t *testing.T) {
	runner := &fakeRunner{}
	cfg := testConfig(t)
	cfg.ProvisionedProviderImage = "ghcr.io/nvidia/nke/cluster-api-provider-provisioned:v0.1.0"

	if err := New(runner, nil).installProvisionedProvider(
		context.Background(), "/tmp/kubeconfig", cfg, time.Minute); err != nil {
		t.Fatalf("installProvisionedProvider returned error: %v", err)
	}

	if len(runner.applied) != 1 {
		t.Fatalf("expected one applied manifest, got %d", len(runner.applied))
	}
	manifest := runner.applied[0]
	if !strings.Contains(manifest, cfg.ProvisionedProviderImage) {
		t.Errorf("applied manifest does not run the built image %q:\n%s",
			cfg.ProvisionedProviderImage, manifest)
	}
	if strings.Contains(manifest, provisionedDefaultImage) {
		t.Errorf("applied manifest still carries the placeholder image %q:\n%s",
			provisionedDefaultImage, manifest)
	}
}

// TestInstallProvisionedProvider_RejectsAnUnexpectedRender is the guard for the
// substitution silently doing nothing. If the provider's manager manifest stops
// carrying the placeholder image, a blind ReplaceAll leaves the rendered YAML
// unchanged and the cluster runs an image nobody published — with the install
// reporting success.
func TestInstallProvisionedProvider_RejectsAnUnexpectedRender(t *testing.T) {
	runner := &fakeRunner{outputs: []fakeOutput{
		{output: "image: some.registry/something-else:v1\n"},
	}}
	cfg := testConfig(t)

	err := New(runner, nil).installProvisionedProvider(
		context.Background(), "/tmp/kubeconfig", cfg, time.Minute)
	if err == nil {
		t.Fatal("expected an error when the render does not contain the placeholder image")
	}
	if !strings.Contains(err.Error(), provisionedDefaultImage) {
		t.Errorf("error %q does not name the image it looked for", err)
	}
}

// TestInstallCAPICore_SkipsWhenAlreadyInstalled: clusterctl refuses a second
// init, so a re-run against a kept bootstrap cluster has to probe first.
func TestInstallCAPICore_SkipsWhenAlreadyInstalled(t *testing.T) {
	runner := &fakeRunner{outputs: []fakeOutput{{output: "deployment.apps/capi-controller-manager"}}}

	if err := New(runner, nil).installCAPICore(context.Background(), "/tmp/kubeconfig", Config{}); err != nil {
		t.Fatalf("installCAPICore returned error: %v", err)
	}
	for _, call := range runner.calls {
		if call.name == "clusterctl" {
			t.Errorf("clusterctl was run against a cluster that already has CAPI: %s", joinArgs(call.args))
		}
	}
}

// TestDeploy_InstallsCAPIBeforeCWE is the ordering that matters: CWE starts
// writing CAPI objects as soon as a cluster is created, and an object written
// before its controller exists just sits there.
func TestDeploy_InstallsCAPIBeforeCWE(t *testing.T) {
	runner := &fakeRunner{}
	if err := New(runner, nil).Deploy(context.Background(), "/tmp/kubeconfig", testConfig(t)); err != nil {
		t.Fatalf("Deploy returned error: %v", err)
	}

	capi := callIndex(runner, "--core cluster-api:")
	provider := callIndex(runner, "--server-side")
	cwe := callIndex(runner, "upgrade --install "+cweReleaseName)

	if capi < 0 || provider < 0 || cwe < 0 {
		t.Fatalf("missing a step: capi=%d provider=%d cwe=%d; calls were %+v",
			capi, provider, cwe, runner.calls)
	}
	if capi > cwe || provider > cwe {
		t.Errorf("CAPI (%d) and the provider (%d) must be installed before CWE (%d)", capi, provider, cwe)
	}
}

// stageCAPILocalRepo lays out a clusterctl local repository under a temp
// directory with empty files, which is all clusterctlLocalRepoConfig looks at.
// An empty certManagerVersion stages no cert-manager.
func stageCAPILocalRepo(t *testing.T, version, certManagerVersion string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range capiLocalRepoProviders() {
		stageFile(t, filepath.Join(root, p.label, version, p.components))
		stageFile(t, filepath.Join(root, p.label, version, capiMetadataFile))
	}
	if certManagerVersion != "" {
		stageFile(t, filepath.Join(root, certManagerLabel, certManagerVersion, certManagerComponents))
	}
	return root
}

func stageFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestClusterctlLocalRepoConfig pins the whole rendered configuration: the
// provider names and types have to match clusterctl's built-ins to replace
// them, the labels have to match clusterctl's ManifestLabel for the file://
// URL to parse, and cert-manager has to carry an explicit version.
func TestClusterctlLocalRepoConfig(t *testing.T) {
	root := stageCAPILocalRepo(t, "v1.2.3", "v9.8.7")

	got, err := clusterctlLocalRepoConfig(root, "v1.2.3")
	if err != nil {
		t.Fatalf("clusterctlLocalRepoConfig returned error: %v", err)
	}
	want := "providers:\n" +
		"- name: cluster-api\n  type: CoreProvider\n  url: file://" + root + "/cluster-api/v1.2.3/core-components.yaml\n" +
		"- name: kubeadm\n  type: BootstrapProvider\n  url: file://" + root + "/bootstrap-kubeadm/v1.2.3/bootstrap-components.yaml\n" +
		"- name: kubeadm\n  type: ControlPlaneProvider\n  url: file://" + root + "/control-plane-kubeadm/v1.2.3/control-plane-components.yaml\n" +
		"cert-manager:\n  url: file://" + root + "/cert-manager/v9.8.7/cert-manager.yaml\n  version: v9.8.7\n"
	if string(got) != want {
		t.Errorf("clusterctl config =\n%s\nwant\n%s", got, want)
	}
}

// An incomplete repository is reported by the path clusterctl would have
// looked for, before clusterctl runs. Each case removes or misplaces one
// thing a staging step could get wrong.
func TestClusterctlLocalRepoConfig_RejectsAnIncompleteRepository(t *testing.T) {
	for _, tt := range []struct {
		name  string
		stage func(t *testing.T) string
		want  string
	}{
		{
			name: "components file missing",
			stage: func(t *testing.T) string {
				root := stageCAPILocalRepo(t, "v1.2.3", "v9.8.7")
				if err := os.Remove(filepath.Join(root, "bootstrap-kubeadm", "v1.2.3", "bootstrap-components.yaml")); err != nil {
					t.Fatal(err)
				}
				return root
			},
			want: "bootstrap-kubeadm/v1.2.3/bootstrap-components.yaml",
		},
		{
			name: "metadata missing",
			stage: func(t *testing.T) string {
				root := stageCAPILocalRepo(t, "v1.2.3", "v9.8.7")
				if err := os.Remove(filepath.Join(root, "cluster-api", "v1.2.3", capiMetadataFile)); err != nil {
					t.Fatal(err)
				}
				return root
			},
			want: "cluster-api/v1.2.3/metadata.yaml",
		},
		{
			name:  "staged at a different version than the build installs",
			stage: func(t *testing.T) string { return stageCAPILocalRepo(t, "v1.2.4", "v9.8.7") },
			want:  "cluster-api/v1.2.3/core-components.yaml",
		},
		{
			name:  "no cert-manager",
			stage: func(t *testing.T) string { return stageCAPILocalRepo(t, "v1.2.3", "") },
			want:  "cert-manager/<version>/cert-manager.yaml",
		},
		{
			name: "two cert-manager versions",
			stage: func(t *testing.T) string {
				root := stageCAPILocalRepo(t, "v1.2.3", "v9.8.7")
				stageFile(t, filepath.Join(root, certManagerLabel, "v9.8.8", certManagerComponents))
				return root
			},
			want: "more than one cert-manager version",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := tt.stage(t)
			_, err := clusterctlLocalRepoConfig(root, "v1.2.3")
			if err == nil {
				t.Fatal("clusterctlLocalRepoConfig accepted an incomplete repository")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not name %q", err, tt.want)
			}
		})
	}
}

// TestInstallCAPICore_ReadsFromTheLocalRepository: with a local repository
// clusterctl gets a --config whose contents point at it; without one the
// invocation is unchanged and no configuration file is written.
func TestInstallCAPICore_ReadsFromTheLocalRepository(t *testing.T) {
	previousCAPIVersion := capiVersion
	capiVersion = "v1.2.3"
	t.Cleanup(func() { capiVersion = previousCAPIVersion })

	for _, tt := range []struct {
		name       string
		repo       func(t *testing.T) string
		wantConfig bool
	}{
		{name: "default fetches from GitHub", repo: func(*testing.T) string { return "" }},
		{name: "local repository is handed to clusterctl", repo: func(t *testing.T) string { return stageCAPILocalRepo(t, "v1.2.3", "v9.8.7") }, wantConfig: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{}
			repo := tt.repo(t)
			if err := New(runner, nil).installCAPICore(context.Background(), "/tmp/kubeconfig", Config{CAPILocalRepo: repo}); err != nil {
				t.Fatalf("installCAPICore returned error: %v", err)
			}

			init := ""
			for _, call := range runner.calls {
				if call.name == "clusterctl" {
					init = joinArgs(call.args)
				}
			}
			if !strings.Contains(init, "init --kubeconfig /tmp/kubeconfig --core cluster-api:v1.2.3") {
				t.Errorf("clusterctl init = %q, want the same init as before", init)
			}
			if got := strings.Contains(init, " --config "); got != tt.wantConfig {
				t.Errorf("clusterctl init = %q, has --config = %v, want %v", init, got, tt.wantConfig)
			}
			if !tt.wantConfig {
				if len(runner.applied) != 0 {
					t.Errorf("a configuration file was written without a local repository: %q", runner.applied)
				}
				return
			}
			if len(runner.applied) != 1 {
				t.Fatalf("expected the clusterctl configuration to be captured, got %d files", len(runner.applied))
			}
			for _, want := range []string{
				"url: file://" + repo + "/cluster-api/v1.2.3/core-components.yaml",
				"url: file://" + repo + "/cert-manager/v9.8.7/cert-manager.yaml",
				"version: v9.8.7",
			} {
				if !strings.Contains(runner.applied[0], want) {
					t.Errorf("clusterctl configuration lacks %q:\n%s", want, runner.applied[0])
				}
			}
		})
	}
}
