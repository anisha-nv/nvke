// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"fmt"
	"slices"
	"strings"
)

// Result is what became of one stream. The vocabulary lives here rather than
// in plan because it is the durable form: a published record carries it, and
// the Planner reads it back to decide whether a cell still needs running.
type Result string

// The results a stream can carry. Passed and Failed are settled, because
// rerunning the same candidate proves nothing either way. Error and
// Unschedulable are not, because the answer is still unknown.
const (
	ResultNone          Result = ""
	ResultPassed        Result = "passed"
	ResultFailed        Result = "failed"
	ResultError         Result = "error"
	ResultUnschedulable Result = "unschedulable"
)

// Settled reports whether this result answers the question the run asked.
func (r Result) Settled() bool {
	return r == ResultPassed || r == ResultFailed
}

// Status is what a run adds to a ComponentQualification, and what aggregate
// merges. A declaration has none.
type Status struct {
	Combinations []StatusCombination `json:"combinations"`

	// QualifiedScopes is computed by aggregate and stored nowhere else. It is
	// a set rather than a boolean because a version qualified at one scope by
	// a nightly gains another once a weekly fills in the rest.
	QualifiedScopes []string `json:"qualifiedScopes,omitempty"`
}

// StatusCombination is one resolved dependency set and what became of each
// stream under it.
type StatusCombination struct {
	ID           string               `json:"id"`
	Dependencies []ResolvedDependency `json:"dependencies,omitempty"`
	Streams      []StreamResult       `json:"streams"`
}

// ResolvedDependency is a dependency with its selector already resolved.
type ResolvedDependency struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// StreamResult is one cell's outcome.
//
// Scope and RunURL sit here rather than above the combinations because a
// record that has collected several results holds streams from several runs,
// and each then carries the conditions it ran under.
type StreamResult struct {
	Name string `json:"name"`

	// Scope is what the run that produced this result set out to cover.
	// Coverage is computed from which streams passed rather than from this,
	// so a run that went half unschedulable contributes only what it proved.
	Scope string `json:"scope"`

	Result  Result   `json:"result"`
	Summary *Summary `json:"summary,omitempty"`

	// RunURL is how a reader reaches the logs, for as long as the CI system
	// keeps them. Diagnostics stay artifacts of that run rather than entering
	// the durable record.
	RunURL string `json:"runUrl,omitempty"`

	// Teardown is present only when teardown did not complete, and is
	// independent of Result: a stream can pass and still leave hardware
	// running.
	Teardown *Teardown `json:"teardown,omitempty"`
}

// Summary is the count of specs behind a result.
type Summary struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// Teardown names what a run did not clean up.
type Teardown struct {
	Remaining []Remaining `json:"remaining"`
}

// Remaining is one piece of hardware still running.
type Remaining struct {
	Instance string `json:"instance"`
	Site     string `json:"site,omitempty"`
}

// ResultName is metadata.name for a result record: the candidate rather than
// the component, so a result is addressable on its own while the declaration
// it came from is not per-version.
func ResultName(component, version string) string {
	return component + "-" + version
}

// Merge folds other's streams into s, matching on combination id. Merging is
// a union rather than a conversion, which is what the two kinds being one
// kind buys.
//
// A stream already present is replaced, because the later result is the
// current answer: a cell that errored and was rerun should read as whatever
// the rerun found.
func (s *Status) Merge(other *Status) {
	if other == nil {
		return
	}
	for _, oc := range other.Combinations {
		i := slices.IndexFunc(s.Combinations, func(c StatusCombination) bool { return c.ID == oc.ID })
		if i < 0 {
			s.Combinations = append(s.Combinations, oc)
			continue
		}
		for _, os := range oc.Streams {
			j := slices.IndexFunc(s.Combinations[i].Streams, func(r StreamResult) bool { return r.Name == os.Name })
			if j < 0 {
				s.Combinations[i].Streams = append(s.Combinations[i].Streams, os)
			} else {
				s.Combinations[i].Streams[j] = os
			}
		}
		// Dependencies are a property of the combination rather than of a
		// run, so the first result to name them wins and a later one that
		// somehow disagrees is not allowed to rewrite history silently.
		if len(s.Combinations[i].Dependencies) == 0 {
			s.Combinations[i].Dependencies = oc.Dependencies
		}
	}
	s.sort()
}

// sort puts combinations and streams in a stable order, so a record pushed
// twice from the same results is byte-identical.
func (s *Status) sort() {
	slices.SortFunc(s.Combinations, func(a, b StatusCombination) int {
		return strings.Compare(a.ID, b.ID)
	})
	for i := range s.Combinations {
		slices.SortFunc(s.Combinations[i].Streams, func(a, b StreamResult) int {
			return strings.Compare(a.Name, b.Name)
		})
	}
}

// Passed reports whether every named stream has a passing result under any
// combination. A stream with no result at all is not a pass, which is what
// makes an absent check render as not-run rather than count quietly.
func (s *Status) Passed(streams []string) bool {
	if len(streams) == 0 {
		// A scope that selects nothing is not qualified by vacuous truth. It
		// means the selector matched no stream, which is a policy error the
		// loader catches, not a qualification.
		return false
	}
	for _, name := range streams {
		if !s.streamPassed(name) {
			return false
		}
	}
	return true
}

func (s *Status) streamPassed(name string) bool {
	for _, c := range s.Combinations {
		for _, r := range c.Streams {
			if r.Name == name && r.Result == ResultPassed {
				return true
			}
		}
	}
	return false
}

// Outcome reports what became of one cell, ResultNone if it never ran.
func (s *Status) Outcome(combination, stream string) Result {
	for _, c := range s.Combinations {
		if c.ID != combination {
			continue
		}
		for _, r := range c.Streams {
			if r.Name == stream {
				return r.Result
			}
		}
	}
	return ResultNone
}

// LoadQualification reads a published result or the central artifact.
//
// It is separate from LoadComponent because a result's metadata.name is the
// candidate rather than the component, which LoadComponent rejects on
// purpose: in a declaration that mismatch is a mistake.
func LoadQualification(path string) (*Component, error) {
	var c Component
	if err := readStrict(path, &c); err != nil {
		return nil, err
	}
	if err := checkTypeMeta(path, c.APIVersion, c.Kind, KindComponentQualification); err != nil {
		return nil, err
	}
	if c.Spec.Component == "" {
		return nil, fmt.Errorf("%s: spec.component is empty", path)
	}
	if c.Spec.Version == "" {
		return nil, fmt.Errorf("%s: spec.version is empty, so this is a declaration rather than a result", path)
	}
	return &c, nil
}
