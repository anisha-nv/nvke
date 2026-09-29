// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package clusterreachability

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/dsx-ai-factory/nke/controller/internal/constants"
	"github.com/dsx-ai-factory/nke/controller/internal/controllers/tenantaccess"
	"github.com/dsx-ai-factory/nke/controller/internal/db"
	"github.com/dsx-ai-factory/nke/controller/internal/kube"
)

func testCluster() db.Cluster { return db.Cluster{ID: uuid.New(), Name: "probe-test"} }

func accessSecret(t *testing.T, endpoint, token string) *corev1.Secret {
	t.Helper()
	cfg := clientcmdapi.Config{
		Clusters:       map[string]*clientcmdapi.Cluster{"tenant": {Server: endpoint}},
		AuthInfos:      map[string]*clientcmdapi.AuthInfo{"cwe": {Token: token}},
		Contexts:       map[string]*clientcmdapi.Context{"tenant": {Cluster: "tenant", AuthInfo: "cwe"}},
		CurrentContext: "tenant",
	}
	data, err := clientcmd.Write(cfg)
	require.NoError(t, err)
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: tenantaccess.SecretsNamespace, Name: tenantaccess.KubeconfigSecretName("probe-test")},
		Data:       map[string][]byte{tenantaccess.KubeconfigSecretKey: data},
	}
}

func managementClient(t *testing.T, secrets ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(secrets...).Build()
}

func TestReachabilityObservation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    int
		want    db.ConditionStatus
		message string
	}{
		{"success", http.StatusOK, db.ConditionStatusTrue, "Kubernetes API is reachable"},
		{"unauthorized", http.StatusUnauthorized, db.ConditionStatusFalse, "Kubernetes API access was denied"},
		{"forbidden", http.StatusForbidden, db.ConditionStatusFalse, "Kubernetes API access was denied"},
		{"server failure", http.StatusInternalServerError, db.ConditionStatusFalse, "Kubernetes API probe failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/version", r.URL.Path)
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte("SENTINEL-response-body"))
			}))
			defer server.Close()
			observation, err := NewProber(managementClient(t, accessSecret(t, server.URL, "example-token"))).Probe(t.Context(), testCluster())
			require.NoError(t, err)
			require.Equal(t, Observation{Status: tc.want, Message: tc.message}, observation)
		})
	}
}

func TestReachabilityUnavailableAccess(t *testing.T) {
	for _, name := range []string{"missing", "empty", "malformed", "wrong owner", "unsupported exec"} {
		t.Run(name, func(t *testing.T) {
			secret := accessSecret(t, "https://example.invalid", "example-token")
			switch name {
			case "empty":
				secret.Data = nil
			case "malformed":
				secret.Data[tenantaccess.KubeconfigSecretKey] = []byte("SENTINEL-malformed: [")
			case "wrong owner":
				secret.Labels = map[string]string{constants.LabelKeyClusterID: uuid.NewString()}
			case "unsupported exec":
				cfg, err := clientcmd.Load(secret.Data[tenantaccess.KubeconfigSecretKey])
				require.NoError(t, err)
				cfg.AuthInfos["cwe"] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{
					Command: "/do-not-execute", APIVersion: "client.authentication.k8s.io/v1", InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
				}}
				secret.Data[tenantaccess.KubeconfigSecretKey], err = clientcmd.Write(*cfg)
				require.NoError(t, err)
			}
			var reader client.Reader = managementClient(t, secret)
			if name == "missing" {
				reader = managementClient(t)
			}
			observation, err := NewProber(reader).Probe(t.Context(), testCluster())
			require.NoError(t, err)
			require.Equal(t, db.ConditionStatusUnknown, observation.Status)
			require.NotContains(t, observation.Message, "SENTINEL")
		})
	}
}

type failingReader struct {
	client.Reader
	err error
}

func (r failingReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return r.err
}

func TestProbeManagementReadErrors(t *testing.T) {
	for _, err := range []error{
		apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "SENTINEL", errors.New("SENTINEL-token")),
		errors.New("SENTINEL-transient-error"), context.DeadlineExceeded,
	} {
		observation, gotErr := NewProber(failingReader{err: err}).Probe(t.Context(), testCluster())
		require.Error(t, gotErr)
		require.Equal(t, Observation{}, observation)
		require.NotContains(t, gotErr.Error(), "SENTINEL")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	observation, err := NewProber(failingReader{err: errors.New("must not read")}).Probe(ctx, testCluster())
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, Observation{}, observation)
}

func TestProbeSecretRotation(t *testing.T) {
	var calls []string
	endpoint := func(token string) *httptest.Server {
		return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/version", r.URL.Path)
			assert.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
			calls = append(calls, token)
			w.WriteHeader(http.StatusOK)
		}))
	}
	first := endpoint("example-token-one")
	defer first.Close()
	second := endpoint("example-token-two")
	defer second.Close()
	secretFor := func(server *httptest.Server, token string) *corev1.Secret {
		secret := accessSecret(t, server.URL, token)
		cfg, err := clientcmd.Load(secret.Data[tenantaccess.KubeconfigSecretKey])
		require.NoError(t, err)
		cfg.Clusters["tenant"].CertificateAuthorityData = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
		secret.Data[tenantaccess.KubeconfigSecretKey], err = clientcmd.Write(*cfg)
		require.NoError(t, err)
		return secret
	}
	secret := secretFor(first, "example-token-one")
	reader := managementClient(t, secret)
	before := secret.DeepCopy()
	prober := NewProber(reader)
	observation, err := prober.Probe(t.Context(), testCluster())
	require.NoError(t, err)
	require.Equal(t, db.ConditionStatusTrue, observation.Status)
	stored := &corev1.Secret{}
	require.NoError(t, reader.Get(t.Context(), client.ObjectKeyFromObject(secret), stored))
	stored.Data = secretFor(second, "example-token-two").Data
	require.NoError(t, reader.Update(t.Context(), stored))
	observation, err = prober.Probe(t.Context(), testCluster())
	require.NoError(t, err)
	require.Equal(t, db.ConditionStatusTrue, observation.Status)
	require.Equal(t, []string{"example-token-one", "example-token-two"}, calls)
	require.Equal(t, before, secret)
}

func TestProbeTransportFailuresAndCancellation(t *testing.T) {
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer tlsServer.Close()
	observation, err := NewProber(managementClient(t, accessSecret(t, tlsServer.URL, ""))).Probe(t.Context(), testCluster())
	require.NoError(t, err)
	require.Equal(t, "Kubernetes API TLS verification failed", observation.Message)
	endpoint := tlsServer.URL
	tlsServer.Close()
	observation, err = NewProber(managementClient(t, accessSecret(t, endpoint, ""))).Probe(t.Context(), testCluster())
	require.NoError(t, err)
	require.Equal(t, "Kubernetes API connection failed", observation.Message)

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	prober := &accessProber{reader: managementClient(t, accessSecret(t, server.URL, "")), verify: func(ctx context.Context, cfg *rest.Config, _ time.Duration) error {
		return kube.VerifyRESTConfig(ctx, cfg, 20*time.Millisecond)
	}}
	observation, err = prober.Probe(t.Context(), testCluster())
	require.NoError(t, err)
	require.Equal(t, "Kubernetes API request timed out", observation.Message)
	ctx, cancel := context.WithCancel(t.Context())
	prober.verify = func(context.Context, *rest.Config, time.Duration) error {
		cancel()
		return errors.New("SENTINEL-credential")
	}
	observation, err = prober.Probe(ctx, testCluster())
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, Observation{}, observation)
}

func TestProbeConfigBoundsSPIFFEExec(t *testing.T) {
	original := &rest.Config{Host: "https://example.invalid", ExecProvider: &clientcmdapi.ExecConfig{
		Command: spireExecCommand, APIVersion: "client.authentication.k8s.io/v1", InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
		Env: []clientcmdapi.ExecEnvVar{{Name: "SPIFFE_ENDPOINT_SOCKET", Value: "unix:///example.sock"}, {Name: "SPIFFE_JWT_AUDIENCE", Value: "tenant"}},
	}}
	before := rest.CopyConfig(original)
	before.ExecProvider = original.ExecProvider.DeepCopy()
	cfg, err := prepareConfig(original)
	require.NoError(t, err)
	require.Equal(t, "/bin/sh", cfg.ExecProvider.Command)
	require.Equal(t, []string{"-c", quietExecScript, "reachability-probe", spireExecCommand, "-timeout=5s"}, cfg.ExecProvider.Args)
	require.Equal(t, original.ExecProvider.Env, cfg.ExecProvider.Env)
	require.Equal(t, requestTimeout, cfg.Timeout)
	require.Equal(t, before, original)
	for _, change := range []func(*clientcmdapi.ExecConfig){
		func(e *clientcmdapi.ExecConfig) { e.Command = "/bin/false" },
		func(e *clientcmdapi.ExecConfig) { e.Args = []string{"-timeout=0"} },
		func(e *clientcmdapi.ExecConfig) { e.APIVersion = "client.authentication.k8s.io/v1beta1" },
		func(e *clientcmdapi.ExecConfig) { e.InteractiveMode = clientcmdapi.AlwaysExecInteractiveMode },
	} {
		invalid := rest.CopyConfig(original)
		invalid.ExecProvider = original.ExecProvider.DeepCopy()
		change(invalid.ExecProvider)
		_, err = prepareConfig(invalid)
		require.Error(t, err)
	}
}

func TestProbeExecDoesNotExposeDiagnostics(t *testing.T) {
	// Model an exchange returning a credential-bearing error body. The wrapper
	// must preserve credential stdout but discard every byte of helper stderr.
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", quietExecScript, "reachability-probe",
		"/bin/sh", "-c", `printf 'SENTINEL-credential-response-body' >&2; printf 'credential-stdout'; exit 1`)
	output, err := cmd.CombinedOutput()
	require.Error(t, err)
	require.Equal(t, "credential-stdout", string(output))
	observation := failedObservation(errors.New("SENTINEL-credential-response-body"))
	require.Equal(t, Observation{Status: db.ConditionStatusFalse, Message: "Kubernetes API probe failed"}, observation)
}
