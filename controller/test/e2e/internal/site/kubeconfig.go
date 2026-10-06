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
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dsx-ai-factory/nke/controller/test/e2e/internal/cwe"
	"github.com/dsx-ai-factory/nke/controller/test/e2e/internal/kube"
)

// forwardKubeconfig fetches the admin kubeconfig from the node, forwards its
// API server port, and writes a copy that reaches the cluster through the
// forward.
func (b bootstrap) forwardKubeconfig(ctx context.Context, outputDir string) (string, error) {
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
	if outputDir == "" {
		if outputDir, err = os.MkdirTemp("", "nke-e2e-site-"); err != nil {
			return "", err
		}
	}
	out := filepath.Join(outputDir, b.candidate.SiteName+".kubeconfig")
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

// waitForCWE polls until CWE answers /healthz, then returns its version.
func waitForCWE(ctx context.Context, opts Options, kubeconfig string) (string, error) {
	deadline, cancel := context.WithTimeout(ctx, opts.ReadyTimeout)
	defer cancel()
	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()
	for {
		version, err := opts.Probe(deadline, kubeconfig)
		if err == nil {
			return version, nil
		}
		select {
		case <-deadline.Done():
			return "", fmt.Errorf("CWE did not become healthy within %s: %w\n%s",
				opts.ReadyTimeout, err, diagnoseCWE(ctx, kubeconfig))
		case <-ticker.C:
		}
	}
}

// diagnoseCWE reports pod and event state from the CWE namespace once the
// probe loop gives up, so a timeout says whether CWE was crash-looping,
// still starting, or never scheduled, rather than only that it did not
// answer in time.
//
// Best-effort and never returns an error itself: the probe's own failure is
// the finding, and losing this context must not replace it with a second,
// less useful one. kubeconfig already reaches the cluster through the
// session's forward at this point, same as the probe that just failed.
func diagnoseCWE(ctx context.Context, kubeconfig string) string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	restConfig, err := kube.RESTConfig(kubeconfig, "")
	if err != nil {
		return fmt.Sprintf("(could not collect CWE pod diagnostics: %v)", err)
	}
	c, err := kube.RuntimeClient(restConfig)
	if err != nil {
		return fmt.Sprintf("(could not collect CWE pod diagnostics: %v)", err)
	}

	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(cwe.Namespace())); err != nil {
		return fmt.Sprintf("(could not list pods in %s: %v)", cwe.Namespace(), err)
	}
	var events corev1.EventList
	if err := c.List(ctx, &events, client.InNamespace(cwe.Namespace())); err != nil {
		return fmt.Sprintf("(could not list events in %s: %v)", cwe.Namespace(), err)
	}
	sort.Slice(events.Items, func(i, j int) bool {
		return events.Items[i].LastTimestamp.Before(&events.Items[j].LastTimestamp)
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "pods in %s:\n", cwe.Namespace())
	if len(pods.Items) == 0 {
		fmt.Fprintf(&sb, "  (none)\n")
	}
	for _, pod := range pods.Items {
		fmt.Fprintf(&sb, "  %s phase=%s\n", pod.Name, pod.Status.Phase)
		for _, cs := range pod.Status.ContainerStatuses {
			state, detail := "running", ""
			switch {
			case cs.State.Waiting != nil:
				state, detail = "waiting", cs.State.Waiting.Reason+": "+cs.State.Waiting.Message
			case cs.State.Terminated != nil:
				state, detail = "terminated", cs.State.Terminated.Reason+": "+cs.State.Terminated.Message
			}
			fmt.Fprintf(&sb, "    %s ready=%t restarts=%d state=%s %s\n",
				cs.Name, cs.Ready, cs.RestartCount, state, detail)
		}
	}
	fmt.Fprintf(&sb, "events in %s (most recent last):\n", cwe.Namespace())
	if len(events.Items) == 0 {
		fmt.Fprintf(&sb, "  (none)\n")
	}
	for _, e := range events.Items {
		fmt.Fprintf(&sb, "  %s %s/%s %s: %s\n",
			e.LastTimestamp.Format(time.RFC3339), e.InvolvedObject.Kind, e.InvolvedObject.Name, e.Reason, e.Message)
	}
	return sb.String()
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
