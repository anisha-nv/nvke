// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dsx-ai-factory/nvke/controller/internal/config"
	"github.com/dsx-ai-factory/nvke/controller/internal/controllerconfig"
)

const startupConfig = `metricsListenAddress: 127.0.0.1:9090
supportedInfraProviders: [nico]
spire:
  joinTokenTTL: 1h
nodeAgent:
  spireAttestationMethod: tpm
`

func TestLoadOIDCConfigMountedOverridesEnv(t *testing.T) {
	tests := []struct {
		name       string
		envEnabled bool
		management string
		want       config.NvkeOIDCAuth
	}{
		{
			name:       "mounted disable overrides environment enable and clears all fields",
			envEnabled: true,
			management: `    enabled: false
    issuerURL: ""
    discoveryURL: ""
    caBundleSecretRef: {namespace: "", name: "", key: ""}
`,
		},
		{
			name: "mounted enable overrides environment disable and replaces all fields",
			management: `    enabled: true
    issuerURL: https://mounted.example.com
    discoveryURL: https://discovery.example.com/.well-known/openid-configuration
    caBundleSecretRef: {namespace: mounted-system, name: mounted-ca, key: mounted.pem}
`,
			want: config.NvkeOIDCAuth{
				Enabled:      true,
				IssuerURL:    "https://mounted.example.com",
				DiscoveryURL: "https://discovery.example.com/.well-known/openid-configuration",
				CABundleSecret: config.OIDCCABundleSecretRef{
					Namespace: "mounted-system", Name: "mounted-ca", Key: "mounted.pem",
				},
			},
		},
		{
			name:       "enabled shared provider clears optional environment fields",
			envEnabled: true,
			management: `    enabled: true
    issuerURL: https://mounted.example.com
    discoveryURL: ""
    caBundleSecretRef: {}
`,
			want: config.NvkeOIDCAuth{Enabled: true, IssuerURL: "https://mounted.example.com"},
		},
		{
			name:       "disabled shared provider still maps configured fields",
			envEnabled: true,
			management: `    enabled: false
    issuerURL: https://mounted.example.com
    discoveryURL: https://discovery.example.com/.well-known/openid-configuration
    caBundleSecretRef: {namespace: mounted-system, name: mounted-ca, key: mounted.pem}
`,
			want: config.NvkeOIDCAuth{
				IssuerURL:    "https://mounted.example.com",
				DiscoveryURL: "https://discovery.example.com/.well-known/openid-configuration",
				CABundleSecret: config.OIDCCABundleSecretRef{
					Namespace: "mounted-system", Name: "mounted-ca", Key: "mounted.pem",
				},
			},
		},
		{
			name:       "omitted management settings clear environment settings",
			envEnabled: true,
			management: "    {}\n",
		},
	}

	const fleetProvider = `  providers:
    - name: corporate
      issuerURL: https://corporate.example.com
      audiences: [nvke-tenant-cluster]
      usernameClaim: sub
      usernamePrefix: "corporate:"
      groupsClaim: groups
      groupsPrefix: "corporate:"
      requiredClaims: {tenant_id: acme}
      caBundle: example-ca
`
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeStartupConfig(t, startupConfig+"oidc:\n  managementOIDCAuth:\n"+tt.management+fleetProvider)
			cfg := parseStartupEnv(t, path, tt.envEnabled)
			before := cfg

			fleet, err := loadOIDCConfig(&cfg)

			require.NoError(t, err)
			tt.want.AutoMigrationEnabled = true
			require.Equal(t, tt.want, cfg.NvkeOIDCAuth)
			require.Equal(t, []controllerconfig.OIDCProvider{{
				Name: "corporate", IssuerURL: "https://corporate.example.com",
				Audiences: []string{"nvke-tenant-cluster"}, UsernameClaim: "sub", UsernamePrefix: "corporate:",
				GroupsClaim: "groups", GroupsPrefix: "corporate:",
				RequiredClaims: map[string]string{"tenant_id": "acme"}, CABundle: "example-ca",
			}}, fleet.Providers)
			before.NvkeOIDCAuth = tt.want
			require.Equal(t, before, cfg)
		})
	}
}

func TestLoadOIDCConfigWithoutMountedFilePreservesEnv(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run("enabled="+strconv.FormatBool(enabled), func(t *testing.T) {
			cfg := parseStartupEnv(t, "", enabled)
			before := cfg

			fleet, err := loadOIDCConfig(&cfg)

			require.NoError(t, err)
			require.Equal(t, controllerconfig.OIDCConfig{}, fleet)
			require.Equal(t, before, cfg)
		})
	}
}

func TestLoadOIDCConfigFailurePreservesEnv(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		wantErr  string
	}{
		{name: "missing mounted file", wantErr: "open controller config"},
		{name: "malformed YAML", contents: "oidc: [", wantErr: "parse controller config"},
		{
			name:     "invalid management OIDC",
			contents: startupConfig + "oidc:\n  managementOIDCAuth:\n    enabled: true\n",
			wantErr:  "managementOIDCAuth.issuerURL is required",
		},
		{
			name: "invalid non-OIDC setting",
			contents: `metricsListenAddress: 127.0.0.1:9090
supportedInfraProviders: [nico]
spire:
  joinTokenTTL: 0s
nodeAgent:
  spireAttestationMethod: tpm
oidc:
  managementOIDCAuth:
    enabled: false
`,
			wantErr: "spire.joinTokenTTL must be greater than 0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "missing.yaml")
			if tt.contents != "" {
				path = writeStartupConfig(t, tt.contents)
			}
			cfg := parseStartupEnv(t, path, true)
			before := cfg

			fleet, err := loadOIDCConfig(&cfg)

			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, controllerconfig.OIDCConfig{}, fleet)
			require.Equal(t, before, cfg)
		})
	}
}

func parseStartupEnv(t *testing.T, path string, enabled bool) config.Config {
	t.Helper()
	t.Setenv("CNPG_SUPERUSER_PASSWORD", "example-token")
	t.Setenv("CONTROLLER_CONFIG_PATH", path)
	t.Setenv("NKE_OIDC_AUTH_ENABLED", strconv.FormatBool(enabled))
	t.Setenv("NKE_OIDC_AUTH_AUTO_MIGRATION_ENABLED", "true")
	t.Setenv("NKE_OIDC_AUTH_ISSUER_URL", "https://legacy.example.com")
	t.Setenv("NKE_OIDC_AUTH_DISCOVERY_URL", "https://legacy.example.com/.well-known/openid-configuration")
	t.Setenv("NKE_OIDC_AUTH_CA_BUNDLE_SECRET_NAMESPACE", "legacy-system")
	t.Setenv("NKE_OIDC_AUTH_CA_BUNDLE_SECRET_NAME", "legacy-ca")
	t.Setenv("NKE_OIDC_AUTH_CA_BUNDLE_SECRET_KEY", "legacy.pem")
	cfg, err := config.ParseConfigFromEnv()
	require.NoError(t, err)
	require.Equal(t, config.NvkeOIDCAuth{
		Enabled: enabled, AutoMigrationEnabled: true,
		IssuerURL: "https://legacy.example.com", DiscoveryURL: "https://legacy.example.com/.well-known/openid-configuration",
		CABundleSecret: config.OIDCCABundleSecretRef{Namespace: "legacy-system", Name: "legacy-ca", Key: "legacy.pem"},
	}, cfg.NvkeOIDCAuth)
	return cfg
}

func writeStartupConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}
