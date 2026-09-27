// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package plan

import "testing"

func TestMatrixDropsSkippedCells(t *testing.T) {
	// A skipped cell stays in the plan and out of the matrix: the plan is
	// the record of what the scope covers, the matrix is the work.
	p := &Plan{
		Component: "controller",
		Version:   "v1.0.0",
		Combinations: []Combination{{
			ID: "c1",
			Streams: []Stream{
				{Name: "a", Capabilities: map[string]string{"arch": "amd64"}},
				{Name: "b", Skip: OutcomePassed},
				{Name: "c", Skip: OutcomeFailed},
				{Name: "d", Capabilities: map[string]string{"arch": "arm64"}},
			},
		}},
	}

	cells := p.Matrix()
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

func TestMatrixOfAFullySkippedPlanIsEmpty(t *testing.T) {
	p := &Plan{Combinations: []Combination{{
		ID:      "c1",
		Streams: []Stream{{Name: "a", Skip: OutcomePassed}},
	}}}
	if got := p.Matrix(); len(got) != 0 {
		t.Errorf("got %+v, want no cells", got)
	}
}

func TestMatrixSpansCombinations(t *testing.T) {
	p := &Plan{
		Component: "controller",
		Combinations: []Combination{
			{ID: "c1", Streams: []Stream{{Name: "a"}}},
			{ID: "c2", Streams: []Stream{{Name: "a"}}},
		},
	}
	cells := p.Matrix()
	if len(cells) != 2 {
		t.Fatalf("got %d cells, want one per combination", len(cells))
	}
	// Same stream name under two combinations is two different jobs, because
	// the dependency versions differ.
	if cells[0].Combination == cells[1].Combination {
		t.Errorf("the combinations collapsed: %+v", cells)
	}
}
