// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package bundle

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nke/cli/internal/bootstrap/registrymirror"
)

// Closure is the manifest resolved against the bundle file: where each
// artifact is pulled from.
type Closure struct {
	// The charts the bootstrap pulls.
	Temporal, SPIRECRDs, SPIRE        ChartInstall
	ControllerImage, ProvisionedImage ImageRef

	// Mirror is the registryMirror block with caCertFile resolved. Nil when
	// the bundle declares none.
	Mirror *registrymirror.Mirror
	// Registries lists the upstream hosts containerd redirects, sorted.
	Registries []string

	// Overrides lists every replaced entry.
	Overrides []Overridden

	// Artifacts lists every chart and image with its upstream reference and
	// resolved location.
	Artifacts []Artifact
}

// Overridden is one manifest entry the bundle file replaced, and why.
type Overridden struct {
	Path   string
	Reason string
}

// ChartInstall is one chart as helm will be asked to install it.
type ChartInstall struct {
	// Path is the manifest entry this came from, for error messages.
	Path string
	// Args are the chart positional argument and the --version flag.
	Args []string
	// Version and SHA256 identify an upstream chart. Empty for a chart
	// supplied by reference.
	Version string
	SHA256  string
	// Built reports a chart supplied by reference.
	Built bool
}

// ImageRef is one image the deploy passes to a chart or manifest by value.
type ImageRef struct {
	Path       string
	Ref        string
	Repository string
	Tag        string
	Digest     string
}

// Artifact is one row of the dry-run table.
type Artifact struct {
	Path     string
	Upstream string
	Location string
	Note     string
	// Registry is the upstream host of a mirrored image. Empty otherwise.
	Registry string
}

// Resolve applies the bundle file to the manifest and resolves every
// artifact to where it is pulled from. The bundle must have passed Validate.
func Resolve(b *Bundle, mf *Manifest) (Closure, error) {
	var m *registrymirror.Mirror
	if b.RegistryMirror != nil {
		// Resolve the CA path against the bundle file.
		resolved := *b.RegistryMirror
		resolved.CACertFile = b.CACertPath()
		m = &resolved
	}
	c := Closure{Mirror: m}

	components := effective(b, mf)
	for _, path := range sortedKeys(b.Overrides) {
		c.Overrides = append(c.Overrides, Overridden{Path: path, Reason: b.Overrides[path].Reason})
	}

	// Where each named artifact lands in the closure.
	charts := map[string]*ChartInstall{
		ComponentTemporal: &c.Temporal, ComponentSPIRECRDs: &c.SPIRECRDs, ComponentSPIRE: &c.SPIRE,
	}
	images := map[string]*ImageRef{
		imagePath(ComponentController, ImageController): &c.ControllerImage,
		imagePath(ComponentProvisioned, ImageManager):   &c.ProvisionedImage,
	}
	theirs := operatorImagePaths()

	registries := map[string]bool{}
	for _, name := range sortedKeys(components) {
		comp := components[name]
		if comp.Chart != nil {
			install, artifact := b.resolveChart(name, comp.Chart, m)
			c.Artifacts = append(c.Artifacts, artifact)
			if dst, ok := charts[name]; ok {
				*dst = install
			}
		}
		for _, imageName := range sortedKeys(comp.Images) {
			path := imagePath(name, imageName)
			_, built := theirs[path]
			ref, artifact := resolveImage(path, comp.Images[imageName], m, built)
			if artifact.Registry != "" && m != nil && m.IsSelf(artifact.Registry) {
				artifact.Registry = "" // the mirror host has its own hosts.toml
			}
			c.Artifacts = append(c.Artifacts, artifact)
			if artifact.Registry != "" {
				registries[artifact.Registry] = true
			}
			if dst, ok := images[path]; ok {
				*dst = ref
			}
		}
	}
	c.Registries = sortedKeys(registries)
	return c, nil
}

// effective copies the manifest and applies the bundle file's images and
// overrides.
func effective(b *Bundle, mf *Manifest) map[string]Component {
	out := make(map[string]Component, len(mf.Components))
	for name, comp := range mf.Components {
		copied := Component{Images: make(map[string]Image, len(comp.Images))}
		if comp.Chart != nil {
			chart := *comp.Chart
			copied.Chart = &chart
		}
		for k, v := range comp.Images {
			copied.Images[k] = v
		}
		out[name] = copied
	}
	if b.Images.Controller != "" {
		out[ComponentController].Images[ImageController] = Image{Ref: b.Images.Controller}
	}
	if b.Images.ProvisionedProvider != "" {
		out[ComponentProvisioned].Images[ImageManager] = Image{Ref: b.Images.ProvisionedProvider}
	}
	for path, o := range b.Overrides {
		parts := strings.Split(path, ".")
		comp := out[parts[0]]
		switch {
		case len(parts) == 2:
			comp.Chart = &Chart{Ref: o.Ref, Repo: o.Repo, Name: o.Name, Version: o.Version, SHA256: o.SHA256}
		case len(parts) == 3:
			comp.Images[parts[2]] = Image{Ref: o.Ref, Digest: o.Digest}
		}
		out[parts[0]] = comp
	}
	return out
}

func (b *Bundle) resolveChart(name string, chart *Chart, m *registrymirror.Mirror) (ChartInstall, Artifact) {
	path := chartPath(name)
	if chart.Ref != "" {
		// A chart supplied by reference. A relative path is relative to the
		// bundle file.
		ref := chart.Ref
		if !strings.HasPrefix(ref, "oci://") && !filepath.IsAbs(ref) && b.Dir() != "" {
			ref = filepath.Join(b.Dir(), ref)
		}
		return ChartInstall{Path: path, Args: []string{ref}, Built: true},
			Artifact{Path: path, Upstream: chart.Ref, Location: ref, Note: "built by you"}
	}
	upstream := chart.describe()
	if m == nil {
		return ChartInstall{
				Path:    path,
				Args:    []string{chart.Name, "--repo", chart.Repo, "--version", chart.Version},
				Version: chart.Version, SHA256: chart.SHA256,
			},
			Artifact{Path: path, Upstream: upstream, Note: "public"}
	}
	args := m.ChartLocation(chart.Name, chart.Version)
	return ChartInstall{Path: path, Args: args, Version: chart.Version, SHA256: chart.SHA256},
		Artifact{Path: path, Upstream: upstream, Location: args[0], Note: "via mirror"}
}

// resolveImage applies one rule to every image. A hostless reference is
// side-loaded. With a mirror, a reference with a host is redirected through
// it; a reference that already names the mirror is used as written. Without
// a mirror, the reference is pulled as written. built marks the images the
// operator builds, for the note only.
func resolveImage(path string, img Image, m *registrymirror.Mirror, built bool) (ImageRef, Artifact) {
	ref := ImageRef{Path: path, Ref: img.Ref, Digest: img.Digest}
	ref.Repository, ref.Tag = splitRepoTag(img.Ref)
	who := ""
	if built {
		who = "built by you; "
	}

	parsed, isRegistryRef := registrymirror.ParseImage(img.Ref)
	if !isRegistryRef {
		return ref, Artifact{Path: path, Upstream: img.Ref, Location: img.Ref, Note: who + "side-loaded"}
	}
	if ref.Digest == "" {
		// A digest inside the reference pins the image.
		ref.Digest = parsed.Digest
	}
	switch {
	case m == nil:
		return ref, Artifact{Path: path, Upstream: img.Ref, Note: who + "public"}
	case m.IsSelf(parsed.Registry):
		return ref, Artifact{Path: path, Upstream: img.Ref, Location: img.Ref, Note: who + "on the mirror", Registry: parsed.Registry}
	default:
		return ref, Artifact{
			Path: path, Upstream: img.Ref, Location: m.ImageLocation(img.Ref),
			Note: who + "via mirror", Registry: parsed.Registry,
		}
	}
}

// splitRepoTag splits an image reference into repository and tag.
func splitRepoTag(ref string) (repo, tag string) {
	if at := strings.Index(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	if colon := strings.LastIndex(ref, ":"); colon >= 0 && !strings.Contains(ref[colon:], "/") {
		return ref[:colon], ref[colon+1:]
	}
	return ref, ""
}

// DryRunTable renders what the bootstrap would pull and from where. The
// credential itself never appears.
func (c Closure) DryRunTable(hasCredential bool) string {
	var sb strings.Builder
	m := c.Mirror
	sb.WriteString("Bootstrap bundle:\n")
	if m == nil {
		fmt.Fprintf(&sb, "  %s: none; every artifact is pulled from the public registry it names\n", mirrorKey)
	} else {
		fmt.Fprintf(&sb, "  %s: %s://%s (%s, auth: %s)\n", mirrorKey, m.Scheme(), m.Endpoint, tlsMode(m), authMode(hasCredential))
		for _, registry := range c.Registries {
			fmt.Fprintf(&sb, "    %-24s -> %s\n", registry, m.NamespacePrefix(registry))
		}
		fmt.Fprintf(&sb, "    %-24s -> %s\n", "charts", m.ChartsPrefix())
	}
	if len(c.Overrides) > 0 {
		fmt.Fprintf(&sb, "  overridden (%d; these differ from what nkectl was built with):\n", len(c.Overrides))
		for _, o := range c.Overrides {
			fmt.Fprintf(&sb, "    %s: %s\n", o.Path, o.Reason)
		}
	}
	sb.WriteString("  artifacts:\n")
	width := 0
	for _, a := range c.Artifacts {
		if len(a.Path) > width {
			width = len(a.Path)
		}
	}
	for _, a := range c.Artifacts {
		line := fmt.Sprintf("    %-*s  %s", width, a.Path, a.Upstream)
		if c.isOverridden(a.Path) {
			line += " [overridden]"
		}
		if a.Location != a.Upstream && a.Location != "" {
			line += " -> " + a.Location
		}
		if a.Note != "" {
			line += "  (" + a.Note + ")"
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

// isOverridden reports whether an artifact row is one the bundle replaced.
func (c Closure) isOverridden(path string) bool {
	for _, o := range c.Overrides {
		if path == o.Path {
			return true
		}
	}
	return false
}

func tlsMode(m *registrymirror.Mirror) string {
	switch {
	case m.PlainHTTP:
		return "plain http"
	case m.InsecureSkipTLSVerify:
		return "tls: verification skipped"
	case m.CACertFile != "":
		return "tls: ca " + m.CACertFile
	default:
		return "tls: system roots"
	}
}

func authMode(hasCredential bool) string {
	if hasCredential {
		return "credential supplied"
	}
	return "none"
}
