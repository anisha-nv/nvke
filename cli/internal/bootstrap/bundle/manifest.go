// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package bundle

import (
	"bytes"
	_ "embed"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// Component keys, as the manifest names them.
	ComponentPostgres    = "postgresql"
	ComponentTemporal    = "temporal"
	ComponentSPIRECRDs   = "spire-crds"
	ComponentSPIRE       = "spire"
	ComponentController  = "controller"
	ComponentProvisioned = "cluster-api-provider-provisioned"

	// Image keys Resolve reads by name.
	ImageController = "controller"
	ImageManager    = "manager"

	// Default references for the images an operator builds. They name no
	// registry, so the bootstrap side-loads them.
	DefaultControllerImage  = "cwe-controller:local"
	DefaultProvisionedImage = "cluster-api-provider-provisioned:local"

	chartKey  = "chart"
	imagesKey = "images"
)

// manifestYAML is the embedded manifest. `nkectl get bootstrap-manifest` prints it.
//
//go:embed bootstrap-manifest.yaml
var manifestYAML []byte

// Manifest lists every chart and image `bootstrap site` obtains, with the
// version and digest of each. nkectl pins it.
type Manifest struct {
	Components map[string]Component `yaml:"components"`
}

// Component is one deployed piece: its chart, if pulled, and its images.
type Component struct {
	Chart  *Chart           `yaml:"chart,omitempty"`
	Images map[string]Image `yaml:"images,omitempty"`
}

// Chart names a Helm chart by repository, name and version, or by Ref after
// an override.
type Chart struct {
	Ref     string `yaml:"ref,omitempty"`
	Repo    string `yaml:"repo,omitempty"`
	Name    string `yaml:"name,omitempty"`
	Version string `yaml:"version,omitempty"`
	// SHA256 is the digest of the chart archive.
	SHA256 string `yaml:"sha256,omitempty"`
}

// Image is a container image the component runs.
type Image struct {
	Ref string `yaml:"ref"`
	// Digest is the image digest. Empty for the images the operator builds.
	Digest string `yaml:"digest,omitempty"`
}

// LoadManifest parses the embedded manifest. It panics if the document does
// not parse; that is a build defect.
func LoadManifest() *Manifest {
	decoder := yaml.NewDecoder(bytes.NewReader(manifestYAML))
	decoder.KnownFields(true)
	var m Manifest
	if err := decoder.Decode(&m); err != nil {
		panic("bundle: embedded bootstrap-manifest.yaml does not parse: " + err.Error())
	}
	return &m
}

// ManifestBytes returns the embedded manifest as shipped, comments included.
func ManifestBytes() []byte {
	return bytes.Clone(manifestYAML)
}

// entryKind says what an manifest path names.
type entryKind int

const (
	entryNone entryKind = iota
	entryChart
	entryImage
)

// entry looks up "<component>.chart" or "<component>.images.<name>".
func (m *Manifest) entry(path string) (kind entryKind, chart *Chart, image *Image) {
	parts := strings.Split(path, ".")
	comp, ok := m.Components[parts[0]]
	if !ok {
		return entryNone, nil, nil
	}
	switch {
	case len(parts) == 2 && parts[1] == chartKey && comp.Chart != nil:
		return entryChart, comp.Chart, nil
	case len(parts) == 3 && parts[1] == imagesKey:
		img, ok := comp.Images[parts[2]]
		if !ok {
			return entryNone, nil, nil
		}
		return entryImage, nil, &img
	default:
		return entryNone, nil, nil
	}
}

// operatorImagePaths maps the operator's image paths to their images: field.
func operatorImagePaths() map[string]string {
	return map[string]string{
		imagePath(ComponentController, ImageController): "controller",
		imagePath(ComponentProvisioned, ImageManager):   "provisionedProvider",
	}
}

func imagePath(component, image string) string { return component + "." + imagesKey + "." + image }
func chartPath(component string) string        { return component + "." + chartKey }

// sortedKeys returns a map's keys in order, for stable output.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// describe renders an entry for a message.
func (c *Chart) describe() string {
	if c.Ref != "" {
		return c.Ref
	}
	return fmt.Sprintf("%s %s from %s", c.Name, c.Version, c.Repo)
}
