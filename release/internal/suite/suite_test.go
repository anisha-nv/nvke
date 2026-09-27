// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package suite

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nke/release/internal/policy"
)

// report builds a ginkgo report with the fields this package reads. The real
// one carries some fifty more, so constructing it here keeps the tests about
// the parsing rather than about ginkgo's schema.
//
// selected is PreRunStats.SpecsThatWillRun, which is what the label filter
// chose. It is passed explicitly rather than derived from specs because the
// gap between the two is the thing several of these tests are about.
func report(selected int, specs string) []byte {
	return []byte(`[{"PreRunStats":{"TotalSpecs":272,"SpecsThatWillRun":` +
		strconv.Itoa(selected) + `},"SpecReports":[` + specs + `]}]`)
}

func spec(leaf, state, labels, containers string) string {
	return `{"LeafNodeType":"` + leaf + `","State":"` + state +
		`","Labels":[` + labels + `],"ContainerHierarchyLabels":[` + containers + `]}`
}

func TestParseSummaryCountsSpecsOnly(t *testing.T) {
	// BeforeSuite and AfterSuite are reported passed when they succeed. A run
	// that passed no spec must not be credited with two.
	b := report(3, strings.Join([]string{
		spec("BeforeSuite", "passed", "", ""),
		spec("It", "failed", "", ""),
		spec("It", "skipped", "", ""),
		spec("It", "skipped", "", ""),
		spec("AfterSuite", "passed", "", ""),
	}, ","))

	got, err := parseSummary(b)
	if err != nil {
		t.Fatal(err)
	}
	want := policy.Summary{Passed: 0, Failed: 1, Skipped: 2}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseSummaryStates(t *testing.T) {
	b := report(5, strings.Join([]string{
		spec("It", "passed", "", ""),
		spec("It", "panicked", "", ""),
		spec("It", "interrupted", "", ""),
		spec("It", "aborted", "", ""),
		spec("It", "pending", "", ""),
	}, ","))

	got, err := parseSummary(b)
	if err != nil {
		t.Fatal(err)
	}
	want := policy.Summary{Passed: 1, Failed: 3, Skipped: 1}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseSummaryIgnoresWhatTheFilterExcluded(t *testing.T) {
	// Measured from a real run: --label-filter=nico-containers selected 69 of
	// 272 specs, and the 203 the filter excluded were reported skipped along
	// with the 64 an Ordered container cut short. Counting skipped states gave
	// "267 skipped", which reads as a suite that tested almost nothing.
	//
	// Here: 4 specs in the suite, 2 selected, 1 failed. The one skip belongs
	// to the run; the two exclusions do not.
	b := report(2, strings.Join([]string{
		spec("It", "failed", "", ""),
		spec("It", "skipped", "", ""),
		spec("It", "skipped", "", ""),
		spec("It", "skipped", "", ""),
	}, ","))

	got, err := parseSummary(b)
	if err != nil {
		t.Fatal(err)
	}
	want := policy.Summary{Passed: 0, Failed: 1, Skipped: 1}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseSummaryEmptyIsAnError(t *testing.T) {
	// A report with no spec results means the suite did not run anything.
	// Returning a zero summary would let that read as a clean pass.
	if _, err := parseSummary(report(0, "")); err == nil {
		t.Fatal("want an error for a report holding no spec results")
	}
}

func TestParseNeedsTakesTheMaximum(t *testing.T) {
	// Two specs under one Ordered container share a site controller, so the
	// requirement is the larger of the two rather than their sum.
	b := report(2, strings.Join([]string{
		spec("It", "passed", `"needs:cpu=3"`, `["lifecycle"]`),
		spec("It", "passed", `"needs:cpu=5"`, `["lifecycle"]`),
		spec("It", "skipped", `"needs:cpu=99"`, `["lifecycle"]`),
	}, ","))

	nodes, selected, err := parseNeeds(b)
	if err != nil {
		t.Fatal(err)
	}
	if selected != 2 {
		t.Errorf("selected %d specs, want 2", selected)
	}
	if want := map[string]int{"cpu": 5}; !maps.Equal(nodes, want) {
		t.Errorf("got %v, want %v", nodes, want)
	}
}

func TestParseNeedsReadsTheContainerHierarchy(t *testing.T) {
	// The hierarchy is nested, one slice per level, and a label on an
	// Ordered Describe applies to every spec beneath it. Reading only the
	// spec's own labels would miss the declaration entirely.
	b := report(1, spec("It", "passed", `"lifecycle"`, `["nico-containers","needs:gpu=2"],["needs:cpu=4"]`))

	nodes, selected, err := parseNeeds(b)
	if err != nil {
		t.Fatal(err)
	}
	if selected != 1 {
		t.Errorf("selected %d specs, want 1", selected)
	}
	if want := map[string]int{"gpu": 2, "cpu": 4}; !maps.Equal(nodes, want) {
		t.Errorf("got %v, want %v", nodes, want)
	}
}

func TestParseNeedsNoLabelsIsNotAnError(t *testing.T) {
	// A suite that declares no hardware is the state of the tree today. It
	// must yield an empty requirement rather than a failure.
	nodes, selected, err := parseNeeds(report(1, spec("It", "passed", "", "")))
	if err != nil {
		t.Fatal(err)
	}
	if selected != 1 || nodes != nil {
		t.Errorf("got %v specs=%d, want no nodes and 1 spec", nodes, selected)
	}
}

func TestParseNeed(t *testing.T) {
	for _, tc := range []struct {
		label string
		kind  string
		count int
		ok    bool
	}{
		{"needs:cpu=3", "cpu", 3, true},
		{"needs:gb200=1", "gb200", 1, true},
		{"needs:cpu=0", "cpu", 0, true},
		{"lifecycle", "", 0, false},
		{"needs:cpu", "", 0, false},
		{"needs:=3", "", 0, false},
		{"needs:cpu=many", "", 0, false},
		{"needs:cpu=-1", "", 0, false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			kind, count, ok := parseNeed(tc.label)
			if kind != tc.kind || count != tc.count || ok != tc.ok {
				t.Errorf("got (%q, %d, %v), want (%q, %d, %v)",
					kind, count, ok, tc.kind, tc.count, tc.ok)
			}
		})
	}
}

func TestEntryVersionsIsOrdered(t *testing.T) {
	// The suite is told the same thing in the same order for the same entry,
	// because the string lands in the record a person reads.
	e := Entry{
		Component: "controller",
		Version:   "v0.1.1-rc.8",
		Dependencies: []policy.ResolvedDependency{
			{Name: "os-image", Version: "v0.1.0-rc.4"},
			{Name: "cli", Version: "v0.1.0-rc.7"},
		},
	}
	want := "controller=v0.1.1-rc.8,cli=v0.1.0-rc.7,os-image=v0.1.0-rc.4"
	if got := e.versions(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEntryCapabilitiesDropsProvider(t *testing.T) {
	// provider selects the label filter and the capacity client, both of
	// which are the tool's concern. The suite has no use for it.
	e := Entry{Capabilities: map[string]string{"provider": "nico", "arch": "amd64", "kube": "1.34"}}
	if got, want := e.capabilities(), "arch=amd64,kube=1.34"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFilterRejectsAnUnknownProvider(t *testing.T) {
	// Falling back to some default filter would run a different selection
	// than the stream asked for and record it under the stream's name.
	if _, err := (Ginkgo{}).filter(Entry{Capabilities: map[string]string{"provider": "forge"}}); err == nil {
		t.Fatal("want an error for a provider with no label filter")
	}
	if _, err := (Ginkgo{}).filter(Entry{Stream: "s"}); err == nil {
		t.Fatal("want an error for a stream declaring no provider")
	}
}

func TestSuiteArgsCarryTheSelection(t *testing.T) {
	g := Ginkgo{Environment: "forge"}
	e := Entry{
		Component:    "controller",
		Version:      "v1.0.0",
		Capabilities: map[string]string{"provider": "nico", "arch": "arm64"},
	}
	args := g.suiteArgs(e)

	// Everything the suite's own flags parse has to sit after the separator,
	// or ginkgo consumes it.
	sep := slices.Index(args, "--")
	if sep < 0 {
		t.Fatalf("no -- separator in %v", args)
	}
	for _, flag := range []string{"-e2e.environment", "-e2e.versions", "-e2e.capabilities"} {
		if i := slices.Index(args, flag); i < sep {
			t.Errorf("%s at %d is not after the separator at %d: %v", flag, i, sep, args)
		}
	}
}

func TestSuiteArgsOmitsWhatWasNotAsked(t *testing.T) {
	// An empty flag is left out rather than passed empty, so the suite's own
	// default applies instead of being overridden with nothing.
	args := (Ginkgo{}).suiteArgs(Entry{Component: "controller", Version: "v1.0.0"})
	for _, flag := range []string{"-e2e.environment", "-e2e.capabilities"} {
		if slices.Contains(args, flag) {
			t.Errorf("%s passed with nothing to say: %v", flag, args)
		}
	}
	if !slices.Contains(args, "-e2e.versions") {
		t.Errorf("-e2e.versions is always known: %v", args)
	}
}
