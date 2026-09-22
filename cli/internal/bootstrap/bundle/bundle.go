// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package bundle reads the operator's bundle file and the embedded manifest,
// and resolves one against the other.
//
// The manifest lists every chart and image `nkectl bootstrap site` obtains.
// nkectl pins it. The bundle file carries the operator's decisions: the
// registry, the two images they build, and any override. Resolve combines
// the two into a Closure.
package bundle

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/dsx-ai-factory/nke/cli/internal/bootstrap/registrymirror"
)

const (
	mirrorKey    = registrymirror.FieldPrefix
	imagesField  = "images"
	overridesKey = "overrides"
)

// templateYAML is the commented bootstrap bundle `nkectl template
// bootstrap-bundle` writes, with three values left to fill in.
//
//go:embed bootstrap-bundle.yaml
var templateYAML []byte

// TemplateBytes returns the bootstrap bundle template as shipped: the
// operator's blocks, with three values to fill in.
func TemplateBytes() []byte {
	return bytes.Clone(templateYAML)
}

// Bundle is the operator's bundle file. Every field is optional.
type Bundle struct {
	// RegistryMirror names the registry every artifact is pulled from.
	// Absent, every artifact comes from its public registry.
	RegistryMirror *registrymirror.Mirror `yaml:"registryMirror,omitempty"`
	// Images names the images the operator builds. Each defaults to a local tag.
	Images Images `yaml:"images,omitempty"`
	// Overrides replace manifest entries. The key is the entry path:
	// "<component>.chart" or "<component>.images.<name>".
	Overrides map[string]Override `yaml:"overrides,omitempty"`

	path string
}

// Images are the two images the operator builds.
type Images struct {
	Controller          string `yaml:"controller,omitempty"`
	ProvisionedProvider string `yaml:"provisionedProvider,omitempty"`
}

// Override replaces one manifest entry and says why. An image takes Ref and
// Digest. A chart takes Repo, Name and Version, or a Ref.
type Override struct {
	Reason  string `yaml:"reason"`
	Ref     string `yaml:"ref,omitempty"`
	Digest  string `yaml:"digest,omitempty"`
	Repo    string `yaml:"repo,omitempty"`
	Name    string `yaml:"name,omitempty"`
	Version string `yaml:"version,omitempty"`
	SHA256  string `yaml:"sha256,omitempty"`
}

// Public reports whether the bundle declares no registry.
func (b *Bundle) Public() bool { return b.RegistryMirror == nil }

// Path returns where the bundle was loaded from.
func (b *Bundle) Path() string { return b.path }

// Dir returns the bundle file's directory. Relative paths resolve against it.
func (b *Bundle) Dir() string {
	if b.path == "" {
		return ""
	}
	return filepath.Dir(b.path)
}

// Load reads a bundle file. It rejects unknown fields. An empty file is valid.
func Load(path string) (*Bundle, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("bundle %s: %w", path, err)
	}
	return parse(raw, path)
}

func parse(raw []byte, path string) (*Bundle, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	b := Bundle{path: path}
	if err := decoder.Decode(&b); err != nil {
		if errors.Is(err, io.EOF) {
			return &b, nil // nothing declared
		}
		return nil, fmt.Errorf("%s%w", prefix(path), err)
	}
	// Refuse a second document rather than ignore it.
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New(prefix(path) + "expected one YAML document")
	}
	return &b, nil
}

// Validate checks the bundle against the manifest. It reports every problem
// together, each naming its field. It reads no files; see ValidateReferences.
func (b *Bundle) Validate(m *Manifest) error {
	var problems []string
	where := "your registry"
	if b.RegistryMirror != nil && b.RegistryMirror.Endpoint != "" {
		where = b.RegistryMirror.Endpoint
	}
	for _, field := range sortedKeys(b.imageFields()) {
		ref := strings.TrimSpace(*b.imageFields()[field])
		at := imagesField + "." + field
		switch {
		case b.RegistryMirror != nil && ref == "":
			// With a registry declared, every image comes from it, so the
			// default -- hostless, side-loaded -- is not an option. An empty
			// value and an absent one decode the same, so one message covers both.
			problems = append(problems, fmt.Sprintf("%s is required with %s: push the image to %s and name it here", at, mirrorKey, where))
		case b.RegistryMirror != nil:
			if _, hasHost := registrymirror.ParseImage(ref); !hasHost {
				problems = append(problems, fmt.Sprintf("%s %q names no registry, but %s is declared: push the image to %s and name it here; "+
					"a hostless image is side-loaded and is for the public form only", at, ref, mirrorKey, where))
			}
		case ref == "" && *b.imageFields()[field] != "":
			problems = append(problems, at+" is blank: leave it out for the default, or name the image you built")
		}
	}
	for _, path := range sortedKeys(b.Overrides) {
		problems = append(problems, b.overrideProblems(m, path, b.Overrides[path])...)
	}
	problems = append(problems, b.mirrorProblems()...)
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%svalidation failed:\n  - %s", b.prefix(), strings.Join(problems, "\n  - "))
}

// overrideProblems checks one override: the target exists, the reason is
// given, and the replacement differs from the entry.
func (b *Bundle) overrideProblems(m *Manifest, path string, o Override) []string {
	at := overridesKey + "." + path
	if field, theirs := operatorImagePaths()[path]; theirs {
		return []string{fmt.Sprintf("%s: that image is yours already; name it in %s.%s instead", at, imagesField, field)}
	}
	kind, chart, image := m.entry(path)
	if kind == entryNone {
		return []string{fmt.Sprintf("%s: the manifest has no entry at that path; `nkectl get bootstrap-manifest` lists every path as <component>.chart or <component>.images.<name>", at)}
	}
	var problems []string
	if strings.TrimSpace(o.Reason) == "" {
		problems = append(problems, at+".reason must say why this differs from what nkectl was built with")
	}
	switch kind {
	case entryImage:
		if o.Repo != "" || o.Name != "" || o.Version != "" || o.SHA256 != "" {
			problems = append(problems, at+" is an image: ref and digest are its replacement fields")
		}
		switch {
		case strings.TrimSpace(o.Ref) == "":
			problems = append(problems, at+".ref is required: the image to pull instead")
		case o.Ref == image.Ref && (o.Digest == "" || o.Digest == image.Digest):
			problems = append(problems, fmt.Sprintf("%s changes nothing: the manifest already names %s", at, image.Ref))
		case o.Digest != "" && o.Digest == image.Digest:
			problems = append(problems, fmt.Sprintf("%s.digest still names the image it replaced (%s): give the digest of %s, or leave it out", at, image.Ref, o.Ref))
		}
	case entryChart:
		if o.Digest != "" {
			problems = append(problems, at+" is a chart: digest is an image field; sha256 is the chart archive digest")
		}
		switch {
		case o.Ref != "" && (o.Repo != "" || o.Name != "" || o.Version != ""):
			problems = append(problems, at+": give a ref, or repo, name and version, not both")
		case o.Ref == "" && (o.Repo == "" || o.Name == "" || o.Version == ""):
			problems = append(problems, at+": a chart is replaced by a ref (an OCI reference or a path), or by repo, name and version together")
		case o.Ref == "" && o.Repo == chart.Repo && o.Name == chart.Name && o.Version == chart.Version:
			problems = append(problems, fmt.Sprintf("%s changes nothing: the manifest already names %s", at, chart.describe()))
		}
		if o.SHA256 != "" && o.SHA256 == chart.SHA256 {
			problems = append(problems, fmt.Sprintf("%s.sha256 still names the chart it replaced: give the digest of the new archive, or leave it out", at))
		}
	default:
	}
	return problems
}

// imageFields maps the images block's field names to the fields.
func (b *Bundle) imageFields() map[string]*string {
	return map[string]*string{"controller": &b.Images.Controller, "provisionedProvider": &b.Images.ProvisionedProvider}
}

// mirrorProblems validates the registryMirror block when present.
func (b *Bundle) mirrorProblems() []string {
	if b.RegistryMirror == nil {
		return nil
	}
	return b.RegistryMirror.Problems()
}

// ValidateReferences checks the files the bundle names: today, that caCertFile
// holds certificates. Call it where the bootstrap runs, not where the file is
// generated.
func (b *Bundle) ValidateReferences() error {
	m := b.RegistryMirror
	if m == nil || m.CACertFile == "" {
		return nil
	}
	if err := registrymirror.CheckCACertFile(b.resolvePath(m.CACertFile)); err != nil {
		return fmt.Errorf("%s%w", b.prefix(), err)
	}
	return nil
}

// CACertPath returns the resolved caCertFile path, or "".
func (b *Bundle) CACertPath() string {
	if b.RegistryMirror == nil || b.RegistryMirror.CACertFile == "" {
		return ""
	}
	return b.resolvePath(b.RegistryMirror.CACertFile)
}

// resolvePath makes a relative path relative to the bundle file.
func (b *Bundle) resolvePath(path string) string {
	if filepath.IsAbs(path) || b.Dir() == "" {
		return path
	}
	return filepath.Join(b.Dir(), path)
}

func (b *Bundle) prefix() string { return prefix(b.path) }

func prefix(path string) string {
	if path == "" {
		return "bootstrap bundle: "
	}
	return "bootstrap bundle " + path + ": "
}
