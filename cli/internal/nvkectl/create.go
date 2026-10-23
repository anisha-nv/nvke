// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkectl

import (
	"github.com/spf13/cobra"

	"github.com/dsx-ai-factory/nvke/cli/internal/controller/clusters"
	"github.com/dsx-ai-factory/nvke/cli/internal/controller/nodepools"
	operatorcmd "github.com/dsx-ai-factory/nvke/cli/internal/operator/command"
)

func newCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a resource",
	}
	siteCmds := append([]*cobra.Command{
		clusters.NewCreateCmd(loadClient),
		nodepools.NewCreateCmd(loadClient),
	}, operatorcmd.NewCreateCmds(loadOperator)...)
	markUsesSite(siteCmds...)
	cmd.AddCommand(siteCmds...)
	return cmd
}
