// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package aggregate rebuilds one artifact from every per-stream result and
// decides what is qualified.
//
// The central artifact is a derived cache rather than the source of truth.
// Every per-stream result is durable and addressable, so the artifact is a
// function of the results alone and an aggregation needs nothing from the run
// that triggered it. It discovers what is new rather than being told, which
// is what keeps that true.
package aggregate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/dsx-ai-factory/nke/release/internal/policy"
	"github.com/dsx-ai-factory/nke/release/internal/record"
)

// State is the central artifact: one ComponentQualification per component and
// version, merged from that version's per-stream results.
type State struct {
	// Records is keyed by the result name, so a component's versions sit
	// beside each other rather than overwriting.
	Records map[string]*policy.Component `json:"records"`

	// Folded names every result tag already merged in. It is what makes the
	// common case cheap: a listing, then a pull for each stream that just
	// ran.
	Folded []string `json:"folded"`
}

// Aggregator folds new results into the state.
type Aggregator struct {
	// Store is where results and the state live.
	Store record.Store

	// Source is the registry base holding one repository per component.
	Source string

	// Policy supplies the scopes and streams that decide what qualified
	// means. Without it the merge still happens and no scope is claimed,
	// which is the right failure: a missing policy must not silently mark
	// things qualified.
	Policy *policy.Policy

	// Components narrows what is folded. Empty means every component the
	// policy declares, which is the ordinary case; naming a subset is for
	// folding one component's results without a policy in hand.
	Components []string

	// Log receives a line per result folded in. Nil discards.
	Log io.Writer
}

// Run lists what exists, folds in what the state does not already hold, and
// recomputes which scopes each version is qualified at.
func (a *Aggregator) Run(ctx context.Context, state *State) (*State, error) {
	if state == nil {
		state = &State{}
	}
	if state.Records == nil {
		state.Records = map[string]*policy.Component{}
	}
	folded := map[string]bool{}
	for _, t := range state.Folded {
		folded[t] = true
	}

	components := a.components()
	if len(components) == 0 {
		return nil, errors.New("no components to aggregate: pass a policy or a component list")
	}

	for _, component := range components {
		repository := strings.TrimSuffix(a.Source, "/") + "/" + component
		tags, err := a.Store.Tags(ctx, repository)
		if err != nil {
			return nil, err
		}
		// Sorted so that two runs folding the same set produce the same
		// state, and so the log reads in a predictable order.
		slices.Sort(tags)

		for _, tag := range tags {
			ref := repository + ":" + tag
			if folded[ref] {
				continue
			}
			if err := a.fold(ctx, state, component, ref); err != nil {
				return nil, err
			}
			folded[ref] = true
		}
	}

	state.Folded = slices.Sorted(maps.Keys(folded))
	a.qualify(state)
	return state, nil
}

// fold pulls one result and merges its stream into the matching record.
func (a *Aggregator) fold(ctx context.Context, state *State, component, ref string) error {
	files, err := a.Store.Pull(ctx, ref)
	if err != nil {
		if errors.Is(err, record.ErrNotFound) {
			// Listed but unreadable: a tag whose blobs went away. Skipping it
			// rather than failing keeps one broken artifact from stalling
			// every component's aggregation, and the next run retries it
			// because it never entered Folded.
			a.logf("skipping %s: %v", ref, err)
			return nil
		}
		return err
	}
	result, err := files.Result()
	if err != nil {
		return fmt.Errorf("%s: %w", ref, err)
	}
	if result.Spec.Component != component {
		return fmt.Errorf("%s holds a result for %q, not %q", ref, result.Spec.Component, component)
	}
	if result.Spec.Version == "" {
		return fmt.Errorf("%s names no version", ref)
	}

	name := policy.ResultName(result.Spec.Component, result.Spec.Version)
	into, ok := state.Records[name]
	if !ok {
		// First result for this candidate. The declaration and the resolved
		// dependencies come along with it; only the streams accumulate.
		into = &policy.Component{
			APIVersion: policy.APIVersion,
			Kind:       policy.KindComponentQualification,
			Metadata:   policy.Metadata{Name: name},
			Spec:       result.Spec,
			Status:     &policy.Status{},
		}
		state.Records[name] = into
	}
	if into.Status == nil {
		into.Status = &policy.Status{}
	}
	into.Status.Merge(result.Status)
	a.logf("folded %s", ref)
	return nil
}

// qualify recomputes status.qualifiedScopes on every record.
//
// A version is qualified at a scope when every stream that scope selects has
// a passing result. Coverage therefore follows from which streams passed
// rather than from the scope each result carries: a run that set out to cover
// the full matrix and went half unschedulable contributes only what it
// proved.
func (a *Aggregator) qualify(state *State) {
	if a.Policy == nil {
		return
	}
	for _, rec := range state.Records {
		if rec.Status == nil {
			continue
		}
		var scopes []string
		for name := range a.Policy.Spec.Scopes {
			if rec.Status.Passed(a.streamsIn(name)) {
				scopes = append(scopes, name)
			}
		}
		slices.Sort(scopes)
		rec.Status.QualifiedScopes = scopes
	}
}

// streamsIn is the streams a scope selects.
//
// A component may narrow what it runs to a subset of them, and that does not
// narrow what counts as qualified: the scope names the coverage the release
// requires, so a component that runs less of it is qualified at less.
func (a *Aggregator) streamsIn(scope string) []string {
	s, ok := a.Policy.Spec.Scopes[scope]
	if !ok {
		return nil
	}

	var names []string
	for _, stream := range a.Policy.Spec.Streams {
		if s.Matches(stream) {
			names = append(names, stream.Name)
		}
	}
	return names
}

// components is which repositories to list.
func (a *Aggregator) components() []string {
	if len(a.Components) > 0 {
		return slices.Clone(a.Components)
	}
	if a.Policy == nil {
		return nil
	}
	return slices.Clone(a.Policy.Spec.Components)
}

func (a *Aggregator) logf(format string, args ...any) {
	if a.Log == nil {
		return
	}
	fmt.Fprintf(a.Log, "qualify: "+format+"\n", args...)
}

// SortedRecords returns the state's records in a stable order, which is what
// generate renders and what a diff of two states compares.
func (s *State) SortedRecords() []*policy.Component {
	names := slices.Sorted(maps.Keys(s.Records))
	out := make([]*policy.Component, 0, len(names))
	for _, n := range names {
		out = append(out, s.Records[n])
	}
	return out
}

// Encode renders the state as the file that goes into the central artifact.
func Encode(s *State) (record.Files, error) {
	b, err := yaml.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("encoding the state: %w", err)
	}
	return record.Files{record.StateFile: b}, nil
}

// Decode reads a state back. Missing or unreadable is not an error here:
// both make the diff the whole listing, which is the same code path that
// rebuilds from nothing.
func Decode(files record.Files) (*State, error) {
	b, ok := files[record.StateFile]
	if !ok {
		return nil, fmt.Errorf("the artifact holds no %s", record.StateFile)
	}
	var s State
	if err := yaml.UnmarshalStrict(b, &s); err != nil {
		return nil, fmt.Errorf("decoding the state: %w", err)
	}
	return &s, nil
}
