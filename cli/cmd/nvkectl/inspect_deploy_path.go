// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dsx-ai-factory/nvke/cli/internal/deploypath"
)

// Field separators of the --item record. US between fields, RS between the
// entries of a list field: neither appears in a path or a key, and the shell
// splits on them with IFS.
const (
	fieldSep = "\x1f"
	entrySep = "\x1e"
)

type inspectDeployPathOptions struct {
	item string
}

func newInspectDeployPathCmd() *cobra.Command {
	opts := &inspectDeployPathOptions{}
	cmd := &cobra.Command{
		Use:   "deploy-path DIR [--item NAME]",
		Short: "Print a deploy path's item list, or one helm item's release.yaml, for the shell",
		Long: `Read a deploy path and print one view of it. deploy/nvke-deploy.sh reads the
deploy path through this command, so the YAML is parsed once, in Go.

Without --item: one line per item of nvke-deploy.yaml, in order, as
name and type separated by a tab.

With --item NAME: the helm item's release.yaml as one record, its fields
separated by the unit separator (0x1f), in this order: chart name, chart
version, repository, path, release name, release namespace, values files,
requires, setFile. The list fields are joined with the record separator
(0x1e). A missing or malformed file is an error.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInspectDeployPath(cmd, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.item, "item", "", "Print this helm item's release.yaml as one record")
	return cmd
}

func runInspectDeployPath(cmd *cobra.Command, dir string, opts *inspectDeployPathOptions) error {
	out := cmd.OutOrStdout()
	if opts.item == "" {
		items, err := deploypath.Items(dir)
		if err != nil {
			return err
		}
		for _, item := range items {
			fmt.Fprintf(out, "%s\t%s\n", item.Name, item.Type)
		}
		return nil
	}
	items, err := deploypath.Items(dir)
	if err != nil {
		return err
	}
	var found *deploypath.Item
	for i := range items {
		if items[i].Name == opts.item {
			found = &items[i]
		}
	}
	if found == nil {
		return fmt.Errorf("%s: no item %q in nvke-deploy.yaml", dir, opts.item)
	}
	if found.Type != deploypath.TypeHelm {
		return fmt.Errorf("%s: item %q is a %s item, not helm", dir, opts.item, found.Type)
	}
	rel, err := deploypath.ReadRelease(dir + "/" + opts.item)
	if err != nil {
		return err
	}
	if rel.Chart == nil || rel.Release == nil {
		return errors.New("release.yaml has no chart or release section")
	}
	var requires []string
	for _, q := range rel.Requires {
		requires = append(requires, strings.Join([]string{q.Kind, q.Name, q.Namespace, q.Condition}, ":"))
	}
	fields := []string{
		rel.Chart.Name, rel.Chart.Version, rel.Chart.Repository, rel.Chart.Path,
		rel.Release.Name, rel.Release.Namespace,
		strings.Join(rel.Values, entrySep), strings.Join(requires, entrySep), strings.Join(rel.Chart.SetFile, entrySep),
	}
	fmt.Fprintln(out, strings.Join(fields, fieldSep))
	return nil
}
