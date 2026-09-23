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

// NewActivitiesForTest builds Activities with the given SPIRE trust-domain-bundle
// getter and join-token creator, so a test in another package can exercise
// SpiffeEnabled-true code paths without a live SPIRE server, the same way an
// in-package test can set the unexported fields directly.
//
// This is a free function rather than an Activities method: worker.RegisterActivity
// registers every exported method of the value NewActivities returns, so an
// exported builder method on *Activities panics at registration time unless
// its signature matches an activity's (result, error) shape.
func NewActivitiesForTest(
	cfg config.Config,
	database db.DB,
	k8sClient ctrlclient.Client,
	nodeAgentClient agent.Client,
	trustDomain string,
	tenantClientProvider TenantClientProvider,
	getTrustDomainBundle trustDomainAndBundleGetter,
	createJoinToken joinTokenCreator,
) *Activities {
	a := NewActivities(cfg, database, k8sClient, nodeAgentClient, trustDomain, tenantClientProvider)
	a.getTrustDomainBundle = getTrustDomainBundle
	a.createJoinToken = createJoinToken
	return a
}
