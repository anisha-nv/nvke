// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package site

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/dsx-ai-factory/nke/controller/test/e2e/internal/cwe"
	"github.com/dsx-ai-factory/nke/controller/test/e2e/internal/kube"
)

// forwardKubeconfig fetches the admin kubeconfig from the node, forwards its
// API server port, and writes a copy that reaches the cluster through the
// forward.
func (b bootstrap) forwardKubeconfig(ctx context.Context) (string, error) {
	raw, err := b.session.Fetch(ctx, kubeconfigPath)
	if err != nil {
		return "", err
	}
	port, err := apiServerPort(raw)
	if err != nil {
		return "", err
	}
	// The site's API server is on the node itself, so this one is loopback.
	local, err := b.session.Forward(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return "", err
	}
	rewritten, err := rewriteServer(raw, local)
	if err != nil {
		return "", err
	}
	outputDir, err := os.MkdirTemp("", "nke-e2e-site-")
	if err != nil {
		return "", err
	}
	out := filepath.Join(outputDir, b.spec.Name+".kubeconfig")
	if err := os.WriteFile(out, rewritten, 0o600); err != nil {
		return "", err
	}
	return out, nil
}

func apiServerPort(raw []byte) (int, error) {
	cfg, err := clientcmd.Load(raw)
	if err != nil {
		return 0, fmt.Errorf("parse kubeconfig: %w", err)
	}
	cluster, err := currentCluster(cfg)
	if err != nil {
		return 0, err
	}
	u, err := url.Parse(cluster.Server)
	if err != nil {
		return 0, fmt.Errorf("parse server %q: %w", cluster.Server, err)
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return strconv.Atoi(port)
}

// rewriteServer points the current cluster at local. The API server
// certificate does not cover the loopback address the forward listens on, so
// TLS keeps verifying against the host the node advertised.
func rewriteServer(raw []byte, local string) ([]byte, error) {
	cfg, err := clientcmd.Load(raw)
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	cluster, err := currentCluster(cfg)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(cluster.Server)
	if err != nil {
		return nil, fmt.Errorf("parse server %q: %w", cluster.Server, err)
	}
	if _, _, err := net.SplitHostPort(local); err != nil {
		return nil, fmt.Errorf("forward address %q: %w", local, err)
	}
	if cluster.TLSServerName == "" {
		cluster.TLSServerName = u.Hostname()
	}
	u.Host = local
	cluster.Server = u.String()
	return clientcmd.Write(*cfg)
}

func currentCluster(cfg *clientcmdapi.Config) (*clientcmdapi.Cluster, error) {
	kctx, ok := cfg.Contexts[cfg.CurrentContext]
	if !ok {
		return nil, fmt.Errorf("kubeconfig current context %q not found", cfg.CurrentContext)
	}
	cluster, ok := cfg.Clusters[kctx.Cluster]
	if !ok {
		return nil, fmt.Errorf("kubeconfig cluster %q not found", kctx.Cluster)
	}
	return cluster, nil
}

const (
	// defaultReadyTimeout and defaultPollInterval bound ProbeCWE when a caller
	// states neither. Here rather than with Bootstrap, which no longer waits
	// for anything once Kubernetes answers.
	defaultReadyTimeout = 10 * time.Minute
	defaultPollInterval = 10 * time.Second
)

// ProbeCWE polls until CWE answers /healthz through kubeconfig, then returns
// the version it reports.
//
// Separate from Bootstrap because only a run that deployed CWE has one to ask.
// A run that stopped at Kubernetes asserts against the API server instead, and
// expresses that by not calling this.
func ProbeCWE(ctx context.Context, kubeconfig string, within, poll time.Duration) (string, error) {
	return waitHealthy(ctx, within, poll, func(ctx context.Context) (string, error) {
		return probeCWE(ctx, kubeconfig)
	})
}

// waitHealthy is ProbeCWE's retry, with the probe as a parameter so the timing
// can be tested without an API server. Unexported: the injection is this
// package's to use, not something a caller should have to supply.
func waitHealthy(ctx context.Context, within, poll time.Duration, probe func(context.Context) (string, error)) (string, error) {
	if within <= 0 {
		within = defaultReadyTimeout
	}
	if poll <= 0 {
		poll = defaultPollInterval
	}
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		version, err := probe(ctx)
		if err == nil {
			return version, nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("CWE did not become healthy within %s: %w", within, err)
		case <-ticker.C:
		}
	}
}

var errUnhealthy = errors.New("CWE unhealthy")

func probeCWE(ctx context.Context, kubeconfig string) (string, error) {
	restConfig, err := kube.RESTConfig(kubeconfig, "")
	if err != nil {
		return "", err
	}
	forwarder, err := kube.NewServiceForwarder(restConfig, cwe.Namespace(), cwe.Service(), cwe.Port())
	if err != nil {
		return "", err
	}
	defer forwarder.Close()

	status, body, err := forwarder.Get(ctx, "/healthz")
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("%w: /healthz returned %d: %s", errUnhealthy, status, body)
	}
	status, body, err = forwarder.Get(ctx, "/api/version")
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("%w: /api/version returned %d: %s", errUnhealthy, status, body)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", fmt.Errorf("decode /api/version: %w", err)
	}
	return v.Version, nil
}
