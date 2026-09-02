// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package clients

import (
	"context"
	"fmt"

	api "github.com/NVIDIA/nke/controller/api"
)

// ListNodes returns nodes matching params.
func (c *CWEClient) ListNodes(ctx context.Context, params *api.ListNodesParams) ([]api.Node, error) {
	resp, err := c.raw.ListNodesWithResponse(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	if resp.StatusCode() != 200 {
		return nil, fmt.Errorf("listing nodes: %w", apiError(resp.Status(), resp.Body))
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("listing nodes: empty response body")
	}
	return resp.JSON200.Nodes, nil
}
