// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command tests keep informational flags independent of credentials and require explicit unauthenticated evaluation.
package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// TestInformationalFlagsDoNotLoadCredentials verifies help and version remain usable before datastore setup.
func TestInformationalFlagsDoNotLoadCredentials(t *testing.T) {
	for _, flag := range []string{"--version", "-h", "--help"} {
		t.Run(flag, func(t *testing.T) {
			var output bytes.Buffer
			require.NoError(t, run(context.Background(), []string{flag}, &output))
			if flag == "--version" {
				require.Equal(t, version+"\n", output.String())
				return
			}
			require.Contains(t, output.String(), "Usage of tenant-api:")
			require.Contains(t, output.String(), "-kubeconfig")
			require.Contains(t, output.String(), "-context")
		})
	}
}

// TestContextFlagSelectsDatastore verifies the CLI forwards the override before validating the selected endpoint.
func TestContextFlagSelectsDatastore(t *testing.T) {
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "kubeconfig")
	raw := clientcmdapi.NewConfig()
	raw.CurrentContext = "missing"
	raw.Contexts["selected"] = &clientcmdapi.Context{Cluster: "selected", AuthInfo: "selected"}
	raw.Clusters["selected"] = &clientcmdapi.Cluster{Server: "http://storage.example"}
	raw.AuthInfos["selected"] = &clientcmdapi.AuthInfo{}
	require.NoError(t, clientcmd.WriteToFile(*raw, kubeconfig))
	var output bytes.Buffer
	err := run(context.Background(), []string{
		"--evaluation-no-auth", "--kubeconfig=" + kubeconfig, "--namespace=evaluation",
		"--page-token-key-file=" + filepath.Join(dir, "page-key"), "--context=selected",
	}, &output)
	require.ErrorContains(t, err, "HTTPS origin")
}

// TestCapacityFlagsRejectInvalidLimits verifies each CLI budget is enforced before credentials are loaded.
func TestCapacityFlagsRejectInvalidLimits(t *testing.T) {
	dir := t.TempDir()
	for _, flag := range []string{
		"max-http-connections", "max-grpc-connections", "max-concurrent-streams", "max-concurrent-requests",
	} {
		for _, value := range []int{0, 1025} {
			t.Run(flag+"/"+strconv.Itoa(value), func(t *testing.T) {
				var output bytes.Buffer
				err := run(context.Background(), []string{
					"--evaluation-no-auth", "--kubeconfig=" + filepath.Join(dir, "kubeconfig"), "--namespace=evaluation",
					"--page-token-key-file=" + filepath.Join(dir, "page-key"), "--" + flag + "=" + strconv.Itoa(value),
				}, &output)
				require.ErrorContains(t, err, flag+" must be between 1 and 1024")
			})
		}
	}
}

// TestAuthenticationMustBeExplicitlyDeferred verifies the command cannot silently start without caller authentication.
func TestAuthenticationMustBeExplicitlyDeferred(t *testing.T) {
	var output bytes.Buffer
	err := run(context.Background(), nil, &output)
	require.ErrorContains(t, err, "--evaluation-no-auth")
}
