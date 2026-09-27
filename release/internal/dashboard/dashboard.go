// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package dashboard renders the central artifact as a static page.
//
// A generator and a page deploy is the whole of it: no server, no API and no
// database. It is read-only, so it can run whenever, including outside the
// qualification workflow entirely. Keeping it separate from aggregate is what
// keeps the Planner's input independent of the dashboard, so the page can be
// replaced without touching the data path.
package dashboard

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"maps"
	"slices"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/dsx-ai-factory/nke/release/internal/aggregate"
	"github.com/dsx-ai-factory/nke/release/internal/policy"
)

//go:embed page.html.tmpl
var files embed.FS

// Page is what the template renders.
type Page struct {
	Components []Component
	Scopes     []string
}

// Component is one component's most recent qualification.
type Component struct {
	Name    string
	Version string

	// Qualified is the scopes this version covers, and is empty when it
	// covers none. A component with results and no qualified scope is the
	// ordinary state mid-matrix, not an error.
	Qualified []string

	Streams []Stream
}

// Stream is one cell as the page shows it.
type Stream struct {
	Name        string
	Combination string
	Result      string

	// Class is the CSS class for the result. A result with no class renders
	// as not-run, which is what keeps an absent check from reading as a pass.
	Class string

	Summary string
	RunURL  string
}

// Render writes the page.
func Render(w io.Writer, state *aggregate.State, p *policy.Policy) error {
	tmpl, err := template.ParseFS(files, "page.html.tmpl")
	if err != nil {
		return fmt.Errorf("parsing the template: %w", err)
	}
	return tmpl.Execute(w, build(state, p))
}

// build turns the state into what the page shows: for each component the most
// recent qualification, the scopes it covers, and which streams passed.
func build(state *aggregate.State, p *policy.Policy) Page {
	page := Page{}
	if p != nil {
		page.Scopes = slices.Sorted(maps.Keys(p.Spec.Scopes))
	}

	// One entry per component, keeping the newest version seen. The records
	// are sorted by name, which for a fixed component sorts by version
	// string; that is not semver order, so the newest is chosen by comparing
	// versions rather than by taking the last.
	newest := map[string]*policy.Component{}
	for _, rec := range state.SortedRecords() {
		name := rec.Spec.Component
		if got, ok := newest[name]; !ok || newer(rec.Spec.Version, got.Spec.Version) {
			newest[name] = rec
		}
	}

	for _, name := range slices.Sorted(maps.Keys(newest)) {
		rec := newest[name]
		c := Component{Name: name, Version: rec.Spec.Version}
		if rec.Status != nil {
			c.Qualified = rec.Status.QualifiedScopes
			for _, comb := range rec.Status.Combinations {
				for _, s := range comb.Streams {
					c.Streams = append(c.Streams, stream(comb.ID, s))
				}
			}
		}
		page.Components = append(page.Components, c)
	}
	return page
}

func stream(combination string, s policy.StreamResult) Stream {
	out := Stream{
		Name:        s.Name,
		Combination: combination,
		Result:      string(s.Result),
		RunURL:      s.RunURL,
	}
	switch s.Result {
	case policy.ResultPassed:
		out.Class = "passed"
	case policy.ResultFailed:
		out.Class = "failed"
	case policy.ResultError:
		out.Class = "error"
	case policy.ResultUnschedulable:
		out.Class = "unschedulable"
	default:
		out.Result = "not run"
		out.Class = "not-run"
	}
	if s.Summary != nil {
		out.Summary = fmt.Sprintf("%d passed, %d failed, %d skipped",
			s.Summary.Passed, s.Summary.Failed, s.Summary.Skipped)
	}
	return out
}

// newer reports whether a is the later of two versions of one component.
//
// Both are release tags, so semver decides it. A version semver cannot read
// loses to one it can, which keeps an unparseable tag from displacing a real
// one; between two unparseable tags the order is lexical, which is arbitrary
// but stable.
func newer(a, b string) bool {
	av, bv := "v"+strings.TrimPrefix(a, "v"), "v"+strings.TrimPrefix(b, "v")
	switch {
	case semver.IsValid(av) && semver.IsValid(bv):
		return semver.Compare(av, bv) > 0
	case semver.IsValid(av):
		return true
	case semver.IsValid(bv):
		return false
	default:
		return a > b
	}
}
