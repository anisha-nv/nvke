// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package suite owns every invocation of the e2e suite, in both directions:
// asking what a selection needs, and running it.
//
// The suite is invoked as a subprocess rather than imported, which is what
// keeps it runnable by hand with exactly the arguments this package builds,
// and runnable by a second organisation against their own environment.
//
// This package owns no test logic. All cluster lifecycle and validation stays
// in the suite; what crosses the boundary is arguments.
package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/nke/release/internal/capacity"
	"github.com/dsx-ai-factory/nke/release/internal/lane"
	"github.com/dsx-ai-factory/nke/release/internal/policy"
)

// needsPrefix marks a label that declares hardware. The declaration sits on
// the container that consumes the nodes, beside the labels already there, so
// changing the cluster shape and changing the requirement are one edit.
const needsPrefix = "needs:"

// Entry is one cell of the plan: a resolved version set and the stream to run
// it against. `run` looks one up; nothing here resolves anything.
type Entry struct {
	Component    string
	Version      string
	Dependencies []policy.ResolvedDependency
	Stream       string
	Capabilities map[string]string
}

// Provider is the stream's provider capability, which decides both the label
// filter and which capacity provider speaks for the run.
func (e Entry) Provider() string { return e.Capabilities["provider"] }

// versions renders the version set as the suite's -e2e.versions argument,
// with the component under test first and the dependencies after it in a
// stable order.
func (e Entry) versions() string {
	parts := []string{e.Component + "=" + e.Version}
	deps := slices.Clone(e.Dependencies)
	slices.SortFunc(deps, func(a, b policy.ResolvedDependency) int {
		return strings.Compare(a.Name, b.Name)
	})
	for _, d := range deps {
		parts = append(parts, d.Name+"="+d.Version)
	}
	return strings.Join(parts, ",")
}

// capabilities renders the stream's capabilities as the suite's
// -e2e.capabilities argument. provider is omitted: it selected the specs
// already, and the suite has no use for it.
func (e Entry) capabilities() string {
	parts := make([]string, 0, len(e.Capabilities))
	for _, k := range slices.Sorted(maps.Keys(e.Capabilities)) {
		if k == "provider" {
			continue
		}
		parts = append(parts, k+"="+e.Capabilities[k])
	}
	return strings.Join(parts, ",")
}

// Result is what one invocation of the suite produced.
type Result struct {
	Outcome policy.Result
	Summary policy.Summary

	// Report is the CTRF-shaped report the suite wrote, verbatim. It is
	// carried rather than parsed further because publish stores it as-is.
	Report []byte
}

// Client owns every invocation of the suite.
type Client interface {
	// Requirement selects without running, and reports what the selection
	// needs.
	Requirement(ctx context.Context, e Entry) (capacity.Requirement, error)

	// Execute runs the same selection for real, against held capacity.
	Execute(ctx context.Context, e Entry, l capacity.Lease) (Result, error)
}

// Ginkgo runs the suite as a subprocess.
type Ginkgo struct {
	// Dir is the suite's package directory.
	Dir string

	// Environment is the e2e environment name the run was configured with.
	// It is the Client's rather than the plan's: no site or instance
	// identifier crosses the boundary, because the suite resolves those from
	// its own environment file. That is what lets a second environment with
	// the same capabilities be a config change rather than a tooling change.
	Environment string

	// Bin is the ginkgo binary. Empty means the one on PATH.
	Bin string

	// ReportDir is where Execute writes the suite's report. Empty means a
	// temporary directory that is not kept.
	ReportDir string

	// Stdout and Stderr receive the suite's output during Execute. A run's
	// console output is how a person follows a thirty-minute build, so it is
	// streamed rather than captured.
	Stdout, Stderr *os.File
}

// Requirement asks the suite what a selection needs, without running it.
//
// Both methods build the same invocation and differ only by --dry-run, so the
// specs a requirement describes are by construction the specs that will run.
func (g Ginkgo) Requirement(ctx context.Context, e Entry) (capacity.Requirement, error) {
	f, err := g.filter(e)
	if err != nil {
		return capacity.Requirement{}, err
	}

	report := filepath.Join(os.TempDir(), "qualify-dryrun.json")
	args := append([]string{
		"--dry-run",
		"--label-filter=" + f.Filter(),
		"--json-report=" + report,
	}, g.suiteArgs(e)...)

	if out, err := g.command(ctx, args).CombinedOutput(); err != nil {
		return capacity.Requirement{}, fmt.Errorf("selecting specs for %s: %w\n%s", e.Stream, err, out)
	}
	defer os.Remove(report)

	b, err := os.ReadFile(report)
	if err != nil {
		return capacity.Requirement{}, fmt.Errorf("reading the dry-run report: %w", err)
	}
	nodes, selected, err := parseNeeds(b)
	if err != nil {
		return capacity.Requirement{}, err
	}
	if selected == 0 {
		// ginkgo exits zero having run nothing, so this has to be caught
		// here or the run reports a pass it never earned.
		return capacity.Requirement{}, fmt.Errorf(
			"label filter %q selected no specs, so a run would pass without asserting anything", f.Filter(),
		)
	}
	return capacity.Requirement{Stream: e.Stream, Nodes: nodes}, nil
}

// Execute runs the selection for real. The lease is taken to make the call
// sequence explicit at the type level: there is no path that runs the suite
// without having acquired capacity first.
func (g Ginkgo) Execute(ctx context.Context, e Entry, _ capacity.Lease) (Result, error) {
	f, err := g.filter(e)
	if err != nil {
		return Result{}, err
	}

	dir := g.ReportDir
	if dir == "" {
		if dir, err = os.MkdirTemp("", "qualify-run"); err != nil {
			return Result{}, fmt.Errorf("making a report directory: %w", err)
		}
		defer os.RemoveAll(dir)
	}
	report := filepath.Join(dir, "report.json")

	args := append([]string{
		"--label-filter=" + f.Filter(),
		"--json-report=" + report,
	}, g.suiteArgs(e)...)

	cmd := g.command(ctx, args)
	cmd.Stdout, cmd.Stderr = g.Stdout, g.Stderr
	runErr := cmd.Run()

	b, readErr := os.ReadFile(report)
	if readErr != nil {
		// No report means the suite did not get far enough to write one:
		// a compile failure, a missing binary, a cancelled context. That is
		// an error rather than a failure, because nothing was asserted.
		//
		// Both errors are kept: the read says the report is absent, and the
		// run says why. Neither alone identifies the cause.
		return Result{Outcome: policy.ResultError}, fmt.Errorf(
			"the suite wrote no report: %w", errors.Join(readErr, runErr),
		)
	}

	summary, err := parseSummary(b)
	if err != nil {
		return Result{Outcome: policy.ResultError, Report: b}, err
	}

	return Result{
		Summary: summary,
		Report:  b,
		Outcome: Outcome(summary, runErr == nil),
	}, nil
}

// Outcome classifies a run from its summary and whether whatever ran the suite
// exited clean.
//
// Shared with `summarise`, where the thing that ran the suite is a CI lane and
// `ok` is its conclusion rather than a subprocess exit. The distinction the
// three cases draw is the same either way, and it is the distinction the whole
// design rests on: a suite that ran and asserted false has told us something
// about the candidate, and a suite that broke has not.
func Outcome(summary policy.Summary, ok bool) policy.Result {
	switch {
	case ok:
		return policy.ResultPassed
	case summary.Failed > 0:
		// The suite ran and something asserted false. That is a qualification
		// result, and the only kind of failure that is one.
		return policy.ResultFailed
	default:
		// A non-zero exit with nothing failed is the suite itself breaking.
		return policy.ResultError
	}
}

// Fold sums every leg's report into one summary.
//
// A lane runs ginkgo more than once -- the NICo lane has up to three legs,
// because controller-managed control planes need a deployment-wide flag and so
// need their own run. Each writes its own report, and the stream's result is
// all of them: a stream is qualified when everything it selected passed, not
// when its last leg did.
//
// An error when no report parses, because a stream with no legs asserted
// nothing, and a zero summary would read as a clean run.
func Fold(reports [][]byte) (policy.Summary, error) {
	var total policy.Summary
	var errs []error
	folded := 0
	for i, b := range reports {
		s, err := parseSummary(b)
		if err != nil {
			errs = append(errs, fmt.Errorf("leg %d: %w", i, err))
			continue
		}
		total.Passed += s.Passed
		total.Failed += s.Failed
		total.Skipped += s.Skipped
		folded++
	}
	if folded == 0 {
		return policy.Summary{}, fmt.Errorf(
			"no leg reported any spec result: %w", errors.Join(errs...),
		)
	}
	return total, nil
}

// filter resolves the entry's provider to the lane it runs in.
//
// The Client resolves this rather than accepting it, because taking a filter
// from the caller would let a run select specs its backend cannot satisfy and
// then report the result as a qualification.
func (g Ginkgo) filter(e Entry) (lane.Lane, error) {
	l, err := lane.For(e.Provider())
	if err != nil {
		return lane.Lane{}, fmt.Errorf("stream %q: %w", e.Stream, err)
	}
	return l, nil
}

// suiteArgs is everything after the -- separator: the arguments the suite's
// own flags parse.
func (g Ginkgo) suiteArgs(e Entry) []string {
	args := []string{"."}
	if g.Environment != "" {
		args = append(args, "--", "-e2e.environment", g.Environment)
	} else {
		args = append(args, "--")
	}
	args = append(args, "-e2e.versions", e.versions())
	if c := e.capabilities(); c != "" {
		args = append(args, "-e2e.capabilities", c)
	}
	return args
}

func (g Ginkgo) command(ctx context.Context, args []string) *exec.Cmd {
	bin := g.Bin
	if bin == "" {
		bin = "ginkgo"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = g.Dir
	return cmd
}

// specNode is the LeafNodeType of an actual spec, as against suite setup.
const specNode = "It"

// ginkgoReport is the part of ginkgo's JSON report this package reads.
type ginkgoReport []struct {
	// SpecsThatWillRun is what the label filter selected. Every spec in the
	// suite appears in SpecReports whether selected or not, and an unselected
	// one is reported skipped, so counting skipped states conflates "the
	// filter excluded this" with "the run did not reach it".
	PreRunStats struct {
		TotalSpecs       int `json:"TotalSpecs"`
		SpecsThatWillRun int `json:"SpecsThatWillRun"`
	} `json:"PreRunStats"`

	SpecReports []struct {
		State string `json:"State"`

		// LeafNodeType separates specs from suite setup. BeforeSuite and
		// AfterSuite appear here as entries of their own and are reported
		// passed when they succeed, so counting them would credit a run that
		// passed no spec with two.
		//
		// Dropping them loses nothing: a failing BeforeSuite skips every
		// spec, which leaves a summary with nothing failed against a non-zero
		// exit -- and that is already classified an error rather than a pass.
		LeafNodeType string `json:"LeafNodeType"`

		// One slice per level of the container hierarchy, which is why this
		// is nested: labels on an Ordered Describe apply to every spec
		// beneath it, and a spec's requirement is the union of its own
		// labels and every level above it.
		ContainerLabels [][]string `json:"ContainerHierarchyLabels"`

		Labels []string `json:"Labels"`
	} `json:"SpecReports"`
}

// parseNeeds reads the needs: labels off the selected specs and returns the
// maximum per machine type, along with how many specs were selected.
//
// The maximum rather than the sum, because an Ordered container shares one
// site controller across every spec beneath it. Summing would ask for a
// site's worth of hardware per assertion.
func parseNeeds(b []byte) (map[string]int, int, error) {
	var report ginkgoReport
	if err := json.Unmarshal(b, &report); err != nil {
		return nil, 0, fmt.Errorf("parsing the ginkgo report: %w", err)
	}

	nodes := map[string]int{}
	selected := 0
	for _, suite := range report {
		selected += suite.PreRunStats.SpecsThatWillRun
		for _, spec := range suite.SpecReports {
			if spec.LeafNodeType != specNode || spec.State == "skipped" {
				continue
			}
			labels := slices.Concat(spec.Labels, flatten(spec.ContainerLabels))
			for _, l := range labels {
				kind, count, ok := parseNeed(l)
				if !ok {
					continue
				}
				if count > nodes[kind] {
					nodes[kind] = count
				}
			}
		}
	}
	if len(nodes) == 0 {
		nodes = nil
	}
	return nodes, selected, nil
}

// flatten collapses the container hierarchy into one label list.
func flatten(levels [][]string) []string {
	var out []string
	for _, level := range levels {
		for _, l := range level {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
	}
	return out
}

// parseNeed reads one needs:cpu=3 label.
func parseNeed(label string) (kind string, count int, ok bool) {
	rest, found := strings.CutPrefix(label, needsPrefix)
	if !found {
		return "", 0, false
	}
	kind, value, found := strings.Cut(rest, "=")
	if !found || kind == "" {
		return "", 0, false
	}
	count, err := strconv.Atoi(value)
	if err != nil || count < 0 {
		return "", 0, false
	}
	return kind, count, true
}

// parseSummary counts the spec states in a report.
func parseSummary(b []byte) (policy.Summary, error) {
	var report ginkgoReport
	if err := json.Unmarshal(b, &report); err != nil {
		return policy.Summary{}, fmt.Errorf("parsing the ginkgo report: %w", err)
	}
	var s policy.Summary
	selected, specs := 0, 0
	for _, suite := range report {
		selected += suite.PreRunStats.SpecsThatWillRun
		for _, spec := range suite.SpecReports {
			if spec.LeafNodeType != specNode {
				continue
			}
			specs++
			switch spec.State {
			case "passed":
				s.Passed++
			case "failed", "panicked", "interrupted", "aborted":
				s.Failed++
			}
		}
	}
	if specs == 0 {
		return policy.Summary{}, errors.New("the report holds no spec results")
	}

	// What the filter selected and the run did not settle: specs an Ordered
	// container cut short after an earlier failure, and specs a Skip() left
	// alone. Deriving it this way keeps the three counts summing to the size
	// of the run rather than the size of the suite.
	s.Skipped = max(selected-s.Passed-s.Failed, 0)
	return s, nil
}
