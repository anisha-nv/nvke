// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dsx-ai-factory/nke/release/internal/aggregate"
	"github.com/dsx-ai-factory/nke/release/internal/policy"
	"github.com/dsx-ai-factory/nke/release/internal/record"
)

func aggregateCmd() *cobra.Command {
	var (
		source     string
		state      string
		root       string
		policyAt   string
		output     string
		components []string
		dryRun     bool
	)

	cmd := &cobra.Command{
		Use:   "aggregate",
		Short: "Rebuild one artifact from every result and decide what is qualified",
		Long: `Rebuild one artifact from every result and decide what is qualified.

Nothing before this has said whether a candidate is qualified: each stream
published its own outcome, and the judgement is over all of them.

It runs after the matrix whatever its outcome, and needs nothing from the run
that triggered it. Results are discovered from a tag listing rather than
passed in, so a cancelled aggregation is harmless and the next one folds in
whatever it would have.

--state is read and written, which is the only place in the design that is
true. A missing or unreadable state makes the diff the whole listing, so the
same path rebuilds it from nothing.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if root == "" {
				var err error
				if root, err = repoRoot(); err != nil {
					return err
				}
			}
			if policyAt == "" {
				policyAt = filepath.Join(root, "release", "qualification.yaml")
			}
			p, err := policy.LoadPolicy(policyAt)
			if err != nil {
				return err
			}

			store := record.OCI{Credential: record.NGCCredential(registryHost(source))}
			previous := read(cmd, store, state)

			a := &aggregate.Aggregator{
				Store:      store,
				Source:     source,
				Policy:     p,
				Components: components,
				Log:        cmd.ErrOrStderr(),
			}
			next, err := a.Run(cmd.Context(), previous)
			if err != nil {
				return err
			}

			files, err := aggregate.Encode(next)
			if err != nil {
				return err
			}
			report(cmd, next)

			if dryRun || state == "" {
				return writeBytes(cmd, output, files[record.StateFile])
			}
			if err := store.Push(cmd.Context(), state, files); err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "qualify: wrote", state)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&source, "source", "", "registry base holding one repository per component (required)")
	f.StringVar(&state, "state", "", "the central artifact, read and written")
	f.StringVar(&root, "root", "", "repository root (default: the enclosing git checkout)")
	f.StringVar(&policyAt, "policy", "", "release policy file (default: <root>/release/qualification.yaml)")
	f.StringVar(&output, "output", "-", "where to write the state when not pushing; - is stdout")
	f.StringSliceVar(&components, "component", nil, "narrow to these components (default: every one the policy declares)")
	f.BoolVar(&dryRun, "dry-run", false, "fold and report without writing --state")

	_ = cmd.MarkFlagRequired("source")
	return cmd
}

// read pulls the previous state, treating every failure as absence.
//
// A state that cannot be read is indistinguishable from one that was never
// written, because it is a derived cache either way. Failing here would turn
// a corrupt cache into an outage, when the results it was derived from are
// all still there.
func read(cmd *cobra.Command, store record.Store, ref string) *aggregate.State {
	if ref == "" {
		return nil
	}
	files, err := store.Pull(cmd.Context(), ref)
	if err != nil {
		if !errors.Is(err, record.ErrNotFound) {
			fmt.Fprintf(cmd.ErrOrStderr(), "qualify: rebuilding the state: %v\n", err)
		}
		return nil
	}
	state, err := aggregate.Decode(files)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "qualify: rebuilding the state: %v\n", err)
		return nil
	}
	return state
}

// report prints what each candidate is qualified at, which is the one line a
// person reads out of a nightly.
func report(cmd *cobra.Command, state *aggregate.State) {
	for _, rec := range state.SortedRecords() {
		scopes := "nothing"
		if rec.Status != nil && len(rec.Status.QualifiedScopes) > 0 {
			scopes = fmt.Sprint(rec.Status.QualifiedScopes)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "qualify: %s qualified at %s\n", rec.Metadata.Name, scopes)
	}
}
