// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkectl

import (
	"github.com/spf13/cobra"

	"github.com/dsx-ai-factory/nvke/cli/internal/controller/clusters"
	"github.com/dsx-ai-factory/nvke/cli/internal/controller/nodepools"
	operatorcmd "github.com/dsx-ai-factory/nvke/cli/internal/operator/command"
)

func newDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a resource",
	}
	siteCmds := append([]*cobra.Command{
		clusters.NewDeleteCmd(loadClient),
		nodepools.NewDeleteCmd(loadClient),
	}, operatorcmd.NewDeleteCmds(loadOperator)...)
	markUsesSite(siteCmds...)
	cmd.AddCommand(siteCmds...)
	return cmd
}
