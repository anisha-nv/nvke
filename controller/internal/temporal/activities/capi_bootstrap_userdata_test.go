// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package activities

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/NVIDIA/nke/controller/internal/config"
	"github.com/NVIDIA/nke/controller/internal/db"
)

func TestRenderCAPIBootstrapUserData_TPMAttestor(t *testing.T) {
	activities := &Activities{
		cfg: config.Config{
			SpiffeEnabled:      true,
			SpireServerAddress: "spire.example.org",
			SpireServerPort:    "9091",
		},
		getTrustDomainBundle: func(context.Context, config.Config) (string, string, error) {
			return "example.org", "-----BEGIN CERT-----\nabc123\n-----END CERT-----", nil
		},
	}

	var nodeSet db.NodeSet
	nodeSet.SetCapi(db.CAPINodePoolProvider{
		SpireAttestationMethod: db.SpireAttestationMethodTPM,
		BootstrapFiles: []db.CloudInitFile{
			{Path: "/etc/custom.conf", Owner: "root:root", Permissions: "0600", Content: "hello"},
		},
		PreKubeadmCommands: []string{"echo before kubeadm"},
	})
	userData, err := activities.renderCAPIBootstrapUserData(context.Background(), nodeSet, "")
	require.NoError(t, err)

	assert.Contains(t, userData, "#cloud-config\n")
	assert.Contains(t, userData, "/etc/custom.conf")
	assert.Contains(t, userData, "/etc/spire-agent/agent.conf")
	assert.Contains(t, userData, "/etc/spire-agent/initial-trust-bundle.pem")
	assert.Contains(t, userData, `NodeAttestor "tpm"`)
	assert.NotContains(t, userData, `NodeAttestor "join_token"`)
	assert.Contains(t, userData, "echo before kubeadm")

	files := parseCAPIBootstrapFiles(t, userData)
	assertCAPIBootstrapFile(t, files, "/etc/spire-agent/initial-trust-bundle.pem", "0644", "-----BEGIN CERT-----\nabc123\n-----END CERT-----")
	legacyConfig := capiBootstrapFile(t, files, "/etc/spire-agent/agent.conf")
	assert.Equal(t, "root:root", legacyConfig.Owner)
	assert.Equal(t, "0644", legacyConfig.Permissions)
	assert.Contains(t, legacyConfig.Content, `NodeAttestor "tpm"`)
	assertCAPIBootstrapFile(t, files, "/etc/nke/trust-bundle.pem", "0644", "-----BEGIN CERT-----\nabc123\n-----END CERT-----")
	assertCAPIBootstrapFile(t, files, "/etc/nke/spire.env", "0600", "SPIRE_SERVER_ADDRESS=\"spire.example.org\"\nSPIRE_SERVER_PORT=\"9091\"\nSPIRE_TRUST_DOMAIN=\"example.org\"\n")
}

func TestRenderCAPIBootstrapUserData_JoinTokenAttestor(t *testing.T) {
	activities := &Activities{
		cfg: config.Config{
			SpiffeEnabled:      true,
			SpireServerAddress: "spire.example.org",
			SpireServerPort:    "9091",
		},
		getTrustDomainBundle: func(context.Context, config.Config) (string, string, error) {
			return "example.org", "bundle", nil
		},
	}

	var nodeSet db.NodeSet
	nodeSet.SetCapi(db.CAPINodePoolProvider{SpireAttestationMethod: db.SpireAttestationMethodJoinToken})
	userData, err := activities.renderCAPIBootstrapUserData(context.Background(), nodeSet, "join-token-123")
	require.NoError(t, err)

	assert.Contains(t, userData, `join_token = "join-token-123"`)
	assert.Contains(t, userData, `NodeAttestor "join_token"`)
	assert.NotContains(t, userData, `NodeAttestor "tpm"`)

	files := parseCAPIBootstrapFiles(t, userData)
	legacyConfig := capiBootstrapFile(t, files, "/etc/spire-agent/agent.conf")
	assert.Equal(t, "root:root", legacyConfig.Owner)
	assert.Equal(t, "0644", legacyConfig.Permissions)
	assert.Contains(t, legacyConfig.Content, `join_token = "join-token-123"`)
	assertCAPIBootstrapFile(t, files, "/etc/nke/spire.env", "0600", "SPIRE_SERVER_ADDRESS=\"spire.example.org\"\nSPIRE_SERVER_PORT=\"9091\"\nSPIRE_TRUST_DOMAIN=\"example.org\"\nSPIRE_JOIN_TOKEN=\"join-token-123\"\n")
}

func TestRenderCAPIBootstrapUserData_NativeSpireEnvironmentDoesNotExecuteIdentityValues(t *testing.T) {
	markerPath := filepath.Join(t.TempDir(), "identity-was-executed")
	joinToken := `$(touch ` + markerPath + `)|&;"\`
	activities := &Activities{
		cfg: config.Config{
			SpiffeEnabled:      true,
			SpireServerAddress: "192.0.2.1",
			SpireServerPort:    "9091",
		},
		getTrustDomainBundle: func(context.Context, config.Config) (string, string, error) {
			return "example.org", "bundle", nil
		},
	}

	var nodeSet db.NodeSet
	nodeSet.SetCapi(db.CAPINodePoolProvider{SpireAttestationMethod: db.SpireAttestationMethodJoinToken})
	userData, err := activities.renderCAPIBootstrapUserData(context.Background(), nodeSet, joinToken)
	require.NoError(t, err)

	files := parseCAPIBootstrapFiles(t, userData)
	envFile := capiBootstrapFile(t, files, "/etc/nke/spire.env")
	envPath := filepath.Join(t.TempDir(), "spire.env")
	require.NoError(t, os.WriteFile(envPath, []byte(envFile.Content), 0o600))

	command := exec.CommandContext(t.Context(), "bash", "-c", `set -eu; source "$1"; test "$SPIRE_SERVER_ADDRESS" = "$2"; test "$SPIRE_SERVER_PORT" = "$3"; test "$SPIRE_TRUST_DOMAIN" = "$4"; test "$SPIRE_JOIN_TOKEN" = "$5"`, "bash", envPath, "192.0.2.1", "9091", "example.org", joinToken)
	require.NoError(t, command.Run())
	_, err = os.Stat(markerPath)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestBuildCAPINativeSpireEnvironmentValidatesIdentityFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		serverAddress string
		serverPort    string
		trustDomain   string
		wantErr       string
	}{
		{
			name:          "valid DNS host",
			serverAddress: "spire.example.org",
			serverPort:    "9091",
			trustDomain:   "example.org",
		},
		{
			name:          "valid IPv4 address",
			serverAddress: "192.0.2.1",
			serverPort:    "65535",
			trustDomain:   "example.org",
		},
		{
			name:          "valid IPv6 address",
			serverAddress: "2001:db8::1",
			serverPort:    "1",
			trustDomain:   "example.org",
		},
		{
			name:          "server address pipe",
			serverAddress: "spire.example.org|touch",
			serverPort:    "9091",
			trustDomain:   "example.org",
			wantErr:       "SPIRE_SERVER_ADDRESS",
		},
		{
			name:          "server address ampersand",
			serverAddress: "spire.example.org&touch",
			serverPort:    "9091",
			trustDomain:   "example.org",
			wantErr:       "SPIRE_SERVER_ADDRESS",
		},
		{
			name:          "server address quote",
			serverAddress: `spire.example.org"`,
			serverPort:    "9091",
			trustDomain:   "example.org",
			wantErr:       "SPIRE_SERVER_ADDRESS",
		},
		{
			name:          "server address backslash",
			serverAddress: `spire.example.org\`,
			serverPort:    "9091",
			trustDomain:   "example.org",
			wantErr:       "SPIRE_SERVER_ADDRESS",
		},
		{
			name:          "server address NUL",
			serverAddress: "spire.example.org\x00",
			serverPort:    "9091",
			trustDomain:   "example.org",
			wantErr:       "SPIRE_SERVER_ADDRESS",
		},
		{
			name:          "server address newline",
			serverAddress: "spire.example.org\nnext",
			serverPort:    "9091",
			trustDomain:   "example.org",
			wantErr:       "SPIRE_SERVER_ADDRESS",
		},
		{
			name:          "zero port",
			serverAddress: "spire.example.org",
			serverPort:    "0",
			trustDomain:   "example.org",
			wantErr:       "SPIRE_SERVER_PORT",
		},
		{
			name:          "port exceeds maximum",
			serverAddress: "spire.example.org",
			serverPort:    "65536",
			trustDomain:   "example.org",
			wantErr:       "SPIRE_SERVER_PORT",
		},
		{
			name:          "port injection",
			serverAddress: "spire.example.org",
			serverPort:    "9091|touch",
			trustDomain:   "example.org",
			wantErr:       "SPIRE_SERVER_PORT",
		},
		{
			name:          "trust domain ampersand",
			serverAddress: "spire.example.org",
			serverPort:    "9091",
			trustDomain:   "trust&domain",
			wantErr:       "SPIRE_TRUST_DOMAIN",
		},
		{
			name:          "trust domain pipe",
			serverAddress: "spire.example.org",
			serverPort:    "9091",
			trustDomain:   "trust|domain",
			wantErr:       "SPIRE_TRUST_DOMAIN",
		},
		{
			name:          "trust domain quote",
			serverAddress: "spire.example.org",
			serverPort:    "9091",
			trustDomain:   `trust"domain`,
			wantErr:       "SPIRE_TRUST_DOMAIN",
		},
		{
			name:          "trust domain backslash",
			serverAddress: "spire.example.org",
			serverPort:    "9091",
			trustDomain:   `trust\domain`,
			wantErr:       "SPIRE_TRUST_DOMAIN",
		},
		{
			name:          "invalid SPIFFE trust domain",
			serverAddress: "spire.example.org",
			serverPort:    "9091",
			trustDomain:   `example.org"; touch`,
			wantErr:       "SPIRE_TRUST_DOMAIN",
		},
		{
			name:          "SPIFFE ID is not a trust domain",
			serverAddress: "spire.example.org",
			serverPort:    "9091",
			trustDomain:   "spiffe://example.org/injection",
			wantErr:       "SPIRE_TRUST_DOMAIN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := buildCAPINativeSpireEnvironment(
				db.SpireAttestationMethodTPM,
				tt.serverAddress,
				tt.serverPort,
				tt.trustDomain,
				"",
			)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestRenderCAPIBootstrapUserData_JoinTokenRequiresToken(t *testing.T) {
	activities := &Activities{cfg: config.Config{}}

	var nodeSet db.NodeSet
	nodeSet.SetCapi(db.CAPINodePoolProvider{SpireAttestationMethod: db.SpireAttestationMethodJoinToken})
	_, err := activities.renderCAPIBootstrapUserData(context.Background(), nodeSet, "")
	require.EqualError(t, err, "spire join token is required when infra.spireAttestationMethod is joinToken")
}

// A site stood up with --disable-mtls has no SPIRE, so there is no workload
// socket to read a bundle from. The node gets no bundle and no agent config,
// and rendering must not try to fetch one: the dial would fail, the activity
// would retry forever, and the cluster would never leave Provisioning.
func TestRenderCAPIBootstrapUserData_SpiffeDisabled(t *testing.T) {
	called := false
	activities := &Activities{
		cfg: config.Config{SpiffeEnabled: false},
		getTrustDomainBundle: func(context.Context, config.Config) (string, string, error) {
			called = true
			return "", "", errors.New("workload API is not reachable without SPIRE")
		},
	}

	var nodeSet db.NodeSet
	nodeSet.SetCapi(db.CAPINodePoolProvider{
		SpireAttestationMethod: db.SpireAttestationMethodTPM,
		BootstrapFiles: []db.CloudInitFile{
			{Path: "/etc/custom.conf", Owner: "root:root", Permissions: "0600", Content: "hello"},
		},
		PreKubeadmCommands: []string{"echo before kubeadm"},
	})

	userData, err := activities.renderCAPIBootstrapUserData(context.Background(), nodeSet, "")
	require.NoError(t, err)

	assert.False(t, called, "the workload API must not be dialed when SPIFFE is off")
	assert.NotContains(t, userData, "/etc/spire-agent/agent.conf")
	assert.NotContains(t, userData, "/etc/spire-agent/initial-trust-bundle.pem")
	assert.NotContains(t, userData, "/etc/nke/spire.env")
	assert.NotContains(t, userData, "/etc/nke/trust-bundle.pem")
	assert.Contains(t, userData, "/etc/custom.conf")
	assert.Contains(t, userData, "echo before kubeadm")
}

func parseCAPIBootstrapFiles(t *testing.T, userData string) []capiCloudInitFile {
	t.Helper()
	var cloudInit capiCloudInitConfig
	require.NoError(t, yaml.Unmarshal([]byte(userData), &cloudInit))
	return cloudInit.WriteFiles
}

func assertCAPIBootstrapFile(t *testing.T, files []capiCloudInitFile, path, permissions, content string) {
	t.Helper()
	file := capiBootstrapFile(t, files, path)
	assert.Equal(t, "root:root", file.Owner)
	assert.Equal(t, permissions, file.Permissions)
	assert.Equal(t, content, file.Content)
}

func capiBootstrapFile(t *testing.T, files []capiCloudInitFile, path string) capiCloudInitFile {
	t.Helper()
	for _, file := range files {
		if file.Path == path {
			return file
		}
	}
	require.Failf(t, "missing cloud-init file", "path %q", path)
	return capiCloudInitFile{}
}
