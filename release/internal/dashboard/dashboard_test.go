// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nke/release/internal/aggregate"
	"github.com/dsx-ai-factory/nke/release/internal/policy"
)

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		// The case a lexical compare gets wrong, and this repository is
		// already past rc.9 on two components.
		{"v0.1.0-rc.10", "v0.1.0-rc.4", true},
		{"v0.10.0", "v0.9.0", true},
		{"v1.0.0", "v1.0.0-rc.1", true},
		{"v0.9.0", "v0.10.0", false},
		{"v1.0.0", "v1.0.0", false},
		// The leading v is optional in a policy file.
		{"1.1.0", "v1.0.0", true},
		// A version semver cannot read loses to one it can, so an
		// unparseable tag never displaces a real one.
		{"v1.0.0", "not-a-version", true},
		{"not-a-version", "v1.0.0", false},
	} {
		t.Run(tc.a+" vs "+tc.b, func(t *testing.T) {
			if got := newer(tc.a, tc.b); got != tc.want {
				t.Errorf("newer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func record(version string, streams ...policy.StreamResult) *policy.Component {
	return &policy.Component{
		Metadata: policy.Metadata{Name: policy.ResultName("controller", version)},
		Spec:     policy.ComponentSpec{Component: "controller", Version: version},
		Status: &policy.Status{
			Combinations: []policy.StatusCombination{{ID: "c1", Streams: streams}},
		},
	}
}

func TestBuildShowsTheMostRecentVersion(t *testing.T) {
	state := &aggregate.State{Records: map[string]*policy.Component{
		"controller-v0.1.0-rc.4":  record("v0.1.0-rc.4", policy.StreamResult{Name: "amd64", Result: policy.ResultPassed}),
		"controller-v0.1.0-rc.10": record("v0.1.0-rc.10", policy.StreamResult{Name: "amd64", Result: policy.ResultFailed}),
	}}

	page := build(state, nil)
	if len(page.Components) != 1 {
		t.Fatalf("got %d components, want one row per component", len(page.Components))
	}
	if got := page.Components[0].Version; got != "v0.1.0-rc.10" {
		t.Errorf("got %q, want the most recent version", got)
	}
}

func TestStreamClassNeverMakesAnAbsentCheckLookLikeAPass(t *testing.T) {
	for _, tc := range []struct {
		result policy.Result
		class  string
	}{
		{policy.ResultPassed, "passed"},
		{policy.ResultFailed, "failed"},
		{policy.ResultError, "error"},
		{policy.ResultUnschedulable, "unschedulable"},
		{policy.ResultNone, "not-run"},
	} {
		t.Run(string(tc.result), func(t *testing.T) {
			got := stream("c1", policy.StreamResult{Name: "s", Result: tc.result})
			if got.Class != tc.class {
				t.Errorf("got class %q, want %q", got.Class, tc.class)
			}
			if tc.result == policy.ResultNone && got.Result != "not run" {
				t.Errorf("an absent result renders as %q, want %q", got.Result, "not run")
			}
		})
	}
}

func TestRenderCarriesTheResults(t *testing.T) {
	state := &aggregate.State{Records: map[string]*policy.Component{
		"controller-v1.0.0": record("v1.0.0",
			policy.StreamResult{
				Name:    "nico-amd64-kube-1.34",
				Result:  policy.ResultPassed,
				Summary: &policy.Summary{Passed: 5, Skipped: 267},
				RunURL:  "https://example.invalid/runs/1",
			},
			policy.StreamResult{Name: "nico-arm64-kube-1.34", Result: policy.ResultUnschedulable},
		),
	}}
	state.Records["controller-v1.0.0"].Status.QualifiedScopes = []string{"active"}

	var out bytes.Buffer
	if err := Render(&out, state, nil); err != nil {
		t.Fatal(err)
	}
	page := out.String()

	for _, want := range []string{
		"controller", "v1.0.0", "active",
		"nico-amd64-kube-1.34", "nico-arm64-kube-1.34",
		"5 passed, 0 failed, 267 skipped",
		"https://example.invalid/runs/1",
		"unschedulable",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page does not carry %q", want)
		}
	}
}

func TestRenderAnEmptyStateSaysSo(t *testing.T) {
	// A page with no rows must say there are no results, not render an empty
	// table that reads as a matrix nobody failed.
	var out bytes.Buffer
	if err := Render(&out, &aggregate.State{}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No results yet") {
		t.Errorf("an empty page does not say so:\n%s", out.String())
	}
}

func TestRenderWithoutStatusDoesNotPanic(t *testing.T) {
	// A record with no status is what a fold of a declaration would leave.
	// The page has to survive it.
	state := &aggregate.State{Records: map[string]*policy.Component{
		"controller-v1.0.0": {
			Metadata: policy.Metadata{Name: "controller-v1.0.0"},
			Spec:     policy.ComponentSpec{Component: "controller", Version: "v1.0.0"},
		},
	}}
	var out bytes.Buffer
	if err := Render(&out, state, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing yet") {
		t.Errorf("a record with no status must read as qualified at nothing:\n%s", out.String())
	}
}
