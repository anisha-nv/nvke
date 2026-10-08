// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testgrid

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSetRejectsWhatTheWorkflowWouldNotAccept(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr error
	}{
		{name: "known key and plain value", key: KeyGPU, value: "gb200", wantErr: nil},
		{name: "version", key: KeyKubernetesVersion, value: "v1.36.3", wantErr: nil},
		{name: "empty value is ignored, not an error", key: KeySite, value: "", wantErr: nil},
		{name: "unknown key", key: "run_url", value: "x", wantErr: ErrUnknownKey},
		{name: "newline could start a log line", key: KeySite, value: "ytl\n::error::x", wantErr: ErrInvalidValue},
		{name: "path syntax", key: KeySite, value: "a/b", wantErr: ErrInvalidValue},
		{name: "equals sign would split the line", key: KeySite, value: "a=b", wantErr: ErrInvalidValue},
		{name: "space", key: KeyGPUProduct, value: "NVIDIA GB200", wantErr: ErrInvalidValue},
		{name: "too long", key: KeySite, value: strings.Repeat("a", 129), wantErr: ErrInvalidValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewCollector().Set(tt.key, tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Set(%q, %q) = %v, want %v", tt.key, tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestSetKeepsAnEarlierValueWhenTheLaterOneIsEmpty(t *testing.T) {
	c := NewCollector()
	mustSet(t, c, KeyKubernetesVersion, "1.36.3")
	mustSet(t, c, KeyKubernetesVersion, "")
	if got, _ := c.Get(KeyKubernetesVersion); got != "1.36.3" {
		t.Fatalf("an empty value replaced the declared one: got %q", got)
	}
	mustSet(t, c, KeyKubernetesVersion, "v1.36.3")
	if got, _ := c.Get(KeyKubernetesVersion); got != "v1.36.3" {
		t.Fatalf("the observed value did not replace the declared one: got %q", got)
	}
}

func TestDeleteForgetsAFact(t *testing.T) {
	c := NewCollector()
	mustSet(t, c, KeyGPU, "gb200")
	c.Delete(KeyGPU)
	if _, ok := c.Get(KeyGPU); ok {
		t.Fatal("Delete left the GPU in place")
	}
	var nilCollector *Collector
	nilCollector.Delete(KeyGPU) // must not panic
}

func TestEncodeIsSortedAndStable(t *testing.T) {
	c := NewCollector()
	mustSet(t, c, KeyStage, "full")
	mustSet(t, c, KeyClusterName, "nke-e2e-site-abc123")
	mustSet(t, c, KeyGPU, "gb200")
	want := "cluster_name=nke-e2e-site-abc123\ngpu=gb200\nstage=full\n"
	if got := string(c.Encode()); got != want {
		t.Fatalf("Encode() = %q, want %q", got, want)
	}
}

func TestNilCollectorRecordsNothing(t *testing.T) {
	var c *Collector
	if err := c.Set("not even a known key", "x"); err != nil {
		t.Fatalf("a nil collector must accept and ignore everything, got %v", err)
	}
	if got := c.Encode(); got != nil {
		t.Fatalf("Encode() = %q, want nil", got)
	}
	if err := c.WriteFile(filepath.Join(t.TempDir(), "never")); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}
}

func TestWriteFileWritesTheEncodedFacts(t *testing.T) {
	c := NewCollector()
	mustSet(t, c, KeySite, "ytl")
	dir := t.TempDir()
	path := filepath.Join(dir, "testgrid-metadata.txt")
	if err := c.WriteFile(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "site=ytl\n" {
		t.Fatalf("file holds %q", b)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the temporary file was left behind: %v", entries)
	}
}

func TestWriteFileReportsAnUnwritableDirectory(t *testing.T) {
	c := NewCollector()
	mustSet(t, c, KeySite, "ytl")
	if err := c.WriteFile(filepath.Join(t.TempDir(), "missing", "testgrid-metadata.txt")); err == nil {
		t.Fatal("writing into a directory that does not exist must fail")
	}
}

func TestSetIsSafeForConcurrentUse(t *testing.T) {
	c := NewCollector()
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(2)
		go func() { defer wg.Done(); _ = c.Set(KeySite, "ytl") }()
		go func() { defer wg.Done(); _ = c.Encode() }()
	}
	wg.Wait()
	if got, _ := c.Get(KeySite); got != "ytl" {
		t.Fatalf("got %q after concurrent writes", got)
	}
}

func TestNodeFacts(t *testing.T) {
	cpu := node("cp", map[string]string{LabelArch: "amd64", LabelInstanceType: "cpu.himem.t1"}, "v1.36.3")
	gb200 := node("w", map[string]string{
		LabelArch: "arm64", LabelInstanceType: "gpu.gb200.h05.ibx4.dpux2", LabelGPUProduct: "NVIDIA-GB200",
	}, "v1.36.3")
	bare := node("bare", map[string]string{}, "")

	tests := []struct {
		name                                string
		nodes                               []corev1.Node
		arch, instanceType, gpu, kubeletVer string
	}{
		{name: "no nodes", nodes: nil},
		{name: "a single CPU site node", nodes: []corev1.Node{cpu}, arch: "amd64", instanceType: "cpu.himem.t1", kubeletVer: "v1.36.3"},
		{
			name:  "CPU control plane and GPU worker report the worker's GPU, not mixed",
			nodes: []corev1.Node{cpu, gb200},
			arch:  Mixed, instanceType: Mixed, gpu: "NVIDIA-GB200", kubeletVer: "v1.36.3",
		},
		{name: "a node with no labels contributes nothing", nodes: []corev1.Node{bare, cpu}, arch: "amd64", instanceType: "cpu.himem.t1", kubeletVer: "v1.36.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := NodeSummary{Arch: tt.arch, InstanceType: tt.instanceType, GPUProduct: tt.gpu, KubeletVersion: tt.kubeletVer}
			if got := NodeFacts(tt.nodes); got != want {
				t.Fatalf("NodeFacts() = %+v, want %+v", got, want)
			}
		})
	}
}

// What NodeFacts returns has to be recordable, or the suite would drop it.
func TestNodeFactsOutputIsRecordable(t *testing.T) {
	gb200 := node("w", map[string]string{LabelArch: "arm64", LabelGPUProduct: "NVIDIA-GB200"}, "v1.36.3")
	got := NodeFacts([]corev1.Node{gb200})
	c := NewCollector()
	for key, v := range map[string]string{KeyNodeArch: got.Arch, KeyGPUProduct: got.GPUProduct, KeyTenantKubernetesVersion: got.KubeletVersion, KeyInstanceType: Mixed} {
		if err := c.Set(key, v); err != nil {
			t.Errorf("Set(%s, %q) = %v", key, v, err)
		}
	}
}

func TestEveryKeyIsAccepted(t *testing.T) {
	c := NewCollector()
	for _, key := range Keys() {
		if err := c.Set(key, "x"); err != nil {
			t.Errorf("Set(%q) = %v; every key in Keys() must be recordable", key, err)
		}
	}
}

// The publishing workflow names the keys it accepts from the metadata file. A
// key recorded here but missing there is dropped with only a warning in the
// publish log, so the two lists are held equal here instead.
func TestPublishingWorkflowAcceptsExactlyTheseKeys(t *testing.T) {
	const workflow = "../../../../../.github/workflows/testgrid-publish.yml"
	b, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatal(err)
	}
	var (
		got    []string
		indent = -1
	)
	for line := range strings.SplitSeq(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		lineIndent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case indent < 0 && trimmed == "meta-file-keys: |":
			indent = lineIndent
		case indent >= 0 && trimmed != "" && lineIndent <= indent:
			indent = -2 // the block has ended
		case indent >= 0 && trimmed != "":
			got = append(got, trimmed)
		}
	}
	want := Keys()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s meta-file-keys = %v, want %v", workflow, got, want)
	}
	// The workflow reads the values it names dashboards from with the same
	// pattern the suite writes them with.
	if !strings.Contains(string(b), valueRE.String()) {
		t.Fatalf("%s does not validate values with %s", workflow, valueRE)
	}
}

func mustSet(t *testing.T, c *Collector, key, value string) {
	t.Helper()
	if err := c.Set(key, value); err != nil {
		t.Fatal(err)
	}
}

func node(name string, labels map[string]string, kubelet string) corev1.Node {
	n := corev1.Node{}
	n.ObjectMeta = metav1.ObjectMeta{Name: name, Labels: labels}
	n.Status.NodeInfo.KubeletVersion = kubelet
	return n
}
