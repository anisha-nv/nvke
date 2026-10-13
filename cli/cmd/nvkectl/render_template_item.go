// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dsx-ai-factory/nvke/cli/internal/deploypath"
)

type renderTemplateItemOptions struct {
	config string
	item   string
	out    string
}

// newRenderTemplateItemCmd renders one template item against a BootstrapConfig
// JSON document. deploy/nvke-deploy.sh calls it for a deploy path that was
// not rendered for a site, as the e2e lanes install theirs.
func newRenderTemplateItemCmd() *cobra.Command {
	opts := &renderTemplateItemOptions{}
	cmd := &cobra.Command{
		Use:   "template-item --config FILE --item DIR --out DIR",
		Short: "Render a template item's .yaml.tmpl files against a BootstrapConfig JSON document",
		Long: `Render every .yaml.tmpl file of a template item against the BootstrapConfig
JSON document --config names, into --out, and copy the plain .yaml files
beside them. The field names are those of
docs/site-bootstrap/deploy-repository.md. A template naming a field the
document lacks, an empty value in the document, or a template that renders
to nothing is refused.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := deploypath.LoadTemplateConfig(opts.config)
			if err != nil {
				return err
			}
			n, err := deploypath.RenderTemplateDir(opts.item, opts.out, cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "rendered %d templates of %s into %s\n", n, opts.item, opts.out)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.config, "config", "", "BootstrapConfig, as JSON (required)")
	cmd.Flags().StringVar(&opts.item, "item", "", "The template item directory (required)")
	cmd.Flags().StringVar(&opts.out, "out", "", "Directory to write (required)")
	for _, flag := range []string{"config", "item", "out"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	return cmd
}
