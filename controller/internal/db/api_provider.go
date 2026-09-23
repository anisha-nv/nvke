// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"errors"
)

// APIProvider identifies the immutable public input contract of a cluster.
type APIProvider string

const (
	APIProviderNico        APIProvider = "nico"
	APIProviderLaunchLayer APIProvider = "launchlayer"
	APIProviderInfra       APIProvider = "infra"
)

var (
	// ErrClusterAPIProviderImmutable reports an attempt to change the public contract.
	ErrClusterAPIProviderImmutable = errors.New("cluster API mode is immutable")
	// ErrInvalidAPIProviderBinding reports corrupt or incompatible stored API mode.
	ErrInvalidAPIProviderBinding = errors.New("stored cluster API mode needs repair")
)

// ResolveAPIProvider resolves legacy NULL modes without inspecting manifest kinds.
func (c Cluster) ResolveAPIProvider() (APIProvider, error) {
	mode := c.APIProvider
	if mode == "" {
		switch c.ProviderType {
		case ProviderTypeNico:
			return APIProviderNico, nil
		case ProviderTypeCAPI:
			return APIProviderInfra, nil
		default:
			return "", ErrInvalidAPIProviderBinding
		}
	}
	if (mode == APIProviderNico && c.ProviderType == ProviderTypeNico) ||
		((mode == APIProviderLaunchLayer || mode == APIProviderInfra) && c.ProviderType == ProviderTypeCAPI) {
		return mode, nil
	}
	return "", ErrInvalidAPIProviderBinding
}
