// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package aggregate

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nke/release/internal/policy"
	"github.com/dsx-ai-factory/nke/release/internal/record"
)

// memory is a Store held in a map. The OCI implementation is exercised
// against a registry rather than here; what these tests are about is which
// references get pulled and what folding them produces.
type memory struct {
	files map[string]record.Files

	// pulls records every reference asked for, which is how the incremental
	// case is distinguished from the rebuild.
	pulls []string

	// ghosts are tags Tags reports and Pull does not have, which is what a
	// retention policy or a concurrent push looks like from here.
	ghosts map[string][]string
}

func (m *memory) Push(_ context.Context, ref string, files record.Files) error {
	if m.files == nil {
		m.files = map[string]record.Files{}
	}
	m.files[ref] = files
	return nil
}

func (m *memory) Pull(_ context.Context, ref string) (record.Files, error) {
	m.pulls = append(m.pulls, ref)
	files, ok := m.files[ref]
	if !ok {
		return nil, fmt.Errorf("%s: %w", ref, record.ErrNotFound)
	}
	return files, nil
}

func (m *memory) Tags(_ context.Context, repository string) ([]string, error) {
	tags := slices.Clone(m.ghosts[repository])
	for ref := range m.files {
		if repo, tag, ok := strings.Cut(ref, ":"); ok && repo == repository {
			tags = append(tags, tag)
		}
	}
	slices.Sort(tags)
	return tags, nil
}

// put writes one stream's result under the key it would really carry.
func (m *memory) put(t *testing.T, base, component, version, combination, stream string, result policy.Result) {
	t.Helper()
	rec := record.New(
		policy.ComponentSpec{Component: component, Version: version},
		policy.StatusCombination{
			ID:      combination,
			Streams: []policy.StreamResult{{Name: stream, Result: result}},
		},
	)
	b, err := record.Encode(rec)
	if err != nil {
		t.Fatal(err)
	}
	key := record.Key{Component: component, Version: version, Combination: combination, Stream: stream}
	if err := m.Push(context.Background(), key.Reference(base), record.Files{record.ResultFile: b}); err != nil {
		t.Fatal(err)
	}
}

// testPolicy declares one component and two streams, with a scope covering
// both and a scope covering one.
func testPolicy() *policy.Policy {
	return &policy.Policy{
		Spec: policy.Spec{
			Components: []string{"controller"},
			Streams: []policy.Stream{
				{Name: "amd64", Capabilities: map[string]string{"arch": "amd64", "kube": "1.34"}},
				{Name: "arm64", Capabilities: map[string]string{"arch": "arm64", "kube": "1.34"}},
			},
			Scopes: map[string]policy.Scope{
				"full":   {},
				"active": {Selector: map[string]string{"arch": "amd64"}},
			},
		},
	}
}

const base = "reg/qualification"

func TestQualifiedAtAScopeOnlyWhenEveryStreamPassed(t *testing.T) {
	store := &memory{}
	store.put(t, base, "controller", "v1.0.0", "c1", "amd64", policy.ResultPassed)
	store.put(t, base, "controller", "v1.0.0", "c1", "arm64", policy.ResultUnschedulable)

	a := &Aggregator{Store: store, Source: base, Policy: testPolicy()}
	state, err := a.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	rec := state.Records["controller-v1.0.0"]
	if rec == nil {
		t.Fatal("no record for controller-v1.0.0")
	}
	// A run that set out to cover the full matrix and went half
	// unschedulable contributes only what it proved.
	if got := rec.Status.QualifiedScopes; !slices.Equal(got, []string{"active"}) {
		t.Errorf("got %v, want [active]", got)
	}
}

func TestQualifiedAtEveryScopeWhenBothPassed(t *testing.T) {
	store := &memory{}
	store.put(t, base, "controller", "v1.0.0", "c1", "amd64", policy.ResultPassed)
	store.put(t, base, "controller", "v1.0.0", "c1", "arm64", policy.ResultPassed)

	a := &Aggregator{Store: store, Source: base, Policy: testPolicy()}
	state, err := a.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	got := state.Records["controller-v1.0.0"].Status.QualifiedScopes
	if !slices.Equal(got, []string{"active", "full"}) {
		t.Errorf("got %v, want [active full]", got)
	}
}

func TestFoldIsIncremental(t *testing.T) {
	store := &memory{}
	store.put(t, base, "controller", "v1.0.0", "c1", "amd64", policy.ResultPassed)

	a := &Aggregator{Store: store, Source: base, Policy: testPolicy()}
	first, err := a.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	pullsAfterFirst := len(store.pulls)

	// A second result arrives. Only it should be fetched: the composite tag
	// says which results exist, and the state says which are already in.
	store.put(t, base, "controller", "v1.0.0", "c1", "arm64", policy.ResultPassed)
	second, err := a.Run(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}

	if got := len(store.pulls) - pullsAfterFirst; got != 1 {
		t.Errorf("pulled %d results on the second run, want 1 (pulls: %v)", got, store.pulls)
	}
	if got := second.Records["controller-v1.0.0"].Status.QualifiedScopes; !slices.Equal(got, []string{"active", "full"}) {
		t.Errorf("got %v, want [active full]", got)
	}
}

func TestRebuildFromNothingIsTheSamePath(t *testing.T) {
	store := &memory{}
	store.put(t, base, "controller", "v1.0.0", "c1", "amd64", policy.ResultPassed)
	store.put(t, base, "controller", "v1.0.0", "c1", "arm64", policy.ResultPassed)

	a := &Aggregator{Store: store, Source: base, Policy: testPolicy()}
	incremental, err := a.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Folding again from an empty state must reach the same answer, which is
	// what makes a lost or corrupt state a cache miss rather than an outage.
	rebuilt, err := a.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	x, err := Encode(incremental)
	if err != nil {
		t.Fatal(err)
	}
	y, err := Encode(rebuilt)
	if err != nil {
		t.Fatal(err)
	}
	if string(x[record.StateFile]) != string(y[record.StateFile]) {
		t.Errorf("rebuilding produced a different state:\n%s\nvs\n%s",
			x[record.StateFile], y[record.StateFile])
	}
}

func TestVersionsAreSeparateRecords(t *testing.T) {
	store := &memory{}
	store.put(t, base, "controller", "v1.0.0", "c1", "amd64", policy.ResultPassed)
	store.put(t, base, "controller", "v1.1.0", "c1", "amd64", policy.ResultFailed)

	a := &Aggregator{Store: store, Source: base, Policy: testPolicy()}
	state, err := a.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := len(state.Records); got != 2 {
		t.Fatalf("got %d records, want one per version", got)
	}
	if got := state.Records["controller-v1.0.0"].Status.QualifiedScopes; !slices.Equal(got, []string{"active"}) {
		t.Errorf("v1.0.0: got %v, want [active]", got)
	}
	if got := state.Records["controller-v1.1.0"].Status.QualifiedScopes; len(got) != 0 {
		t.Errorf("v1.1.0 failed a stream, so it is qualified at nothing; got %v", got)
	}
}

func TestSortedRecordsIsStable(t *testing.T) {
	store := &memory{}
	store.put(t, base, "controller", "v1.1.0", "c1", "amd64", policy.ResultPassed)
	store.put(t, base, "controller", "v1.0.0", "c1", "amd64", policy.ResultPassed)

	a := &Aggregator{Store: store, Source: base, Policy: testPolicy()}
	state, err := a.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, rec := range state.SortedRecords() {
		names = append(names, rec.Metadata.Name)
	}
	if !slices.Equal(names, []string{"controller-v1.0.0", "controller-v1.1.0"}) {
		t.Errorf("got %v, want a stable sorted order", names)
	}
}

func TestNoPolicyClaimsNoScope(t *testing.T) {
	store := &memory{}
	store.put(t, base, "controller", "v1.0.0", "c1", "amd64", policy.ResultPassed)

	// Without a policy there is no scope definition, so there is nothing to
	// be qualified against. Claiming one would be inventing coverage.
	a := &Aggregator{Store: store, Source: base, Components: []string{"controller"}}
	state, err := a.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Records["controller-v1.0.0"].Status.QualifiedScopes; len(got) != 0 {
		t.Errorf("got %v, want nothing claimed", got)
	}
}

func TestNoComponentsIsAnError(t *testing.T) {
	// Aggregating nothing would push an empty state over a populated one.
	a := &Aggregator{Store: &memory{}, Source: base}
	if _, err := a.Run(context.Background(), nil); err == nil {
		t.Fatal("want an error when there is nothing to aggregate")
	}
}

func TestDecodeRequiresTheStateFile(t *testing.T) {
	if _, err := Decode(record.Files{"other.yaml": []byte("{}")}); err == nil {
		t.Fatal("want an error for an artifact holding no state")
	}
}

func TestListedButMissingResultIsSkippedNotFatal(t *testing.T) {
	// A tag listed and then gone is a race with a retention policy or a
	// concurrent push, not a reason to abandon every other result.
	store := &memory{ghosts: map[string][]string{base + "/controller": {"v0.9.0_c1_amd64"}}}
	store.put(t, base, "controller", "v1.0.0", "c1", "amd64", policy.ResultPassed)
	store.put(t, base, "controller", "v1.0.0", "c1", "arm64", policy.ResultPassed)

	a := &Aggregator{Store: store, Source: base, Policy: testPolicy()}
	state, err := a.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("a vanished result must not fail the aggregate: %v", err)
	}
	if !slices.Contains(store.pulls, base+"/controller:v0.9.0_c1_amd64") {
		t.Fatalf("the ghost tag was never pulled, so nothing was tested: %v", store.pulls)
	}
	// The results that are there still land.
	if got := state.Records["controller-v1.0.0"].Status.QualifiedScopes; !slices.Equal(got, []string{"active", "full"}) {
		t.Errorf("got %v, want [active full]", got)
	}
}
