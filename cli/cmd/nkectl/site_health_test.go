// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/dsx-ai-factory/nke/cli/internal/config"
	"github.com/dsx-ai-factory/nke/controller/api"
)

func TestClusterReachabilitySiteBoundary(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	oldSite, oldVerbose, oldJSONLogs := site, verbose, jsonLogs
	t.Cleanup(func() { site, verbose, jsonLogs = oldSite, oldVerbose, oldJSONLogs })
	var callsA, callsB atomic.Int32
	endpoint := func(name string, calls *atomic.Int32) *httptest.Server {
		cluster := api.Cluster{ID: uuid.New(), Name: name}
		cluster.Conditions.APIServerReachable = &api.Condition{Status: api.ConditionStatusTrue}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			require.Equal(t, "/api/clusters", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"clusters": []api.Cluster{cluster}}))
		}))
		t.Cleanup(server.Close)
		return server
	}
	a, b := endpoint("tenant-a", &callsA), endpoint("tenant-b", &callsB)
	cfg := &config.Config{CurrentSite: "a", Sites: map[string]config.SiteConfig{"a": {URL: a.URL}, "b": {URL: b.URL}}}
	require.NoError(t, cfg.Save())
	for _, format := range []string{"json", "yaml"} {
		root := newRootCmd("test", "test", "test", "test")
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs([]string{"--site", "b", "get", "clusters", "-o", format})
		require.NoError(t, root.Execute())
		data := out.Bytes()
		if format == "yaml" {
			data = []byte(expectJSON(t, data))
		}
		var clusters []api.Cluster
		require.NoError(t, json.Unmarshal(data, &clusters))
		require.Len(t, clusters, 1)
		require.Equal(t, "tenant-b", clusters[0].Name)
		require.Equal(t, api.ConditionStatusTrue, clusters[0].Conditions.APIServerReachable.Status)
	}
	require.Zero(t, callsA.Load(), "selecting b must not contact a")
	require.EqualValues(t, 2, callsB.Load())
	loaded, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, "a", loaded.CurrentSite)
}

func expectJSON(t *testing.T, data []byte) string {
	t.Helper()
	converted, err := yaml.YAMLToJSON(data)
	require.NoError(t, err)
	return string(converted)
}
