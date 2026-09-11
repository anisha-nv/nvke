// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package bootstrapengine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCreate_ConfiguresDockerHubMirror(t *testing.T) {
	runner := &fakeRunner{
		outputs: []fakeOutput{
			{output: "No kind clusters found.\n", err: errors.New("exit status 1")}, // get clusters
			{output: ""}, // create cluster
			{output: ""}, // exec: docker.io hosts.toml
			{output: ""}, // exec: registry-1.docker.io hosts.toml
			{output: ""}, // export kubeconfig
		},
	}
	e := New(runner, nil)

	_, err := e.Create(context.Background(), Config{
		ClusterName:     "test-cluster",
		DockerHubMirror: "mirror.example.com:5000",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if len(runner.calls) != 5 {
		t.Fatalf("got %d calls, want 5: %v", len(runner.calls), runner.calls)
	}

	for i, registry := range []string{"docker.io", "registry-1.docker.io"} {
		call := runner.calls[2+i]
		if call.name != RuntimeCLI {
			t.Errorf("call[%d] binary = %q, want %q", 2+i, call.name, RuntimeCLI)
		}
		args := joinArgs(call.args)
		for _, want := range []string{
			"exec test-cluster-control-plane sh -c",
			"/etc/containerd/certs.d/" + registry + "/hosts.toml",
			`server = "https://mirror.example.com:5000"`,
			`[host."https://mirror.example.com:5000"]`,
		} {
			if !strings.Contains(args, want) {
				t.Errorf("call[%d] args miss %q:\n%s", 2+i, want, args)
			}
		}
	}
	// The mirror is in place before anything else touches the cluster.
	if last := runner.calls[4]; last.name != "kind" || last.args[0] != "export" {
		t.Errorf("call[4] = %s %s, want the kubeconfig export after the mirror setup", last.name, joinArgs(last.args))
	}
}

func TestCreate_NoMirrorMeansNoExec(t *testing.T) {
	runner := &fakeRunner{
		outputs: []fakeOutput{
			{output: "No kind clusters found.\n", err: errors.New("exit status 1")},
			{output: ""},
			{output: ""},
		},
	}
	e := New(runner, nil)
	if _, err := e.Create(context.Background(), Config{ClusterName: "test-cluster"}); err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	for _, call := range runner.calls {
		if call.name != "kind" {
			t.Errorf("unexpected non-kind call without a mirror: %s %s", call.name, joinArgs(call.args))
		}
	}
}

func TestCreate_RejectsAMalformedMirror(t *testing.T) {
	runner := &fakeRunner{}
	e := New(runner, nil)
	_, err := e.Create(context.Background(), Config{
		ClusterName:     "test-cluster",
		DockerHubMirror: "https://mirror.example.com/v2/",
	})
	if err == nil {
		t.Fatal("Create accepted a mirror with a scheme and a path")
	}
	if len(runner.calls) != 0 {
		t.Errorf("Create ran %d commands before rejecting the mirror, want 0", len(runner.calls))
	}
}

func TestDefaultConfig_DockerHubMirrorFromEnv(t *testing.T) {
	t.Setenv("NKECTL_BOOTSTRAP_DOCKERHUB_MIRROR", "mirror.example.com")
	if got := DefaultConfig().DockerHubMirror; got != "mirror.example.com" {
		t.Errorf("DockerHubMirror = %q, want %q", got, "mirror.example.com")
	}
	t.Setenv("NKECTL_BOOTSTRAP_DOCKERHUB_MIRROR", "")
	if got := DefaultConfig().DockerHubMirror; got != "" {
		t.Errorf("DockerHubMirror = %q, want empty when unset", got)
	}
}

// TestCreate_ConfiguresRegistryMirror: one mirror covers every registry the
// bootstrap pulls from, in a fixed order; with a Docker Hub mirror beside it,
// the Docker Hub hosts keep going to that one.
func TestCreate_ConfiguresRegistryMirror(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  Config
		want map[string]string // registry -> mirror it is routed to
	}{
		{
			name: "registry mirror alone covers every registry",
			cfg:  Config{ClusterName: "test-cluster", RegistryMirror: "mirror.example.com:5000"},
			want: map[string]string{
				"docker.io": "mirror.example.com:5000", "registry-1.docker.io": "mirror.example.com:5000",
				"ghcr.io": "mirror.example.com:5000", "registry.k8s.io": "mirror.example.com:5000",
				"quay.io": "mirror.example.com:5000", "cgr.dev": "mirror.example.com:5000",
			},
		},
		{
			name: "docker hub mirror keeps the docker hub hosts",
			cfg:  Config{ClusterName: "test-cluster", RegistryMirror: "mirror.example.com:5000", DockerHubMirror: "hub.example.com"},
			want: map[string]string{
				"docker.io": "hub.example.com", "registry-1.docker.io": "hub.example.com",
				"ghcr.io": "mirror.example.com:5000", "registry.k8s.io": "mirror.example.com:5000",
				"quay.io": "mirror.example.com:5000", "cgr.dev": "mirror.example.com:5000",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			outputs := []fakeOutput{
				{output: "No kind clusters found.\n", err: errors.New("exit status 1")}, // get clusters
				{output: ""}, // create cluster
			}
			for range tt.want {
				outputs = append(outputs, fakeOutput{output: ""}) // one exec per registry
			}
			outputs = append(outputs, fakeOutput{output: ""}) // export kubeconfig
			runner := &fakeRunner{outputs: outputs}

			if _, err := New(runner, nil).Create(context.Background(), tt.cfg); err != nil {
				t.Fatalf("Create returned error: %v", err)
			}
			if len(runner.calls) != len(outputs) {
				t.Fatalf("got %d calls, want %d: %v", len(runner.calls), len(outputs), runner.calls)
			}

			got := map[string]string{}
			for _, call := range runner.calls[2 : 2+len(tt.want)] {
				if call.name != RuntimeCLI {
					t.Fatalf("expected a %s exec, got %s %s", RuntimeCLI, call.name, joinArgs(call.args))
				}
				args := joinArgs(call.args)
				for registry, mirror := range tt.want {
					if strings.Contains(args, "/etc/containerd/certs.d/"+registry+"/hosts.toml") {
						if !strings.Contains(args, `server = "https://`+mirror+`"`) {
							t.Errorf("%s routed elsewhere than %s:\n%s", registry, mirror, args)
						}
						got[registry] = mirror
					}
				}
			}
			for registry := range tt.want {
				if _, ok := got[registry]; !ok {
					t.Errorf("no hosts.toml was written for %s", registry)
				}
			}
			if last := runner.calls[len(runner.calls)-1]; last.name != "kind" || last.args[0] != "export" {
				t.Errorf("last call = %s %s, want the kubeconfig export after the mirror setup", last.name, joinArgs(last.args))
			}
		})
	}
}

func TestCreate_RejectsAMalformedRegistryMirror(t *testing.T) {
	runner := &fakeRunner{}
	_, err := New(runner, nil).Create(context.Background(), Config{
		ClusterName:    "test-cluster",
		RegistryMirror: "https://mirror.example.com/v2/",
	})
	if err == nil {
		t.Fatal("Create accepted a registry mirror with a scheme and a path")
	}
	if !strings.Contains(err.Error(), "RegistryMirror") {
		t.Errorf("error %q does not name the field", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("Create ran %d commands before rejecting the mirror, want 0", len(runner.calls))
	}
}

func TestDefaultConfig_RegistryMirrorFromEnv(t *testing.T) {
	t.Setenv("NKECTL_BOOTSTRAP_REGISTRY_MIRROR", "mirror.example.com")
	if got := DefaultConfig().RegistryMirror; got != "mirror.example.com" {
		t.Errorf("RegistryMirror = %q, want %q", got, "mirror.example.com")
	}
}
