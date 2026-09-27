// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dsx-ai-factory/nke/release/internal/plan"
	"github.com/dsx-ai-factory/nke/release/internal/policy"
)

func planCmd() *cobra.Command {
	var (
		component string
		version   string
		scope     string
		output    string
		matrix    string
		root      string
		policyAt  string
	)

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Resolve what to qualify for one component",
		Long: `Resolve what to qualify for one component.

Reads the component's qualification spec and the release-wide policy,
resolves each dependency's selectors, and expands the scope's selector over
the stream list. The result is one file naming every combination and every
stream, including the ones an earlier run already settled.

An absent --version means the component's latest release candidate.`,
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

			planner := &plan.Planner{
				Policy:  p,
				Root:    root,
				Tags:    plan.GitTags{Dir: root},
				History: plan.NoHistory{},
			}

			result, err := planner.Plan(component, version, scope)
			if err != nil {
				return err
			}
			if err := write(cmd, output, result); err != nil {
				return err
			}
			if matrix == "" {
				return nil
			}
			return writeMatrix(matrix, result)
		},
	}

	f := cmd.Flags()
	f.StringVar(&component, "component", "", "component to qualify (required)")
	f.StringVar(&version, "version", "", "candidate to qualify; empty means the latest release candidate")
	// full is the default because it is the scope that is never wrong. A
	// narrower default would silently cover less of the matrix than the
	// person running it believed.
	f.StringVar(&scope, "scope", "full", "how much of the matrix to cover")
	f.StringVar(&output, "output", "-", "where to write the plan; - is stdout")
	f.StringVar(&matrix, "matrix", "", "also write the runnable cells here, as a flat list for a job matrix")
	f.StringVar(&root, "root", "", "repository root (default: the enclosing git checkout)")
	f.StringVar(&policyAt, "policy", "", "release policy file (default: <root>/release/qualification.yaml)")

	_ = cmd.MarkFlagRequired("component")
	return cmd
}

// write emits the plan as indented JSON, to stdout or to a file.
func write(cmd *cobra.Command, path string, result *plan.Plan) error {
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the plan: %w", err)
	}
	b = append(b, '\n')

	if path == "-" {
		_, err = cmd.OutOrStdout().Write(b)
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// writeMatrix emits the runnable cells as a compact one-line array, which is
// the shape `fromJSON` wants and the shape a GITHUB_OUTPUT line admits.
//
// An empty matrix is written as [] rather than null, because `fromJSON(null)`
// is a workflow error while a matrix of [] is a job that is skipped. A plan
// that covers nothing should leave the run page empty, not red.
func writeMatrix(path string, result *plan.Plan) error {
	cells := result.Matrix()
	if cells == nil {
		cells = []plan.Cell{}
	}
	b, err := json.Marshal(cells)
	if err != nil {
		return fmt.Errorf("encoding the matrix: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// repoRoot finds the checkout the command was run from, so that neither
// --root nor --policy has to be passed by hand.
func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("finding the repository root: %w (pass --root)", err)
	}
	return strings.TrimSpace(string(out)), nil
}
