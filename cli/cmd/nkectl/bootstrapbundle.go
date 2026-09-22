// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dsx-ai-factory/nke/cli/internal/bootstrap/bundle"
)

// newTemplateCmd is the "template" command group: files the operator starts
// from and edits.
func newTemplateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Write a file to start from",
	}
	cmd.AddCommand(newTemplateBootstrapBundleCmd())
	return cmd
}

func newTemplateBootstrapBundleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "bootstrap-bundle",
		Short: "Write a bootstrap bundle to fill in",
		Long: `Write a bootstrap bundle template to standard output.

The bootstrap bundle carries your decisions for "nkectl bootstrap site": the
registry it pulls from, the two images you build, and any override. nkectl
pins what the artifacts are. "nkectl get bootstrap-manifest" prints that list.

Fill in three values: registryMirror.endpoint, images.controller and
images.provisionedProvider. With a registry declared, every artifact is pulled
from it, the two images included, so push them there first; "nkectl get
bootstrap-manifest" lists what else the registry must hold. Check the result
with "nkectl validate bootstrap-bundle -f <file>". Point
sitespec.bootstrapBundleFile at it.

For development, delete the registryMirror and images blocks. nkectl then
pulls every artifact from its public registry and side-loads the two images
from your image store. Pass --public to "bootstrap site" to confirm that.

A complete, filled-in example is in examples/bootstrap-bundle.yaml. See
docs/bundle.md.`,
		Example: `  nkectl template bootstrap-bundle > bootstrap-bundle.yaml
  # edit the three values, then
  nkectl validate bootstrap-bundle -f bootstrap-bundle.yaml`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := cmd.OutOrStdout().Write(bundle.TemplateBytes())
			return err
		},
	}
}

// newValidateCmd is the "validate" command group: check a file before acting
// on it.
func newValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check a file before acting on it",
	}
	cmd.AddCommand(newValidateBootstrapBundleCmd())
	return cmd
}

func newValidateBootstrapBundleCmd() *cobra.Command {
	var path string

	cmd := &cobra.Command{
		Use:   "bootstrap-bundle",
		Short: "Check a bootstrap bundle and show where every artifact would come from",
		Long: `Check a bootstrap bundle against the manifest this nkectl was built with.

It runs the checks "bootstrap site" runs, and reports every problem together,
each naming its field. On success it prints where every artifact would be
pulled from, as "bootstrap site --dry-run" does. It needs no site spec and
reaches no registry.`,
		Example:      `  nkectl validate bootstrap-bundle -f bootstrap-bundle.yaml`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			b, err := bundle.Load(path)
			if err != nil {
				return err
			}
			manifest := bundle.LoadManifest()
			if err := b.Validate(manifest); err != nil {
				return err
			}
			if err := b.ValidateReferences(); err != nil {
				return err
			}
			closure, err := bundle.Resolve(b, manifest)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprint(out, closure.DryRunTable(false))
			_, err = fmt.Fprintf(out, "\nbootstrap bundle %s is valid.\n", path)
			return err
		},
	}

	cmd.Flags().StringVarP(&path, "file", "f", "", "Path to the bootstrap bundle (required)")
	_ = cmd.MarkFlagRequired("file")

	return cmd
}
