// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package activities

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	nkeproto "github.com/dsx-ai-factory/nke/agent/pkg/agent/proto"

	"github.com/dsx-ai-factory/nke/controller/internal/config"
	"github.com/dsx-ai-factory/nke/controller/internal/controllerconfig"
	"github.com/dsx-ai-factory/nke/controller/internal/db"
	"github.com/dsx-ai-factory/nke/controller/internal/kubeadmconfig"
	adapter "github.com/dsx-ai-factory/nke/controller/internal/rollout/adapters/controller"
	"github.com/dsx-ai-factory/nke/controller/internal/rollout/operations/oidc"
)

func TestBuildOIDCAuthenticationConfig_PinnedProviders(t *testing.T) {
	provider := controllerconfig.OIDCProvider{
		Name: "workforce", IssuerURL: "https://workforce.example.com", Audiences: []string{"tenant"},
		UsernameClaim: "sub", UsernamePrefix: "workforce:", GroupsClaim: "groups", GroupsPrefix: "workforce:",
		RequiredClaims: map[string]string{"organization": "example"}, CABundle: "example public CA bundle",
	}
	a := &Activities{
		cfg:         config.Config{NkeOIDCAuth: config.NkeOIDCAuth{Enabled: true, IssuerURL: "https://shared.example.com"}},
		trustDomain: "jwt.example.com",
	}
	pinned := db.ClusterOIDCProvidersFromConfig([]controllerconfig.OIDCProvider{provider})
	tests := []struct {
		name    string
		cluster db.Cluster
		needs   bool
		issuers []string
	}{
		{name: "nothing to render", cluster: db.Cluster{Name: "tenant"}},
		{name: "shared only", cluster: db.Cluster{Name: "tenant", OIDCAuthEnabled: true}, needs: true, issuers: []string{"https://shared.example.com"}},
		{name: "operator only before enablement", cluster: db.Cluster{Name: "tenant", OIDCProviders: pinned}, needs: true, issuers: []string{provider.IssuerURL}},
		{
			name: "combined after enablement", cluster: db.Cluster{Name: "tenant", OIDCAuthEnabled: true, OIDCProviders: pinned},
			needs: true, issuers: []string{"https://shared.example.com", provider.IssuerURL},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.needs, needsAuthenticationConfig(tt.cluster))
			if !tt.needs {
				return
			}
			tt.cluster.KubernetesVersion = "v1.34.2"
			cfg, err := a.buildOIDCAuthenticationConfig(context.Background(), tt.cluster)
			require.NoError(t, err)
			got := make([]string, 0, len(cfg.JWT))
			for _, jwt := range cfg.JWT {
				got = append(got, jwt.Issuer.URL)
			}
			require.Equal(t, tt.issuers, got)

			file, err := kubeadmconfig.GenerateAPIServerAuthenticationConfig(cfg, tt.cluster.KubernetesVersion)
			require.NoError(t, err)
			op := oidc.NewEnableOIDCOperation(
				oidc.WithOIDCConfig(&a.cfg.NkeOIDCAuth),
				oidc.WithOperatorJWTPolicy(kubeadmconfig.OperatorJWTPolicy{ManagementJWTTrustDomain: a.trustDomain}),
			)
			payload, err := op.BuildApplyToNodePayload(context.Background(), adapter.NewCluster(tt.cluster, nil, nil, "50051"))
			require.NoError(t, err)
			var artifact struct {
				Files    []*nkeproto.File `json:"files"`
				Previous *struct {
					Files []*nkeproto.File `json:"files"`
				} `json:"previous"`
			}
			require.NoError(t, json.Unmarshal(payload, &artifact))
			files := artifact.Files
			if !tt.cluster.OIDCAuthEnabled {
				require.NotNil(t, artifact.Previous, "operator-only bootstrap must have a rollback configuration")
				files = artifact.Previous.Files
			}
			require.Equal(t, toNodeAgentFiles([]kubeadmconfig.File{file}), files, "bootstrap and rollout must render identical authentication files")
		})
	}
}
