// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
	"github.com/NVIDIA/nke/cli/internal/sitespec"
)

// newGenerateCmd is the "generate" command group.
func newGenerateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate NKE configuration files",
	}
	cmd.AddCommand(newGenerateSiteSpecCmd())
	cmd.AddCommand(newGenerateClusterSpecCmd())
	return cmd
}

type generateClusterSpecOptions struct {
	provider                  string
	kubernetesVersion         string
	cniPlugin                 string
	cniVersion                string
	etcdEncrypted             bool
	controlPlaneCount         int
	workerCount               int
	controlPlaneVIP           string
	systemReservedMemory      string
	orgID                     string
	siteID                    string
	vpcID                     string
	vpcPrefixID               string
	subnetID                  string
	instanceTypeID            string
	sshKeyGroupID             string
	osProfile                 string
	controlPlaneConfigProfile string
	workerConfigProfile       string
	capdMachineImage          string
	nonInteractive            bool
}

func newGenerateClusterSpecCmd() *cobra.Command {
	opts := &generateClusterSpecOptions{}
	cmd := &cobra.Command{
		Use:   "clusterspec <cluster-name>",
		Short: "Generate a tenant cluster create spec",
		Long: `Write a complete CreateClusterRequest YAML document to standard output.

Pass the generated file unchanged to "nkectl create cluster -f". The provider
selects the request shape: "nico" generates NICo provider blocks and "capd"
generates local Docker-backed CAPI infrastructure blocks.

NICo needs identifiers for an organization, site, VPC, network, instance type,
SSH key group, OS profile, and role-specific config profiles. These values are
specific to the configured NICo site; use "nkectl generate clusterspec --help"
to see the corresponding flags. CAPD uses local development defaults.`,
		Example: `  # Local CAPD cluster
  nkectl generate clusterspec demo --provider capd > cluster.yaml
  nkectl create cluster -f cluster.yaml

  # NICo cluster using a VPC prefix
  nkectl generate clusterspec demo --provider nico \
    --org-id my-org --site-id <uuid> --vpc-id <uuid> \
    --vpc-prefix-id <uuid> --instance-type-id <uuid> \
    --ssh-key-group-id <uuid> --os-profile nke-os \
    --control-plane-config-profile default-cp \
    --worker-config-profile default-worker > cluster.yaml`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGenerateClusterSpec(cmd, args[0], opts)
		},
	}
	defaultHelp := cmd.HelpFunc()
	cmd.SetHelpFunc(func(command *cobra.Command, args []string) {
		command.Long = generateClusterSpecHelp(opts.provider)
		defaultHelp(command, args)
	})
	flags := cmd.Flags()
	flags.StringVar(&opts.provider, "provider", "", "Cluster infrastructure provider: nico or capd (required)")
	_ = cmd.MarkFlagRequired("provider")
	flags.StringVar(&opts.kubernetesVersion, "kubernetes-version", "1.34.2", "Kubernetes version as X.Y.Z")
	flags.StringVar(&opts.cniPlugin, "cni-plugin", "calico", "CNI plugin")
	flags.StringVar(&opts.cniVersion, "cni-version", "", "Optional CNI version as X.Y.Z")
	flags.BoolVar(&opts.etcdEncrypted, "etcd-encrypted", false, "Enable etcd encryption")
	flags.IntVar(&opts.controlPlaneCount, "control-plane-count", 1, "Number of control-plane nodes")
	flags.IntVar(&opts.workerCount, "worker-count", 1, "Number of worker nodes; use 0 for none")
	flags.StringVar(&opts.controlPlaneVIP, "control-plane-vip", "", "Optional control-plane virtual IP")
	flags.StringVar(&opts.systemReservedMemory, "system-reserved-memory", "1Gi", "System memory reservation for every node pool")

	flags.StringVar(&opts.orgID, "org-id", "", "NICo organization ID (required for --provider nico)")
	flags.StringVar(&opts.siteID, "site-id", "", "NICo site UUID (required for --provider nico)")
	flags.StringVar(&opts.vpcID, "vpc-id", "", "NICo VPC UUID (required for --provider nico)")
	flags.StringVar(&opts.vpcPrefixID, "vpc-prefix-id", "", "NICo VPC prefix UUID (exactly one of this or --subnet-id is required for --provider nico)")
	flags.StringVar(&opts.subnetID, "subnet-id", "", "NICo subnet UUID (exactly one of this or --vpc-prefix-id is required for --provider nico)")
	flags.StringVar(&opts.instanceTypeID, "instance-type-id", "", "NICo instance type UUID (required for --provider nico)")
	flags.StringVar(&opts.sshKeyGroupID, "ssh-key-group-id", "", "NICo SSH key group UUID (required for --provider nico)")
	flags.StringVar(&opts.osProfile, "os-profile", "", "NICo OS profile (required for --provider nico)")
	flags.StringVar(&opts.controlPlaneConfigProfile, "control-plane-config-profile", "", "NICo control-plane config profile (required for --provider nico)")
	flags.StringVar(&opts.workerConfigProfile, "worker-config-profile", "", "NICo worker config profile (required for --provider nico)")
	flags.StringVar(&opts.capdMachineImage, "capd-machine-image", "cwe-capd-kindest-node:local", "CAPD DockerMachine image")
	flags.BoolVar(&opts.nonInteractive, "non-interactive", false, "Reject missing values instead of prompting")
	return cmd
}

func generateClusterSpecHelp(provider string) string {
	common := `Write a complete CreateClusterRequest YAML document to standard output.

Pass the generated file unchanged to "nkectl create cluster -f". The provider
selects the request shape: "nico" generates NICo provider blocks and "capd"
generates local Docker-backed CAPI infrastructure blocks.`
	switch provider {
	case clusterspec.ProviderNico:
		return common + `

NICo input

NICo creates a top-level nico object and one nico object per node pool. Supply
the organization, site, VPC, instance type, SSH key group, OS profile, and
control-plane/worker config profiles for the target site. Select exactly one
network: --vpc-prefix-id on native-networking sites, or --subnet-id otherwise.

When run from a terminal, omitted NICo fields are prompted for on standard
error. YAML is still written only to standard output, so "> cluster.yaml" is
safe. Non-terminal invocations must supply every required NICo field as flags.`
	case clusterspec.ProviderCAPD:
		return common + `

CAPD input

CAPD generates DockerCluster and DockerMachineTemplate infrastructure manifests
for the local Tilt environment. It uses cwe-capd-kindest-node:local by default;
override it with --capd-machine-image only when the local environment uses a
different node image.`
	default:
		return common + `

Use --provider nico --help for NICo identifiers, profiles, and network input.
Use --provider capd --help for the local CAPD/Tilt request shape.`
	}
}

func runGenerateClusterSpec(cmd *cobra.Command, clusterName string, opts *generateClusterSpecOptions) error {
	if opts.provider == clusterspec.ProviderNico && !opts.nonInteractive && canPrompt(cmd) {
		if err := promptNicoFields(cmd, opts); err != nil {
			return err
		}
	}
	request, err := clusterspec.Build(clusterspec.Input{
		Provider:             opts.provider,
		Name:                 clusterName,
		KubernetesVersion:    opts.kubernetesVersion,
		CNIPlugin:            opts.cniPlugin,
		CNIVersion:           opts.cniVersion,
		EtcdEncrypted:        opts.etcdEncrypted,
		ControlPlaneCount:    opts.controlPlaneCount,
		WorkerCount:          opts.workerCount,
		ControlPlaneVIP:      opts.controlPlaneVIP,
		SystemReservedMemory: opts.systemReservedMemory,
		Nico: clusterspec.NicoInput{
			OrgID:                     opts.orgID,
			SiteID:                    opts.siteID,
			VpcID:                     opts.vpcID,
			VpcPrefixID:               opts.vpcPrefixID,
			SubnetID:                  opts.subnetID,
			InstanceTypeID:            opts.instanceTypeID,
			SSHKeyGroupID:             opts.sshKeyGroupID,
			OSProfile:                 opts.osProfile,
			ControlPlaneConfigProfile: opts.controlPlaneConfigProfile,
			WorkerConfigProfile:       opts.workerConfigProfile,
		},
		CAPD: clusterspec.CAPDInput{MachineImage: opts.capdMachineImage},
	})
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

func promptNicoFields(cmd *cobra.Command, opts *generateClusterSpecOptions) error {
	reader := bufio.NewReader(cmd.InOrStdin())
	if opts.vpcPrefixID == "" && opts.subnetID == "" {
		if err := promptNicoField(cmd.ErrOrStderr(), reader, "--vpc-prefix-id", "NICo VPC prefix UUID (leave blank to use a subnet)", &opts.vpcPrefixID); err != nil {
			return err
		}
		if opts.vpcPrefixID == "" {
			if err := promptNicoField(cmd.ErrOrStderr(), reader, "--subnet-id", "NICo subnet UUID", &opts.subnetID); err != nil {
				return err
			}
		}
	}
	for _, field := range []struct {
		name        string
		description string
		value       *string
	}{
		{"--org-id", "NICo organization ID", &opts.orgID},
		{"--site-id", "NICo site UUID", &opts.siteID},
		{"--vpc-id", "NICo VPC UUID", &opts.vpcID},
		{"--instance-type-id", "NICo instance type UUID", &opts.instanceTypeID},
		{"--ssh-key-group-id", "NICo SSH key group UUID", &opts.sshKeyGroupID},
		{"--os-profile", "NICo OS profile", &opts.osProfile},
		{"--control-plane-config-profile", "NICo control-plane config profile", &opts.controlPlaneConfigProfile},
	} {
		if err := promptNicoField(cmd.ErrOrStderr(), reader, field.name, field.description, field.value); err != nil {
			return err
		}
	}
	if opts.workerCount > 0 {
		if err := promptNicoField(cmd.ErrOrStderr(), reader, "--worker-config-profile", "NICo worker config profile", &opts.workerConfigProfile); err != nil {
			return err
		}
	}
	return nil
}

func promptNicoField(out io.Writer, reader *bufio.Reader, name, description string, value *string) error {
	if *value != "" {
		return nil
	}
	fmt.Fprintf(out, "%s: ", description)
	input, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("read %s: %w", name, err)
	}
	*value = strings.TrimSpace(input)
	if err == io.EOF && *value == "" {
		return fmt.Errorf("read %s: standard input ended before a value was supplied", name)
	}
	return nil
}

// generateSiteSpecOptions holds the flag values for "generate sitespec".
type generateSiteSpecOptions struct {
	nodes string
	sets  []string
}

func newGenerateSiteSpecCmd() *cobra.Command {
	opts := &generateSiteSpecOptions{}

	cmd := &cobra.Command{
		Use:   "sitespec <cluster-name>",
		Short: "Generate a site spec file for a tier-0 cluster",
		Long: `Write a complete site spec for "nkectl bootstrap site" to standard output.

The hosts come from a CSV so a real site can be described without repeating a
flag per machine. The output needs no editing: every field is validated before
anything is written, so a file this command produces is a file bootstrap site
accepts.

The CSV needs a header naming its columns, in any order. name and ip are
required; tpm_ek_thumbprint is optional per row and enables TPM attestation for
that host.

  name,ip,tpm_ek_thumbprint
  cp-01,10.10.50.2,0000000000000000000000000000000000000000000000000000000000000000
  cp-02,10.10.50.3,
  cp-03,10.10.50.4,

Anything the CSV does not cover is set with --set, using the same field paths
the file itself uses. Run with an unknown path to list them.

The generated file depends only on the arguments given: unlike bootstrap site,
this command ignores the inventory environment variables, so a site spec never
carries a value nobody typed.`,
		Example: `  # Generate a site spec and bootstrap from it
  nkectl generate sitespec my-tier0-site --nodes hosts.csv > sitespec.yaml
  nkectl bootstrap site -f sitespec.yaml

  # Override fields the CSV does not carry
  nkectl generate sitespec my-tier0-site --nodes hosts.csv \
    --set cluster.cniName=calico \
    --set cluster.podCidr=10.100.0.0/16 > sitespec.yaml`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGenerateSiteSpec(cmd, args[0], opts)
		},
	}

	cmd.Flags().StringVar(&opts.nodes, "nodes", "",
		"Path to the node CSV, or '-' to read standard input (required)")
	_ = cmd.MarkFlagRequired("nodes")
	cmd.Flags().StringArrayVar(&opts.sets, "set", nil,
		"Set a field, as path=value. Repeatable. Accepts: "+strings.Join(sitespec.SettablePaths(), ", "))

	return cmd
}

func runGenerateSiteSpec(cmd *cobra.Command, clusterName string, opts *generateSiteSpecOptions) error {
	nodes, err := readNodesCSV(cmd, opts.nodes)
	if err != nil {
		return err
	}

	spec, err := sitespec.Build(clusterName, nodes, opts.sets)
	if err != nil {
		return err
	}

	rendered, err := spec.Render()
	if err != nil {
		return err
	}

	_, err = cmd.OutOrStdout().Write(rendered)
	return err
}

// readNodesCSV reads the node list from path, or from standard input when path
// is "-", so the CSV can be piped from whatever already knows the hosts.
func readNodesCSV(cmd *cobra.Command, path string) ([]sitespec.Node, error) {
	if path == "-" {
		return sitespec.ParseNodesCSV("standard input", cmd.InOrStdin())
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open node CSV: %w", err)
	}
	defer func() { _ = file.Close() }()

	return sitespec.ParseNodesCSV(path, file)
}
