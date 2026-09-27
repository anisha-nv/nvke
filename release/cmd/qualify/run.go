// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/dsx-ai-factory/nke/release/internal/capacity"
	"github.com/dsx-ai-factory/nke/release/internal/plan"
	"github.com/dsx-ai-factory/nke/release/internal/policy"
	"github.com/dsx-ai-factory/nke/release/internal/record"
	"github.com/dsx-ai-factory/nke/release/internal/suite"
)

// releaseBudget bounds how long a release may take after the run is over.
// Long enough for a retry or two against a slow API, short enough that a job
// does not sit past its timeout holding nothing.
const releaseBudget = 2 * time.Minute

func runCmd() *cobra.Command {
	var (
		planPath    string
		combination string
		stream      string
		artifacts   string
		suiteDir    string
		environment string
		ginkgoBin   string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Qualify one entry of a plan",
		Long: `Qualify one entry of a plan.

One invocation covers one combination and one stream, and does nothing else.
The versions are looked up in the plan rather than resolved, so a tag landing
between the plan and this run cannot change what it tests.

The sequence is fixed: ask the suite what the selection needs, acquire that
capacity or stop, then run the same selection for real. The requirement comes
first because the capacity provider cannot know how many nodes a stream takes
without asking the suite.

An outcome is always written to --artifacts, including when capacity was
unavailable, so publish has something to record either way.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := readPlan(planPath)
			if err != nil {
				return err
			}
			entry, err := lookup(p, combination, stream)
			if err != nil {
				return err
			}

			dir := record.EntryDir(artifacts, combination, stream)
			client := suite.Ginkgo{
				Dir:         suiteDir,
				Environment: environment,
				Bin:         ginkgoBin,
				ReportDir:   dir,
				Stdout:      os.Stdout,
				Stderr:      os.Stderr,
			}

			h, report, err := execute(cmd, client, capacity.Default(), entry)

			// The outcome is written before the error is returned, so a
			// failed or unschedulable run is still publishable. Losing the
			// record of a failure is worse than the failure.
			if writeErr := record.WriteHandoff(dir, h, report); writeErr != nil {
				return errors.Join(err, writeErr)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "qualify: %s/%s %s\n", combination, stream, h.Result)
			return err
		},
	}

	f := cmd.Flags()
	f.StringVar(&planPath, "plan", "", "the plan to read (required)")
	f.StringVar(&combination, "combination", "", "which combination to qualify (required)")
	f.StringVar(&stream, "stream", "", "which stream to qualify it against (required)")
	f.StringVar(&artifacts, "artifacts", "artifacts", "where to write this entry's outcome and report")
	f.StringVar(&suiteDir, "suite", "controller/test/e2e", "the e2e suite's package directory")
	f.StringVar(&environment, "environment", "", "the e2e environment name to run against")
	f.StringVar(&ginkgoBin, "ginkgo", "", "the ginkgo binary (default: the one on PATH)")

	for _, name := range []string{"plan", "combination", "stream"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

// execute is the fixed sequence, returning what to record rather than only an
// error, because every exit path here produces a publishable outcome.
func execute(
	cmd *cobra.Command,
	client suite.Client,
	providers capacity.Providers,
	entry suite.Entry,
) (record.Handoff, []byte, error) {
	ctx := cmd.Context()

	need, err := client.Requirement(ctx, entry)
	if err != nil {
		// The suite could not even be asked what it needs. Nothing was
		// asserted, so this is an error rather than a failure.
		return record.Handoff{Result: policy.ResultError}, nil, err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "qualify: %s needs %s\n", entry.Stream, need)

	provider, err := providers.For(entry.Provider())
	if err != nil {
		return record.Handoff{Result: policy.ResultError}, nil, err
	}

	lease, err := provider.Acquire(ctx, need)
	if err != nil {
		if errors.Is(err, capacity.ErrUnschedulable) {
			// Not a statement about the software. The Planner will offer this
			// cell again, because unschedulable does not settle the question.
			return record.Handoff{Result: policy.ResultUnschedulable}, nil, nil
		}
		return record.Handoff{Result: policy.ResultError}, nil, err
	}

	// Deferred here rather than written as a final step, so it runs on
	// success, on a failed assertion, on a panic and on cancellation.
	defer func() {
		// Released against a fresh context with its own budget: cmd's may
		// already be cancelled, and a lease has to come back even then.
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseBudget)
		defer cancel()
		if err := lease.Release(release); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "qualify: releasing capacity: %v\n", err)
		}
	}()

	result, err := client.Execute(ctx, entry, lease)
	h := record.Handoff{Result: result.Outcome}
	if result.Outcome != policy.ResultUnschedulable {
		summary := result.Summary
		h.Summary = &summary
	}
	if result.Outcome == policy.ResultPassed || result.Outcome == policy.ResultFailed {
		// The suite ran and reported. Whatever err says, the outcome is the
		// qualification result, so it is not also an error to the caller.
		return h, result.Report, nil
	}
	return h, result.Report, err
}

// lookup finds one entry in a plan, and refuses a cell the Planner marked.
func lookup(p *plan.Plan, combination, stream string) (suite.Entry, error) {
	i := slices.IndexFunc(p.Combinations, func(c plan.Combination) bool { return c.ID == combination })
	if i < 0 {
		ids := make([]string, 0, len(p.Combinations))
		for _, c := range p.Combinations {
			ids = append(ids, c.ID)
		}
		return suite.Entry{}, fmt.Errorf("the plan has no combination %q; it has %v", combination, ids)
	}
	c := p.Combinations[i]

	j := slices.IndexFunc(c.Streams, func(s plan.Stream) bool { return s.Name == stream })
	if j < 0 {
		return suite.Entry{}, fmt.Errorf("combination %q has no stream %q", combination, stream)
	}
	s := c.Streams[j]
	if s.Skip != policy.ResultNone {
		// The flattening filter should never hand this over. Refusing it here
		// means a hand-run command cannot accidentally re-qualify a cell the
		// plan settled.
		return suite.Entry{}, fmt.Errorf("stream %q is marked %q in the plan, so it is not for running", stream, s.Skip)
	}

	deps := make([]policy.ResolvedDependency, 0, len(c.Dependencies))
	for _, d := range c.Dependencies {
		deps = append(deps, policy.ResolvedDependency{Name: d.Name, Version: d.Version})
	}
	return suite.Entry{
		Component:    p.Component,
		Version:      p.Version,
		Dependencies: deps,
		Stream:       s.Name,
		Capabilities: s.Capabilities,
	}, nil
}

// readPlan reads a plan written by `qualify plan`.
func readPlan(path string) (*plan.Plan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the plan: %w", err)
	}
	var p plan.Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if p.Component == "" || p.Version == "" {
		return nil, fmt.Errorf("%s names no component or version, so it is not a plan", path)
	}
	return &p, nil
}
