// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package clients

import (
	"context"
	"fmt"

	api "github.com/NVIDIA/nke/controller/api"
)

// CreateNodePool adds a node pool to an existing cluster.
func (c *CWEClient) CreateNodePool(
	ctx context.Context,
	req api.CreateNodePoolJSONRequestBody,
) (*api.NodePool, error) {
	resp, err := c.raw.CreateNodePoolWithResponse(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("creating node pool: %w", err)
	}
	if resp.StatusCode() != 201 {
		return nil, fmt.Errorf("creating node pool: %w", apiError(resp.Status(), resp.Body))
	}
	if resp.JSON201 == nil {
		return nil, fmt.Errorf("creating node pool: empty response body")
	}
	return resp.JSON201, nil
}

// ListNodePools returns node pools matching params.
func (c *CWEClient) ListNodePools(
	ctx context.Context,
	params *api.ListNodePoolsParams,
) ([]api.NodePool, error) {
	resp, err := c.raw.ListNodePoolsWithResponse(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("listing node pools: %w", err)
	}
	if resp.StatusCode() != 200 {
		return nil, fmt.Errorf("listing node pools: %w", apiError(resp.Status(), resp.Body))
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("listing node pools: empty response body")
	}
	return resp.JSON200.NodePools, nil
}
