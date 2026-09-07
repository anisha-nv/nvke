// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NVIDIA/nke/cli/internal/clusterspec"
)

type generateClusterSpecOptions struct {
	provider       string
	sets           []string
	nonInteractive bool
}

func newGenerateClusterSpecCmd() *cobra.Command {
	opts := &generateClusterSpecOptions{}
	cmd := &cobra.Command{
		Use:          "clusterspec <cluster-name>",
		Short:        "Generate a tenant cluster create spec",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGenerateClusterSpec(cmd, args[0], opts)
		},
	}
	defaultHelp := cmd.HelpFunc()
	cmd.SetHelpFunc(func(command *cobra.Command, args []string) {
		command.Long = generateClusterSpecHelp(opts.provider)
		command.Example = generateClusterSpecExample(opts.provider)
		defaultHelp(command, args)
	})
	flags := cmd.Flags()
	flags.StringVar(&opts.provider, "provider", "", "Cluster infrastructure provider: nico or capd (required)")
	_ = cmd.MarkFlagRequired("provider")
	flags.StringArrayVar(&opts.sets, "set", nil, "Set a provider-template field as path=value. Repeatable.")
	flags.BoolVar(&opts.nonInteractive, "non-interactive", false, "Reject missing values instead of prompting")
	return cmd
}

func generateClusterSpecHelp(provider string) string {
	common := `Write a CreateClusterRequest YAML document to standard output.

Pass it unchanged to "nkectl create cluster -f". Use --set path=value for
template fields; paths expand into the API request, including both generated
node pools where applicable.`
	if provider != clusterspec.ProviderNico && provider != clusterspec.ProviderCAPD {
		return common + "\n\nUse --provider nico --help or --provider capd --help to list only that provider's fields."
	}
	var lines []string
	for _, field := range clusterspec.Fields(provider) {
		requirement := "optional"
		if field.Required {
			requirement = "required"
		}
		description := strings.ReplaceAll(field.Description, "\n", " ")
		lines = append(lines, fmt.Sprintf("  %s (%s): %s", field.Path, requirement, description))
	}
	if provider == clusterspec.ProviderNico {
		lines = append(lines, "  nico.vpcPrefixID or nico.subnetID (exactly one): NICo network ID for all generated node pools.")
		return common + "\n\nNICo fields\n" + strings.Join(lines, "\n") + `

When attached to a terminal, missing NICo fields are prompted on standard error.
YAML remains on standard output, so "> cluster.yaml" is safe.`
	}
	return common + "\n\nCAPD fields\n" + strings.Join(lines, "\n")
}

func generateClusterSpecExample(provider string) string {
	if provider == clusterspec.ProviderNico {
		return `  nkectl generate clusterspec demo --provider nico \
    --set nico.orgID=example-org \
    --set nico.siteID=11111111-1111-4111-8111-111111111111 \
    --set nico.vpcID=22222222-2222-4222-8222-222222222222 \
    --set nico.vpcPrefixID=33333333-3333-4333-8333-333333333333 \
    --set nico.instanceTypeID=44444444-4444-4444-8444-444444444444 \
    --set nico.sshKeyGroupID=55555555-5555-4555-8555-555555555555 \
    --set nico.osProfile=example-nke-os \
    --set nodePools.controlPlane.nico.configProfile=example-control-plane \
    --set nodePools.worker.nico.configProfile=example-worker > cluster.yaml`
	}
	return `  nkectl generate clusterspec demo --provider capd > cluster.yaml
  nkectl create cluster -f cluster.yaml`
}

func runGenerateClusterSpec(cmd *cobra.Command, clusterName string, opts *generateClusterSpecOptions) error {
	in := clusterspec.DefaultInput(opts.provider, clusterName)
	if err := clusterspec.ApplySets(&in, opts.sets); err != nil {
		return err
	}
	if opts.provider == clusterspec.ProviderNico && !opts.nonInteractive && canPrompt(cmd) {
		if err := promptMissingFields(cmd, &in); err != nil {
			return err
		}
	}
	if missing := clusterspec.MissingRequiredFields(in); len(missing) > 0 {
		paths := make([]string, 0, len(missing))
		for _, field := range missing {
			paths = append(paths, field.Path)
		}
		return fmt.Errorf("missing required fields for provider %s: %s", opts.provider, strings.Join(paths, ", "))
	}
	request, err := clusterspec.Build(in)
	if err != nil {
		return err
	}
	rendered, err := clusterspec.Render(request)
	if err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write(rendered)
	return err
}

func canPrompt(cmd *cobra.Command) bool {
	if cmd.InOrStdin() != os.Stdin {
		return true
	}
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func promptMissingFields(cmd *cobra.Command, in *clusterspec.Input) error {
	reader := bufio.NewReader(cmd.InOrStdin())
	for _, field := range clusterspec.MissingRequiredFields(*in) {
		if err := promptSet(cmd.ErrOrStderr(), reader, in, field); err != nil {
			return err
		}
	}
	if in.Nico.VpcPrefixID == "" && in.Nico.SubnetID == "" {
		field := clusterspec.Field{Path: "nico.subnetID", Description: "NICo subnet ID"}
		if err := promptSet(cmd.ErrOrStderr(), reader, in, field); err != nil {
			return err
		}
	}
	return nil
}

func promptSet(out io.Writer, reader *bufio.Reader, in *clusterspec.Input, field clusterspec.Field) error {
	fmt.Fprintf(out, "%s (%s): ", field.Description, field.Path)
	value, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("read %s: %w", field.Path, err)
	}
	if err := clusterspec.ApplySets(in, []string{field.Path + "=" + strings.TrimSpace(value)}); err != nil {
		return err
	}
	if err == io.EOF && strings.TrimSpace(value) == "" {
		return fmt.Errorf("read %s: standard input ended before a value was supplied", field.Path)
	}
	return nil
}
