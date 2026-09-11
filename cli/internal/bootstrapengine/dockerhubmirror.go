// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package bootstrapengine

import (
	"context"
	"fmt"
	"regexp"
)

// dockerHubRegistries are the registry hosts a docker.io image reference
// resolves through. containerd looks each one up under its own certs.d
// directory, so both need a hosts.toml for the mirror to take effect.
var dockerHubRegistries = []string{"docker.io", "registry-1.docker.io"}

// otherRegistries are the registry hosts, besides Docker Hub, that the images
// the bootstrap releases run are pulled from: the SPIRE stack from ghcr.io,
// Cluster API from registry.k8s.io, cert-manager from quay.io, and the SPIRE
// chart's digest-pinned shell from cgr.dev. The list tracks
// hack/mirror-bootstrap-images.sh, which mirrors each image under the same
// path it has upstream, so one mirror host serves every registry here.
var otherRegistries = []string{"ghcr.io", "registry.k8s.io", "quay.io", "cgr.dev"}

// registryMirror is one hosts.toml to write for each of registries, sending
// their pulls to mirror.
type registryMirror struct {
	mirror     string
	registries []string
}

// registryMirrors resolves the two mirror knobs into the hosts.toml files to
// write. RegistryMirror covers every registry; DockerHubMirror, when also set,
// takes the Docker Hub hosts, so a CI that already has a Docker Hub mirror
// keeps using it when a full mirror is added beside it.
func registryMirrors(cfg Config) []registryMirror {
	var out []registryMirror
	if cfg.RegistryMirror != "" {
		registries := otherRegistries
		if cfg.DockerHubMirror == "" {
			registries = append(append([]string{}, dockerHubRegistries...), otherRegistries...)
		}
		out = append(out, registryMirror{mirror: cfg.RegistryMirror, registries: registries})
	}
	if cfg.DockerHubMirror != "" {
		out = append(out, registryMirror{mirror: cfg.DockerHubMirror, registries: dockerHubRegistries})
	}
	return out
}

// registryMirrorPattern is a registry host with an optional port. The value
// is written into a TOML string and a shell command, so nothing else is
// accepted.
var registryMirrorPattern = regexp.MustCompile(`^[a-zA-Z0-9.-]+(:[0-9]{1,5})?$`)

// validateRegistryMirror returns an error unless mirror is empty or a
// registry host with an optional port. field names the Config field in the
// error.
func validateRegistryMirror(field, mirror string) error {
	if mirror == "" || registryMirrorPattern.MatchString(mirror) {
		return nil
	}
	return fmt.Errorf("bootstrapengine: Config.%s %q must be a registry host with an optional port", field, mirror)
}

// configureRegistryMirror writes a containerd hosts.toml for each of
// m.registries on the cluster's control-plane node, so the node's pulls from
// those registries go to m.mirror. kind's node image enables containerd's
// config_path, so the files apply to the next pull with no restart.
//
// The mirror is both the pull host and the server, the same shape
// controller/hack/ci/backend-e2e.sh gives the management cluster: a mirror
// that does not answer fails the pull rather than falling back to the
// upstream registry, which is what the mirror exists to keep the run away
// from. containerd keeps the image's upstream path in the request, so the
// mirror has to hold ghcr.io/spiffe/spire-server as <mirror>/spiffe/spire-server,
// which is the layout hack/mirror-bootstrap-images.sh writes.
func (e *Engine) configureRegistryMirror(ctx context.Context, cfg Config, m registryMirror) error {
	node := cfg.ClusterName + "-control-plane"
	e.log.Info("routing the bootstrap cluster's image pulls through a mirror",
		"cluster", cfg.ClusterName, "mirror", m.mirror, "registries", m.registries)

	hostsTOML := fmt.Sprintf("server = \"https://%[1]s\"\n\n[host.\"https://%[1]s\"]\n  capabilities = [\"pull\", \"resolve\"]\n", m.mirror)
	for _, registry := range m.registries {
		dir := "/etc/containerd/certs.d/" + registry
		// hostsTOML holds no single quote, so the literal is safe.
		script := fmt.Sprintf("mkdir -p %s && printf '%%s' '%s' > %s/hosts.toml", dir, hostsTOML, dir)
		out, err := e.Runner.Run(ctx, nil, RuntimeCLI, "exec", node, "sh", "-c", script)
		if err != nil {
			return fmt.Errorf("configuring the %s mirror %s on %s: %w\n%s", registry, m.mirror, node, err, out)
		}
	}
	return nil
}
