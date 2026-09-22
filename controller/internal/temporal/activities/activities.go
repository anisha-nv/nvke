// SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package activities

import (
	"context"

	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dsx-ai-factory/nke/controller/internal/agent"
	"github.com/dsx-ai-factory/nke/controller/internal/config"
	"github.com/dsx-ai-factory/nke/controller/internal/db"
	"github.com/dsx-ai-factory/nke/controller/internal/spiffe"
)

type trustDomainAndBundleGetter func(context.Context, config.Config) (string, string, error)

type joinTokenCreator func(context.Context, config.Config, string) (string, error)

type Activities struct {
	cfg                  config.Config
	db                   db.DB
	k8sClient            ctrlclient.Client
	nodeAgentClient      agent.Client
	tenantClientProvider TenantClientProvider
	trustDomain          string
	getTrustDomainBundle trustDomainAndBundleGetter
	createJoinToken      joinTokenCreator
}

func NewActivities(
	cfg config.Config,
	database db.DB,
	k8sClient ctrlclient.Client,
	nodeAgentClient agent.Client,
	trustDomain string,
	tenantClientProvider TenantClientProvider,
) *Activities {
	return &Activities{
		cfg:                  cfg,
		db:                   database,
		k8sClient:            k8sClient,
		nodeAgentClient:      nodeAgentClient,
		tenantClientProvider: tenantClientProvider,
		trustDomain:          trustDomain,
		getTrustDomainBundle: spiffe.GetTrustDomainAndBundle,
		createJoinToken:      spiffe.CreateJoinToken,
	}
}

// WithTrustDomainBundleGetter overrides how Activities fetches the SPIRE
// trust domain and bundle. Production callers never need this: NewActivities
// already wires the real implementation. It exists so a test in another
// package can exercise SpiffeEnabled-true code paths without a live SPIRE
// server, the same way an in-package test can set the unexported field
// directly.
func (a *Activities) WithTrustDomainBundleGetter(fn trustDomainAndBundleGetter) *Activities {
	a.getTrustDomainBundle = fn
	return a
}

// WithJoinTokenCreator overrides how Activities mints a SPIRE join token, for
// the same reason and with the same production-caller exemption as
// WithTrustDomainBundleGetter.
func (a *Activities) WithJoinTokenCreator(fn joinTokenCreator) *Activities {
	a.createJoinToken = fn
	return a
}
