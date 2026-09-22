// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package registrymirror models the registry the bootstrap pulls from. It
// maps each upstream reference to its location on that registry.
//
// The layout is a convention. An image from <registry>/<path> lives at
// <endpoint>/<registry>/<path>. A chart lives at <endpoint>/charts/<name>.
// containerd redirects images through hosts.toml files rendered here.
package registrymirror

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

const (
	// FieldPrefix is the bundle field name used in every message.
	FieldPrefix = "registryMirror"

	// DefaultChartsNamespace is where charts live unless chartsNamespace says otherwise.
	DefaultChartsNamespace = "charts"

	// DockerHubRegistry is the canonical Docker Hub host.
	DockerHubRegistry = "docker.io"
	// DockerHubRegistryAlias is the host containerd dials for docker.io.
	DockerHubRegistryAlias = "registry-1.docker.io"
)

var (
	// endpointPattern is a registry host with an optional port. The value
	// lands in TOML, helm arguments and a directory name.
	endpointPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?(:[0-9]{1,5})?$`)

	// namespacePattern is the OCI repository-name grammar.
	namespacePattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)

	// registryPattern is an upstream registry host, same grammar as endpoint.
	registryPattern = endpointPattern
)

// Mirror is the registryMirror block: the registry and how to reach it.
type Mirror struct {
	// Endpoint is the registry host, with an optional port. Required.
	Endpoint string `yaml:"endpoint"`
	// OCINamespaces overrides the namespace per upstream registry. The
	// default is the registry's own host name.
	OCINamespaces []OCINamespace `yaml:"ociNamespaces,omitempty"`
	// ChartsNamespace overrides where charts live. Empty is DefaultChartsNamespace.
	ChartsNamespace string `yaml:"chartsNamespace,omitempty"`
	// CACertFile is a PEM file with the CA that signed the registry's certificate.
	CACertFile string `yaml:"caCertFile,omitempty"`
	// PlainHTTP reaches the mirror over http rather than https.
	PlainHTTP bool `yaml:"plainHTTP,omitempty"`
	// InsecureSkipTLSVerify reaches the mirror over https without verifying
	// its certificate.
	InsecureSkipTLSVerify bool `yaml:"insecureSkipTLSVerify,omitempty"`
}

// OCINamespace maps one upstream registry to its namespace on the mirror.
type OCINamespace struct {
	// Registry is the upstream registry host, e.g. ghcr.io.
	Registry string `yaml:"registry"`
	// Namespace is the path under the mirror endpoint the upstream's images
	// were copied into. Empty means the mirror root.
	Namespace string `yaml:"namespace,omitempty"`
}

// Problems returns every problem with the declaration, each naming its field.
// It does not read the CA file; see CheckCACertFile.
func (m *Mirror) Problems() []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if m.Endpoint == "" {
		add("%s.endpoint is required: nkectl pulls nothing from public registries, so the registry the "+
			"bundle's artifacts were copied into has to be named", FieldPrefix)
	} else if !endpointPattern.MatchString(m.Endpoint) {
		add("%s.endpoint %q must be a registry host with an optional port", FieldPrefix, m.Endpoint)
	}

	seen := map[string]int{}
	for i, ns := range m.OCINamespaces {
		path := fmt.Sprintf("%s.ociNamespaces[%d]", FieldPrefix, i)
		switch {
		case ns.Registry == "":
			add("%s.registry is required", path)
		case !registryPattern.MatchString(ns.Registry):
			add("%s.registry %q must be a registry host with an optional port", path, ns.Registry)
		default:
			seen[normalizeRegistry(ns.Registry)]++
		}
		if ns.Namespace != "" && !namespacePattern.MatchString(ns.Namespace) {
			add("%s.namespace %q must be an OCI repository path (lowercase letters, digits, and . _ - / between them)",
				path, ns.Namespace)
		}
	}
	for registry, count := range seen {
		if count > 1 {
			add("%s.ociNamespaces declares %s %d times; one entry per upstream registry", FieldPrefix, registry, count)
		}
	}

	if m.ChartsNamespace != "" && !namespacePattern.MatchString(m.ChartsNamespace) {
		add("%s.chartsNamespace %q must be an OCI repository path", FieldPrefix, m.ChartsNamespace)
	}

	if m.PlainHTTP && m.InsecureSkipTLSVerify {
		add("%s.plainHTTP and %s.insecureSkipTLSVerify cannot both be set: plain HTTP has no certificate to skip verifying",
			FieldPrefix, FieldPrefix)
	}
	if m.PlainHTTP && m.CACertFile != "" {
		add("%s.caCertFile is set but %s.plainHTTP is true; a CA is only used over https", FieldPrefix, FieldPrefix)
	}
	return problems
}

// CheckCredentialTransport rejects a credential over plain HTTP, except to a
// loopback address.
func (m *Mirror) CheckCredentialTransport(hasCredential bool) error {
	if !hasCredential || !m.PlainHTTP || isLoopback(m.Endpoint) {
		return nil
	}
	return fmt.Errorf("%s.plainHTTP is true and a registry credential was supplied: the credential would cross "+
		"the network in clear text; use https (with caCertFile or insecureSkipTLSVerify) for %s", FieldPrefix, m.Endpoint)
}

// CheckCACertFile rejects a caCertFile that is unreadable or holds anything
// other than certificates.
func CheckCACertFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s.caCertFile %q cannot be read: %w", FieldPrefix, path, err)
	}
	blocks := 0
	for rest := data; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("%s.caCertFile %q contains a %q block; only CERTIFICATE blocks are accepted",
				FieldPrefix, path, block.Type)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("%s.caCertFile %q holds a CERTIFICATE block that does not parse: %w", FieldPrefix, path, err)
		}
		blocks++
	}
	if blocks == 0 {
		return fmt.Errorf("%s.caCertFile %q contains no PEM CERTIFICATE block", FieldPrefix, path)
	}
	return nil
}

// Namespace returns the path under Endpoint for an upstream registry's
// images. Empty means the root.
func (m *Mirror) Namespace(registry string) string {
	registry = normalizeRegistry(registry)
	for _, ns := range m.OCINamespaces {
		if normalizeRegistry(ns.Registry) == registry {
			return ns.Namespace
		}
	}
	return registry
}

// NamespacePrefix returns Endpoint joined with Namespace.
func (m *Mirror) NamespacePrefix(registry string) string {
	return joinPath(m.Endpoint, m.Namespace(registry))
}

// IsSelf reports whether registry is the mirror's own host.
func (m *Mirror) IsSelf(registry string) bool {
	return normalizeRegistry(registry) == normalizeRegistry(m.Endpoint)
}

// ImageLocation returns the mirror location of an upstream image reference.
// A hostless reference, or one that already names the mirror, is returned
// unchanged.
func (m *Mirror) ImageLocation(upstreamRef string) string {
	ref, ok := ParseImage(upstreamRef)
	if !ok || m.IsSelf(ref.Registry) {
		return upstreamRef
	}
	location := joinPath(m.NamespacePrefix(ref.Registry), ref.Path)
	if ref.Tag != "" {
		location += ":" + ref.Tag
	}
	if ref.Digest != "" {
		location += "@" + ref.Digest
	}
	return location
}

// ChartLocation returns the helm arguments that pull a chart from the mirror's
// chart namespace, version pinned.
func (m *Mirror) ChartLocation(name, version string) []string {
	return []string{"oci://" + joinPath(m.Endpoint, m.chartsNamespace(), name), "--version", version}
}

// ChartsPrefix returns where charts live on the mirror, as an OCI reference
// prefix.
func (m *Mirror) ChartsPrefix() string {
	return "oci://" + joinPath(m.Endpoint, m.chartsNamespace())
}

func (m *Mirror) chartsNamespace() string {
	if m.ChartsNamespace != "" {
		return m.ChartsNamespace
	}
	return DefaultChartsNamespace
}

// Scheme is the URL scheme the mirror is reached over.
func (m *Mirror) Scheme() string {
	if m.PlainHTTP {
		return "http"
	}
	return "https"
}

// ExpandRegistries returns the containerd host names for the registries,
// sorted. docker.io expands to both of its names.
func ExpandRegistries(registries []string) []string {
	set := map[string]bool{}
	for _, registry := range registries {
		registry = normalizeRegistry(registry)
		set[registry] = true
		if registry == DockerHubRegistry {
			set[DockerHubRegistryAlias] = true
		}
	}
	out := make([]string, 0, len(set))
	for registry := range set {
		out = append(out, registry)
	}
	sort.Strings(out)
	return out
}

// normalizeRegistry lower-cases a host and folds the Docker Hub aliases onto
// docker.io.
func normalizeRegistry(registry string) string {
	registry = strings.ToLower(registry)
	switch registry {
	case DockerHubRegistryAlias, "index.docker.io":
		return DockerHubRegistry
	}
	return registry
}

func joinPath(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, strings.Trim(p, "/"))
		}
	}
	return strings.Join(kept, "/")
}

func isLoopback(endpoint string) bool {
	host := endpoint
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}
