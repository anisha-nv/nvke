// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package clusterreachability observes tenant API access from the management plane.
package clusterreachability

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dsx-ai-factory/nke/controller/internal/capi"
	"github.com/dsx-ai-factory/nke/controller/internal/controllers/tenantaccess"
	"github.com/dsx-ai-factory/nke/controller/internal/db"
	"github.com/dsx-ai-factory/nke/controller/internal/kube"
)

const (
	credentialTimeout = 5 * time.Second
	requestTimeout    = 5 * time.Second
	spireExecCommand  = "/opt/spiffe/bin/k8s-spiffe-workload-jwt-exec-auth"
	// The controller image supplies /bin/sh. The fixed wrapper replaces itself
	// with the helper and discards helper diagnostics; client-go otherwise sends
	// exchange response bodies and credential-bearing URLs to process stderr.
	quietExecScript = `exec "$@" 2>/dev/null`
)

// Observation describes one completed tenant API access check without sensitive diagnostics.
type Observation struct {
	Status  db.ConditionStatus
	Message string
}

// Prober checks tenant access. An error means no observation was completed and
// callers must preserve the previous condition, including on cancellation.
type Prober interface {
	Probe(ctx context.Context, cluster db.Cluster) (Observation, error)
}

type accessProber struct {
	reader client.Reader
	verify func(context.Context, *rest.Config, time.Duration) error
}

// NewProber reads current published access from the management cluster for each check.
func NewProber(reader client.Reader) Prober {
	return &accessProber{reader: reader, verify: kube.VerifyRESTConfig}
}

func (p *accessProber) Probe(ctx context.Context, cluster db.Cluster) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	attempt, cancel := context.WithTimeout(ctx, credentialTimeout+requestTimeout)
	defer cancel()
	var secret corev1.Secret
	err := p.reader.Get(attempt, types.NamespacedName{
		Namespace: tenantaccess.SecretsNamespace, Name: tenantaccess.KubeconfigSecretName(cluster.Name),
	}, &secret)
	if ctx.Err() != nil {
		return Observation{}, ctx.Err()
	}
	if err != nil {
		if apierrors.IsNotFound(err) {
			return unknown("Tenant access is not available"), nil
		}
		// Management errors may contain request/credential details. The framework
		// logs this safe error once, without leaking the reader's diagnostic.
		return Observation{}, errors.New("failed to read tenant access from management cluster")
	}
	if capi.ValidateSecretOwnership(&secret, cluster) != nil {
		return unknown("Tenant access ownership does not match"), nil
	}
	data := secret.Data[tenantaccess.KubeconfigSecretKey]
	if len(data) == 0 {
		return unknown("Tenant access is not available"), nil
	}
	cfg, err := kube.NewRESTConfigFromKubeconfig(data)
	if err != nil {
		return unknown("Tenant access configuration is invalid"), nil
	}
	cfg, err = prepareConfig(cfg)
	if err != nil {
		return unknown("Tenant access configuration is invalid"), nil
	}
	err = p.verify(attempt, cfg, requestTimeout)
	if ctx.Err() != nil {
		return Observation{}, ctx.Err()
	}
	if err != nil {
		return failedObservation(err), nil
	}
	return Observation{Status: db.ConditionStatusTrue, Message: "Kubernetes API is reachable"}, nil
}

func unknown(message string) Observation {
	return Observation{Status: db.ConditionStatusUnknown, Message: message}
}

func prepareConfig(original *rest.Config) (*rest.Config, error) {
	cfg := rest.CopyConfig(original)
	cfg.Timeout = requestTimeout
	// Published static credentials are embedded. Do not load local files or
	// invoke auth-provider plugins supplied through a kubeconfig Secret.
	if cfg.AuthProvider != nil || cfg.BearerTokenFile != "" || cfg.CertFile != "" || cfg.KeyFile != "" || cfg.CAFile != "" {
		return nil, errors.New("unsupported tenant access configuration")
	}
	if cfg.ExecProvider == nil {
		return cfg, nil
	}
	exec := cfg.ExecProvider.DeepCopy()
	if exec.Command != spireExecCommand || exec.APIVersion != "client.authentication.k8s.io/v1" ||
		exec.InteractiveMode != clientcmdapi.NeverExecInteractiveMode || len(exec.Args) != 0 ||
		exec.ProvideClusterInfo || exec.Config != nil || !publishedExecEnvironment(exec.Env) || cfg.BearerToken != "" || len(cfg.CertData) != 0 ||
		len(cfg.KeyData) != 0 || cfg.Username != "" || cfg.Password != "" {
		return nil, errors.New("unsupported tenant exec configuration")
	}
	// client-go invokes exec auth without CommandContext. The pinned helper's
	// own deadline bounds credential fetch and token exchange, including refresh.
	exec.Command = "/bin/sh"
	exec.Args = []string{"-c", quietExecScript, "reachability-probe", spireExecCommand, "-timeout=" + credentialTimeout.String()}
	cfg.ExecProvider = exec
	return cfg, nil
}

func publishedExecEnvironment(env []clientcmdapi.ExecEnvVar) bool {
	seen := make(map[string]bool, len(env))
	for _, entry := range env {
		switch entry.Name {
		case "SPIFFE_ENDPOINT_SOCKET", "SPIFFE_JWT_AUDIENCE", "SPIFFE_JWT_EXCHANGE_ENDPOINT":
			if seen[entry.Name] || entry.Value == "" {
				return false
			}
			seen[entry.Name] = true
		default:
			return false
		}
	}
	return seen["SPIFFE_ENDPOINT_SOCKET"] && seen["SPIFFE_JWT_AUDIENCE"]
}

func failedObservation(err error) Observation {
	message := "Kubernetes API probe failed"
	var networkError net.Error
	var tlsError *tls.CertificateVerificationError
	switch {
	case apierrors.IsUnauthorized(err), apierrors.IsForbidden(err):
		message = "Kubernetes API access was denied"
	case errors.As(err, &tlsError):
		message = "Kubernetes API TLS verification failed"
	case errors.Is(err, context.DeadlineExceeded):
		message = "Kubernetes API request timed out"
	case errors.As(err, &networkError):
		if networkError.Timeout() {
			message = "Kubernetes API request timed out"
		} else {
			message = "Kubernetes API connection failed"
		}
	}
	return Observation{Status: db.ConditionStatusFalse, Message: message}
}
