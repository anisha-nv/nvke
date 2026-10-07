// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package testgrid records what a run built and observed, for the workflow
// that publishes the run to TestGrid.
//
// The suite fills a Collector as it goes and rewrites the file each time it
// learns something, so a run that stops part way still leaves what it
// learned. The file is plain key=value lines rather than JSON so the
// publishing workflow can read it with the shell alone.
//
// The keys are a contract with that workflow, which accepts only these, and
// every value is restricted to a conservative character set: the file travels
// in a build artifact, and its values end up in a bucket path and in log
// lines.
package testgrid

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
)

// Keys a run may record.
const (
	// KeyClusterName is the site cluster's name, which is per run.
	KeyClusterName = "cluster_name"
	// KeySite is the Forge site the run resolved to, such as ytl.
	KeySite = "site"
	// KeyStage is how far the run was asked to go: provision, bootstrap or full.
	KeyStage = "stage"
	// KeyArch is the architecture the run asked for.
	KeyArch = "arch"
	// KeyGPU is the GPU model the run asked for, such as gb200. Absent for a
	// run that asked for none.
	KeyGPU = "gpu"
	// KeyKubernetesVersion is the site API server's version as observed, or the
	// version the run declared when it could not be observed.
	KeyKubernetesVersion = "kubernetes_version"
	// KeyNodeArch is the site node's architecture as observed.
	KeyNodeArch = "node_arch"
	// KeyInstanceType is the site node's instance type as observed.
	KeyInstanceType = "instance_type"
	// KeyGPUProduct is the GPU product a tenant node reports, as observed.
	KeyGPUProduct = "gpu_product"
	// KeyTenantKubernetesVersion is the kubelet version on the tenant's nodes,
	// as observed.
	KeyTenantKubernetesVersion = "tenant_kubernetes_version"
)

// Node labels NodeFacts reads, and what it reports when nodes disagree.
const (
	LabelArch         = "kubernetes.io/arch"
	LabelInstanceType = "node.kubernetes.io/instance-type"
	LabelGPUProduct   = "nvidia.com/gpu.product"

	Mixed = "mixed"
)

var (
	// ErrUnknownKey is returned for a key outside the contract.
	ErrUnknownKey = errors.New("unknown metadata key")
	// ErrInvalidValue is returned for a value outside the allowed character set.
	ErrInvalidValue = errors.New("metadata value must be 1-128 characters of A-Z a-z 0-9 . _ + -")

	// valueRE keeps a value to one short token: no separators the workflow
	// would have to escape, no path or URL syntax, and no line breaks.
	valueRE = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,128}$`)
)

// Keys is every key a run may record: the contract with the publishing
// workflow, whose meta-file-keys must list exactly these. A test holds the two
// to each other.
func Keys() []string {
	return []string{
		KeyClusterName, KeySite, KeyStage, KeyArch, KeyGPU, KeyKubernetesVersion,
		KeyNodeArch, KeyInstanceType, KeyGPUProduct, KeyTenantKubernetesVersion,
	}
}

// knownKey reports whether key is part of the contract.
func knownKey(key string) bool {
	return slices.Contains(Keys(), key)
}

// Collector accumulates facts about a run. It is safe for concurrent use.
// A nil Collector records nothing, which is what a run that does not publish
// to TestGrid gets.
type Collector struct {
	mu    sync.Mutex
	facts map[string]string
}

// NewCollector returns an empty Collector.
func NewCollector() *Collector {
	return &Collector{mu: sync.Mutex{}, facts: map[string]string{}}
}

// Set records value under key, replacing an earlier value. An empty value is
// ignored, so a fact that could not be learned leaves an earlier one in place.
func (c *Collector) Set(key, value string) error {
	if c == nil {
		return nil
	}
	if !knownKey(key) {
		return fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
	if value == "" {
		return nil
	}
	if !valueRE.MatchString(value) {
		return fmt.Errorf("%w: %q for %s", ErrInvalidValue, value, key)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.facts[key] = value
	return nil
}

// Get returns the value recorded under key.
func (c *Collector) Get(key string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.facts[key]
	return v, ok
}

// Encode returns the facts as key=value lines, sorted by key so the same facts
// always give the same bytes.
func (c *Collector) Encode() []byte {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(c.facts))
	for k := range c.facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + c.facts[k] + "\n")
	}
	return []byte(b.String())
}

// WriteFile writes the facts to path. It writes a temporary file beside it and
// renames it into place, so a reader never sees a partial file.
func (c *Collector) WriteFile(path string) error {
	if c == nil {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".testgrid-metadata-*")
	if err != nil {
		return fmt.Errorf("creating a temporary file beside %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(c.Encode()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting the mode of %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("renaming into %s: %w", path, err)
	}
	return nil
}

// NodeSummary is what a set of nodes reports about itself. Each field is the
// value the nodes share, Mixed when they disagree, or empty when none reports
// it.
type NodeSummary struct {
	Arch           string
	InstanceType   string
	GPUProduct     string
	KubeletVersion string
}

// NodeFacts summarizes nodes. The GPU product ignores nodes without a GPU, so a
// cluster with a CPU control plane and GPU workers reports the workers' GPU
// rather than Mixed.
func NodeFacts(nodes []corev1.Node) NodeSummary {
	archs := map[string]bool{}
	types := map[string]bool{}
	gpus := map[string]bool{}
	kubelets := map[string]bool{}
	for i := range nodes {
		n := &nodes[i]
		addIfSet(archs, n.Labels[LabelArch])
		addIfSet(types, n.Labels[LabelInstanceType])
		addIfSet(gpus, n.Labels[LabelGPUProduct])
		addIfSet(kubelets, n.Status.NodeInfo.KubeletVersion)
	}
	return NodeSummary{
		Arch:           single(archs),
		InstanceType:   single(types),
		GPUProduct:     single(gpus),
		KubeletVersion: single(kubelets),
	}
}

func addIfSet(set map[string]bool, v string) {
	if v != "" {
		set[v] = true
	}
}

func single(set map[string]bool) string {
	switch len(set) {
	case 0:
		return ""
	case 1:
		for v := range set {
			return v
		}
	}
	return Mixed
}
