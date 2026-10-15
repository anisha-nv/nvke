// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package config loads explicit configuration for the evaluation cluster API.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/dsx-ai-factory/nvke/management-engine/internal/public/tenant/limits"
)

// Options contains only process-owned configuration, never request routing.
type Options struct {
	Kubeconfig       string          // An explicit file selects the datastore independently of ambient Kubernetes settings.
	Namespace        string          // All requests share this process-owned scope until authenticated tenant routing exists.
	PageTokenKeyFile string          // A mounted shared key lets replicas validate each other's pagination tokens.
	HTTPAddress      string          // The REST listener can be exposed independently from RPC and probes.
	GRPCAddress      string          // Native clients and the local HTTP gateway reach the same RPC controls.
	HealthAddress    string          // Probes remain separate from application traffic and authentication.
	EvaluationNoAuth bool            // Startup requires explicit acknowledgement that caller authentication is unavailable.
	Context          string          // An optional context overrides current-context within the explicit kubeconfig only.
	Capacity         limits.Capacity // Deployment-owned limits keep connection and request budgets independent.
}

// Loaded is validated startup configuration shared by all replicas.
type Loaded struct {
	Options
	Storage      *rest.Config // Verified transport settings prevent ambient credentials or proxies changing the target.
	PageTokenKey []byte       // Raw key material derives encryption for opaque pagination state.
	DatastoreID  string       // Stable endpoint identity prevents pagination tokens crossing datastore boundaries.
}

// Load requires an explicitly selected, verified HTTPS datastore and identity.
func Load(options Options) (*Loaded, error) {
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	storage, err := loadStorage(options.Kubeconfig, options.Context)
	if err != nil {
		return nil, err
	}
	key, err := os.ReadFile(options.PageTokenKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read page-token encryption key: %w", err)
	}
	if len(key) < limits.PageTokenKeyMinBytes || len(key) > limits.PageTokenKeyMaxBytes {
		return nil, fmt.Errorf("page-token encryption key must contain %d to %d raw bytes", limits.PageTokenKeyMinBytes, limits.PageTokenKeyMaxBytes)
	}
	digest := sha256.Sum256([]byte(storage.Host))
	return &Loaded{Options: options, Storage: storage, PageTokenKey: key, DatastoreID: hex.EncodeToString(digest[:])}, nil
}

// validateOptions requires explicit evaluation mode, scope and file paths before reading credentials.
func validateOptions(options Options) error {
	if !options.EvaluationNoAuth {
		return fmt.Errorf("authentication is not implemented; explicitly enable --evaluation-no-auth for isolated evaluation")
	}
	if !filepath.IsAbs(options.Kubeconfig) || !filepath.IsAbs(options.PageTokenKeyFile) {
		return fmt.Errorf("kubeconfig and page-token-key-file must be explicit absolute paths")
	}
	if len(validation.IsDNS1123Label(options.Namespace)) != 0 {
		return fmt.Errorf("namespace must be a DNS label")
	}
	addresses := []string{options.HTTPAddress, options.GRPCAddress, options.HealthAddress}
	for _, address := range addresses {
		if _, _, err := net.SplitHostPort(address); err != nil {
			return fmt.Errorf("invalid listener address: %w", err)
		}
	}
	return options.Capacity.Validate()
}

// loadStorage accepts a verified HTTPS origin and explicit identity without executing credential plugins.
func loadStorage(path, contextName string) (*rest.Config, error) {
	raw, err := clientcmd.LoadFromFile(path)
	if err != nil {
		// Parsing errors can contain fragments of credential-bearing input.
		return nil, fmt.Errorf("cannot read or parse the explicit datastore kubeconfig")
	}
	if err := clientcmd.ResolveLocalPaths(raw); err != nil {
		return nil, fmt.Errorf("cannot resolve paths in the explicit datastore kubeconfig")
	}
	if contextName == "" {
		contextName = raw.CurrentContext
	}
	selected := raw.Contexts[contextName]
	if selected == nil {
		return nil, fmt.Errorf("datastore kubeconfig must select an existing context")
	}
	cluster := raw.Clusters[selected.Cluster]
	identity := raw.AuthInfos[selected.AuthInfo]
	if cluster == nil || identity == nil {
		return nil, fmt.Errorf("datastore context must select an explicit cluster and identity")
	}
	endpoint, err := url.Parse(cluster.Server)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, fmt.Errorf("datastore server must be an HTTPS origin without credentials, query, or path")
	}
	if cluster.InsecureSkipTLSVerify || cluster.ProxyURL != "" ||
		(cluster.CertificateAuthority == "" && len(cluster.CertificateAuthorityData) == 0) {
		return nil, fmt.Errorf("datastore requires an explicit CA, verified TLS, and direct access without a proxy")
	}
	if err := validateIdentity(identity); err != nil {
		return nil, err
	}
	storage, err := clientcmd.NewNonInteractiveClientConfig(*raw, contextName, nil, nil).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("cannot construct the explicit datastore client configuration")
	}
	storage.Host = endpoint.Scheme + "://" + endpoint.Host
	storage.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	storage.Timeout = limits.StorageTimeout
	storage.QPS = 20
	storage.Burst = 40
	storage.UserAgent = "tenant-api"
	return storage, nil
}

// validateIdentity permits one mounted token or certificate pair so authentication has no ambient fallback.
func validateIdentity(identity *clientcmdapi.AuthInfo) error {
	if identity.Exec != nil || identity.AuthProvider != nil || identity.Username != "" || identity.Password != "" ||
		identity.Impersonate != "" || identity.ImpersonateUID != "" || len(identity.ImpersonateGroups) != 0 || len(identity.ImpersonateUserExtra) != 0 {
		return fmt.Errorf("datastore exec, auth-provider, basic authentication, and impersonation are unsupported")
	}
	if identity.Token != "" {
		return fmt.Errorf("datastore bearer credentials must use a mounted tokenFile")
	}
	hasToken := identity.TokenFile != ""
	hasCertificate := identity.ClientCertificate != "" || len(identity.ClientCertificateData) != 0
	hasKey := identity.ClientKey != "" || len(identity.ClientKeyData) != 0
	if hasCertificate != hasKey || hasToken == hasCertificate {
		return fmt.Errorf("datastore identity requires exactly one tokenFile or client certificate/key pair")
	}
	return nil
}
