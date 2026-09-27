// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package record stores qualification results.
//
// Where a result goes is the one thing publish is parameterised on. This
// implementation pushes an OCI artifact, because the registry already exists
// and the SBOM work established the pattern. Pushing somewhere else later is
// a different Store for the same step, and nothing upstream of it changes.
package record

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/dsx-ai-factory/nke/release/internal/policy"
)

// Names of the files an artifact can carry. A per-stream result holds the
// first two; the central artifact holds the third.
const (
	ResultFile = "result.yaml"
	ReportFile = "ctrf/report.json"
	StateFile  = "state.yaml"
)

// ArtifactType is the OCI artifact type every object here carries.
const ArtifactType = "application/vnd.nvidia.nke.qualification.v1+yaml"

// ErrNotFound is returned by Pull when the reference holds nothing.
//
// It is distinguished so that a missing central artifact makes the aggregate
// diff the whole listing rather than fail, which is what makes rebuilding
// from nothing the same code path as the incremental case.
var ErrNotFound = errors.New("not found")

// Files is an artifact's contents, keyed by path within it.
//
// The store is deliberately untyped about what it carries. A per-stream
// result and the central artifact are different shapes stored the same way,
// and keeping the encoding with the caller means adding a third shape needs
// no change here.
type Files map[string][]byte

// Store is where artifacts are kept.
type Store interface {
	// Push writes one artifact at the given reference.
	Push(ctx context.Context, ref string, files Files) error

	// Pull reads one back, returning ErrNotFound when the reference holds
	// nothing.
	Pull(ctx context.Context, ref string) (Files, error)

	// Tags lists a repository's tags. A repository that does not exist
	// yields an empty list rather than an error, because "no results yet" is
	// the ordinary first run.
	Tags(ctx context.Context, repository string) ([]string, error)
}

// Key identifies one stream's result. It is a composite key rather than a
// name, which is what lets aggregate discover what exists from a tag listing
// without pulling anything.
type Key struct {
	Component   string
	Version     string
	Combination string
	Stream      string
}

// Tag renders the key as an OCI tag.
//
// The parts are joined with _ and each is sanitised, because a tag admits a
// narrower alphabet than a version does. The combination id and the stream
// name both already avoid the characters that would need escaping; the
// sanitiser is here so that one that does not produces a wrong-looking tag
// rather than a push that fails deep inside a registry client.
func (k Key) Tag() string {
	return strings.Join([]string{
		sanitise(k.Version),
		sanitise(k.Combination),
		sanitise(k.Stream),
	}, "_")
}

// Repository is where this component's results live.
func (k Key) Repository(base string) string {
	return strings.TrimSuffix(base, "/") + "/" + k.Component
}

// Reference is the full reference a push or pull addresses.
func (k Key) Reference(base string) string {
	return k.Repository(base) + ":" + k.Tag()
}

// sanitise replaces characters an OCI tag does not admit.
func sanitise(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.', r == '-':
			return r
		default:
			return '-'
		}
	}, s)
}

// Encode renders a result as the bytes that go into an artifact.
func Encode(c *policy.Component) ([]byte, error) {
	b, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encoding the result: %w", err)
	}
	return b, nil
}

// Decode reads a result back.
func Decode(b []byte) (*policy.Component, error) {
	var c policy.Component
	if err := yaml.UnmarshalStrict(b, &c); err != nil {
		return nil, fmt.Errorf("decoding the result: %w", err)
	}
	if c.Kind != policy.KindComponentQualification {
		return nil, fmt.Errorf("artifact holds kind %q, want %q", c.Kind, policy.KindComponentQualification)
	}
	return &c, nil
}

// Result reads the result out of an artifact's files.
func (f Files) Result() (*policy.Component, error) {
	b, ok := f[ResultFile]
	if !ok {
		return nil, fmt.Errorf("the artifact holds no %s", ResultFile)
	}
	return Decode(b)
}

// New builds a result record for one stream: the same kind the component
// declares, with the selectors resolved and one stream in its status.
func New(spec policy.ComponentSpec, combination policy.StatusCombination) *policy.Component {
	return &policy.Component{
		APIVersion: policy.APIVersion,
		Kind:       policy.KindComponentQualification,
		Metadata:   policy.Metadata{Name: policy.ResultName(spec.Component, spec.Version)},
		Spec:       spec,
		Status:     &policy.Status{Combinations: []policy.StatusCombination{combination}},
	}
}
