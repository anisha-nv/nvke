// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dsx-ai-factory/nke/release/internal/aggregate"
	"github.com/dsx-ai-factory/nke/release/internal/dashboard"
	"github.com/dsx-ai-factory/nke/release/internal/policy"
	"github.com/dsx-ai-factory/nke/release/internal/record"
)

func generateCmd() *cobra.Command {
	var (
		source   string
		out      string
		root     string
		policyAt string
	)

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Render the dashboard from the central artifact",
		Long: `Render the dashboard from the central artifact.

For each component the page shows the most recent qualification, the scopes
it covers, which streams passed, and a link into the run behind each. A
stream that is skipped or absent renders as not-run rather than quietly
counting as a pass.

It is read-only, so it needs no concurrency group and can run whenever,
including outside the qualification workflow entirely.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			state, err := load(cmd, source)
			if err != nil {
				return err
			}

			if root == "" {
				if root, err = repoRoot(); err != nil {
					return err
				}
			}
			if policyAt == "" {
				policyAt = filepath.Join(root, "release", "qualification.yaml")
			}
			// The policy is what names the scopes. A page rendered without it
			// still shows every result, which is why this is not fatal.
			p, err := policy.LoadPolicy(policyAt)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "qualify: rendering without the policy: %v\n", err)
				p = nil
			}

			var page bytes.Buffer
			if err := dashboard.Render(&page, state, p); err != nil {
				return err
			}

			if out == "-" {
				_, err := cmd.OutOrStdout().Write(page.Bytes())
				return err
			}
			if err := os.MkdirAll(out, 0o755); err != nil {
				return fmt.Errorf("making %s: %w", out, err)
			}
			path := filepath.Join(out, "index.html")
			if err := os.WriteFile(path, page.Bytes(), 0o644); err != nil {
				return fmt.Errorf("writing %s: %w", path, err)
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "qualify: wrote", path)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&source, "source", "", "the central artifact to render; a file path or an OCI reference (required)")
	f.StringVar(&out, "out", "site", "directory to write index.html into; - is stdout")
	f.StringVar(&root, "root", "", "repository root (default: the enclosing git checkout)")
	f.StringVar(&policyAt, "policy", "", "release policy file (default: <root>/release/qualification.yaml)")

	_ = cmd.MarkFlagRequired("source")
	return cmd
}

// load reads the state from a file when one exists at that path, and from the
// registry otherwise.
//
// Accepting both is what lets the page be rendered from a state produced by
// `aggregate --dry-run` without a registry in the loop, which is how the
// template is worked on.
func load(cmd *cobra.Command, source string) (*aggregate.State, error) {
	if b, err := os.ReadFile(source); err == nil {
		return aggregate.Decode(record.Files{record.StateFile: b})
	}
	store := record.OCI{Credential: record.NGCCredential(registryHost(source))}
	files, err := store.Pull(cmd.Context(), source)
	if err != nil {
		return nil, err
	}
	return aggregate.Decode(files)
}
