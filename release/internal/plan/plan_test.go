// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package plan

import "testing"

// nico is the one provider with a lane today. Every stream in these tests
// declares it, because a stream whose provider has none is now an error rather
// than a cell.
func caps(arch string) map[string]string {
	return map[string]string{"provider": "nico", "arch": arch}
}

func TestMatrixDropsSkippedCells(t *testing.T) {
	// A skipped cell stays in the plan and out of the matrix: the plan is
	// the record of what the scope covers, the matrix is the work.
	p := &Plan{
		Component: "controller",
		Version:   "v1.0.0",
		Combinations: []Combination{{
			ID: "c1",
			Streams: []Stream{
				{Name: "a", Capabilities: caps("amd64")},
				{Name: "b", Skip: OutcomePassed, Capabilities: caps("amd64")},
				{Name: "c", Skip: OutcomeFailed, Capabilities: caps("amd64")},
				{Name: "d", Capabilities: caps("arm64")},
			},
		}},
	}

	cells, err := p.Matrix()
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 2 {
		t.Fatalf("got %d cells, want 2: %+v", len(cells), cells)
	}
	for i, want := range []string{"a", "d"} {
		if cells[i].Stream != want {
			t.Errorf("cell %d is %q, want %q", i, cells[i].Stream, want)
		}
		if cells[i].Component != "controller" || cells[i].Version != "v1.0.0" {
			t.Errorf("cell %d lost the candidate: %+v", i, cells[i])
		}
		if cells[i].Combination != "c1" {
			t.Errorf("cell %d lost the combination: %+v", i, cells[i])
		}
	}
	// A matrix job has to choose a runner before it can read the plan, so
	// the capabilities have to come with the cell.
	if cells[1].Capabilities["arch"] != "arm64" {
		t.Errorf("the capabilities did not come along: %+v", cells[1])
	}
}

func TestMatrixCarriesTheLane(t *testing.T) {
	// The matrix job hands these straight to the reusable workflow. A
	// workflow expression cannot look a provider up in a table, so a cell
	// arriving without its lane would be wired from whatever literal CI had
	// written down.
	p := &Plan{Combinations: []Combination{{
		ID:      "c1",
		Streams: []Stream{{Name: "a", Capabilities: caps("amd64")}},
	}}}

	cells, err := p.Matrix()
	if err != nil {
		t.Fatal(err)
	}
	c := cells[0]
	if c.Backend != "nico" {
		t.Errorf("backend is %q, want nico", c.Backend)
	}
	if c.Filter != "nico-containers" {
		t.Errorf("filter is %q, want nico-containers", c.Filter)
	}
	// A JSON array, because that is what the Tiltfile's input parses. capi_core
	// is required by every backend and the Tiltfile rejects a list without it.
	if c.Providers != `["capi_core","bgp-containers","nico"]` {
		t.Errorf("providers is %q", c.Providers)
	}
	if !c.CAPNICoSource {
		t.Error("the nico lane needs the CAPNICo checkout")
	}
}

func TestMatrixRejectsAStreamNothingCanRun(t *testing.T) {
	// An error, not a dropped cell. Dropping it would shrink the matrix and
	// let the run report on the streams that remained, so a typo in a provider
	// name would read as a narrower qualification rather than a broken policy.
	for _, tc := range []struct {
		name string
		caps map[string]string
	}{
		{"unknown provider", map[string]string{"provider": "forge"}},
		{"no provider", map[string]string{"arch": "amd64"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Plan{Combinations: []Combination{{
				ID:      "c1",
				Streams: []Stream{{Name: "s", Capabilities: tc.caps}},
			}}}
			if _, err := p.Matrix(); err == nil {
				t.Fatal("want an error rather than a quietly smaller matrix")
			}
		})
	}
}

func TestMatrixOfAFullySkippedPlanIsEmpty(t *testing.T) {
	p := &Plan{Combinations: []Combination{{
		ID:      "c1",
		Streams: []Stream{{Name: "a", Skip: OutcomePassed}},
	}}}
	got, err := p.Matrix()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want no cells", got)
	}
}

func TestMatrixSpansCombinations(t *testing.T) {
	p := &Plan{
		Component: "controller",
		Combinations: []Combination{
			{ID: "c1", Streams: []Stream{{Name: "a", Capabilities: caps("amd64")}}},
			{ID: "c2", Streams: []Stream{{Name: "a", Capabilities: caps("amd64")}}},
		},
	}
	cells, err := p.Matrix()
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 2 {
		t.Fatalf("got %d cells, want one per combination", len(cells))
	}
	// Same stream name under two combinations is two different jobs, because
	// the dependency versions differ.
	if cells[0].Combination == cells[1].Combination {
		t.Errorf("the combinations collapsed: %+v", cells)
	}
}
