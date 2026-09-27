// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package plan turns a trigger into a run list.
//
// The Planner is always invoked for one component. It reads that component's
// qualification spec and the release-wide policy, resolves each dependency's
// selectors, and expands the named scope's selector over the stream list,
// pairing the result with each combination. It is stateless and holds no
// cache, so the same inputs always produce the same plan in the same order.
package plan

import (
	"errors"
	"fmt"

	"github.com/dsx-ai-factory/nke/release/internal/lane"
	"github.com/dsx-ai-factory/nke/release/internal/policy"
)

// errNoCandidate is returned when no version survives a dependency's
// constraint. It fails here, before anything is scheduled, rather than
// producing a plan nothing can run.
var errNoCandidate = errors.New("no version satisfies the constraint")

// Plan is what `qualify plan` writes and every later verb reads.
type Plan struct {
	Component    string        `json:"component"`
	Version      string        `json:"version"`
	Scope        string        `json:"scope"`
	Combinations []Combination `json:"combinations"`
}

// Combination is one resolved set of dependency versions, to be qualified
// against every stream in the scope.
type Combination struct {
	ID           string       `json:"id"`
	Dependencies []Dependency `json:"dependencies"`
	Streams      []Stream     `json:"streams"`
}

// Dependency is one dependency with its selector resolved to a version.
type Dependency struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Stream is one cell of the matrix.
type Stream struct {
	Name string `json:"name"`

	// Capabilities is what the stream declared in the policy, copied here so
	// the run that executes this cell is configured from the plan rather than
	// by re-reading the policy. Resolution happens once; a policy edited
	// between the plan and the last cell must not change what the last cell
	// runs.
	Capabilities map[string]string `json:"capabilities"`

	// Skip says why the Planner decided against running this cell, and is
	// empty when it will run. Skipped cells stay in the file rather than
	// being dropped, so a plan covering four of six streams says so instead
	// of looking like a smaller matrix.
	Skip Outcome `json:"skip,omitempty"`
}

// Cell is one runnable cell with the nesting flattened away.
//
// A matrix job takes a flat list, and flattening it here rather than in the
// workflow keeps the arithmetic of what runs in code that is tested. The
// fields are what `qualify run` needs to identify itself and nothing more:
// the dependency versions stay in the plan, which the run reads.
type Cell struct {
	Component   string `json:"component"`
	Version     string `json:"version"`
	Combination string `json:"combination"`
	Stream      string `json:"stream"`

	// Capabilities is the stream's, carried here because a matrix job has to
	// choose a runner before it can read the plan. It is the declaration
	// rather than a runner label: which label satisfies an architecture is
	// the workflow's knowledge, not the policy's.
	Capabilities map[string]string `json:"capabilities"`

	// Backend, Filter, Providers and CAPNICoSource are the lane that will run
	// this cell, resolved here so the matrix job can pass them straight to
	// the reusable workflow. A workflow expression cannot look a provider up
	// in a table, so leaving this to CI would mean one provider's wiring
	// applied to every stream, whatever its provider said.
	Backend       string `json:"backend"`
	Filter        string `json:"filter"`
	Providers     string `json:"providers"`
	CAPNICoSource bool   `json:"capnicoSource"`
}

// Matrix is the cells that will actually run.
//
// Skipped cells are dropped here, having been kept in the plan itself: the
// plan is the record of what the scope covers, and the matrix is the work.
// Scheduling a job to skip would spend a runner to print a decision the plan
// already states.
//
// An error rather than a silent omission when a stream's provider has no lane.
// Dropping the cell would shrink the matrix and let the run report on the
// streams that remained, so a typo in a provider name would read as a narrower
// qualification rather than as a broken policy.
func (p *Plan) Matrix() ([]Cell, error) {
	var cells []Cell
	for _, c := range p.Combinations {
		for _, s := range c.Streams {
			if s.Skip != OutcomeNone {
				continue
			}
			l, err := lane.For(s.Capabilities["provider"])
			if err != nil {
				return nil, fmt.Errorf("stream %q: %w", s.Name, err)
			}
			cells = append(cells, Cell{
				Component:     p.Component,
				Version:       p.Version,
				Combination:   c.ID,
				Stream:        s.Name,
				Capabilities:  s.Capabilities,
				Backend:       l.Backend,
				Filter:        l.Filter(),
				Providers:     l.ProvidersJSON(),
				CAPNICoSource: l.NeedsCAPNICoSource,
			})
		}
	}
	return cells, nil
}

// Outcome is what became of a cell in an earlier run. It is policy's Result
// under a name that reads better at the Planner's call site: there, the
// question is what happened last time rather than what this run produced.
type Outcome = policy.Result

// The outcomes a cell can carry.
const (
	OutcomeNone          = policy.ResultNone
	OutcomePassed        = policy.ResultPassed
	OutcomeFailed        = policy.ResultFailed
	OutcomeError         = policy.ResultError
	OutcomeUnschedulable = policy.ResultUnschedulable
)
