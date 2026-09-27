// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/dsx-ai-factory/nke/release/internal/plan"
	"github.com/dsx-ai-factory/nke/release/internal/policy"
	"github.com/dsx-ai-factory/nke/release/internal/record"
)

func publishCmd() *cobra.Command {
	var (
		planPath    string
		combination string
		stream      string
		artifacts   string
		push        string
		runURL      string
		root        string
		output      string
	)

	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Record one stream's outcome",
		Long: `Record one stream's outcome.

This runs inside the job that produced the outcome, not after the matrix, so
a stream that finished has a durable result before the run is over and a
stream that failed has published before anything else notices.

It writes one artifact carrying that stream's outcome and its report. Nothing
is aggregated, nothing is judged, and no stream waits on another. In
particular the record carries no "qualified" field, because one stream cannot
judge a set.

Without --push the record is written to --output instead, which is how the
artifact can be inspected before a registry is involved.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := readPlan(planPath)
			if err != nil {
				return err
			}
			if root == "" {
				if root, err = repoRoot(); err != nil {
					return err
				}
			}

			result, err := buildResult(p, root, combination, stream, runURL, artifacts)
			if err != nil {
				return err
			}
			_, report, err := record.ReadHandoff(record.EntryDir(artifacts, combination, stream))
			if err != nil {
				return err
			}

			if push == "" {
				b, err := record.Encode(result)
				if err != nil {
					return err
				}
				return writeBytes(cmd, output, b)
			}

			key := record.Key{
				Component:   p.Component,
				Version:     p.Version,
				Combination: combination,
				Stream:      stream,
			}
			b, err := record.Encode(result)
			if err != nil {
				return err
			}
			files := record.Files{record.ResultFile: b}
			if len(report) > 0 {
				files[record.ReportFile] = report
			}

			ref := key.Reference(push)
			store := record.OCI{Credential: record.NGCCredential(registryHost(push))}
			if err := store.Push(cmd.Context(), ref, files); err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "qualify: published", ref)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&planPath, "plan", "", "the plan the run came from (required)")
	f.StringVar(&combination, "combination", "", "which combination ran (required)")
	f.StringVar(&stream, "stream", "", "which stream ran (required)")
	f.StringVar(&artifacts, "artifacts", "artifacts", "where run left the outcome and report")
	f.StringVar(&push, "push", "", "registry base to push to; empty writes to --output instead")
	f.StringVar(&runURL, "run-url", "", "where the logs live, for as long as CI keeps them")
	f.StringVar(&root, "root", "", "repository root (default: the enclosing git checkout)")
	f.StringVar(&output, "output", "-", "where to write the record when not pushing; - is stdout")

	for _, name := range []string{"plan", "combination", "stream"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

// buildResult assembles the record: the component's own declaration with the
// version set and this one stream's outcome in the status.
//
// The declaration is read from the component's spec rather than copied out of
// the plan, because the plan holds resolved dependencies and spec.dependencies
// is meant to carry the selectors as declared. A reader then sees what was
// asked for beside what was tested.
func buildResult(
	p *plan.Plan,
	root, combination, stream, runURL, artifacts string,
) (*policy.Component, error) {
	declared, err := policy.LoadComponent(policy.ComponentPath(root, p.Component))
	if err != nil {
		return nil, err
	}

	i := slices.IndexFunc(p.Combinations, func(c plan.Combination) bool { return c.ID == combination })
	if i < 0 {
		return nil, fmt.Errorf("the plan has no combination %q", combination)
	}
	c := p.Combinations[i]
	if !slices.ContainsFunc(c.Streams, func(s plan.Stream) bool { return s.Name == stream }) {
		return nil, fmt.Errorf("combination %q has no stream %q", combination, stream)
	}

	h, _, err := record.ReadHandoff(record.EntryDir(artifacts, combination, stream))
	if err != nil {
		return nil, err
	}

	deps := make([]policy.ResolvedDependency, 0, len(c.Dependencies))
	for _, d := range c.Dependencies {
		deps = append(deps, policy.ResolvedDependency{Name: d.Name, Version: d.Version})
	}

	spec := declared.Spec
	spec.Version = p.Version
	return record.New(spec, policy.StatusCombination{
		ID:           c.ID,
		Dependencies: deps,
		Streams: []policy.StreamResult{{
			Name:     stream,
			Scope:    p.Scope,
			Result:   h.Result,
			Summary:  h.Summary,
			RunURL:   runURL,
			Teardown: h.Teardown,
		}},
	}), nil
}

// registryHost is the host part of a registry base, which is what a
// credential is scoped to.
func registryHost(base string) string {
	host, _, _ := splitHost(base)
	return host
}

func splitHost(base string) (host, path string, ok bool) {
	for i := 0; i < len(base); i++ {
		if base[i] == '/' {
			return base[:i], base[i+1:], true
		}
	}
	return base, "", false
}

// writeBytes emits to stdout or a file, making the parent directory so that
// --output into a fresh artifacts path does not need a separate mkdir.
func writeBytes(cmd *cobra.Command, path string, b []byte) error {
	if path == "-" {
		_, err := cmd.OutOrStdout().Write(b)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("making %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
