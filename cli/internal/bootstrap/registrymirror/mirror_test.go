// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package registrymirror

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func declared() *Mirror {
	return &Mirror{
		Endpoint: "mirror.internal:5000",
		OCINamespaces: []OCINamespace{
			{Registry: "docker.io"},
			{Registry: "ghcr.io", Namespace: "ghcr"},
		},
		ChartsNamespace: "charts",
	}
}

func TestProblems_Valid(t *testing.T) {
	if p := declared().Problems(); len(p) != 0 {
		t.Fatalf("a valid mirror reported problems: %v", p)
	}
}

// TestProblems_EachRuleHasADistinctMessage drives every structural rule and
// checks the message names the field, so an operator can fix the file without
// reading this package.
func TestProblems_EachRuleHasADistinctMessage(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate  func(*Mirror)
		wantSub string
	}{
		"missing endpoint":         {func(m *Mirror) { m.Endpoint = "" }, "registryMirror.endpoint is required"},
		"endpoint with path":       {func(m *Mirror) { m.Endpoint = "mirror.internal/nke" }, "registryMirror.endpoint"},
		"endpoint with quote":      {func(m *Mirror) { m.Endpoint = `m'x` }, "registryMirror.endpoint"},
		"namespace with colon":     {func(m *Mirror) { m.OCINamespaces[1].Namespace = "a:b" }, "registryMirror.ociNamespaces[1].namespace"},
		"namespace with quote":     {func(m *Mirror) { m.OCINamespaces[1].Namespace = `a"b` }, "registryMirror.ociNamespaces[1].namespace"},
		"namespace with backslash": {func(m *Mirror) { m.OCINamespaces[1].Namespace = `a\b` }, "registryMirror.ociNamespaces[1].namespace"},
		"namespace uppercase":      {func(m *Mirror) { m.OCINamespaces[1].Namespace = "Ghcr" }, "registryMirror.ociNamespaces[1].namespace"},
		"missing registry":         {func(m *Mirror) { m.OCINamespaces[0].Registry = "" }, "registryMirror.ociNamespaces[0].registry is required"},
		"duplicate registry via alias": {func(m *Mirror) {
			m.OCINamespaces = append(m.OCINamespaces, OCINamespace{Registry: "registry-1.docker.io"})
		}, "docker.io 2 times"},
		"charts namespace bad":      {func(m *Mirror) { m.ChartsNamespace = "charts:v1" }, "registryMirror.chartsNamespace"},
		"endpoint is a path":        {func(m *Mirror) { m.Endpoint = ".." }, "registryMirror.endpoint"},
		"endpoint dot with port":    {func(m *Mirror) { m.Endpoint = "..:5000" }, "registryMirror.endpoint"},
		"registry is a path":        {func(m *Mirror) { m.OCINamespaces[0].Registry = "." }, "registryMirror.ociNamespaces[0].registry"},
		"plainHTTP and skip verify": {func(m *Mirror) { m.PlainHTTP = true; m.InsecureSkipTLSVerify = true }, "cannot both be set"},
		"plainHTTP with CA":         {func(m *Mirror) { m.PlainHTTP = true; m.CACertFile = "/x.crt" }, "only used over https"},
	} {
		t.Run(name, func(t *testing.T) {
			m := declared()
			tc.mutate(m)
			problems := strings.Join(m.Problems(), "\n")
			if problems == "" {
				t.Fatalf("Problems accepted invalid input; want a problem mentioning %q", tc.wantSub)
			}
			if !strings.Contains(problems, tc.wantSub) {
				t.Errorf("problems %q do not mention %q", problems, tc.wantSub)
			}
		})
	}
}

// TestEndpointOnlyIsAValidDeclaration: the layout is a convention, so the
// minimum declaration is the endpoint alone.
func TestEndpointOnlyIsAValidDeclaration(t *testing.T) {
	m := &Mirror{Endpoint: "registry.customer.internal"}
	if p := m.Problems(); len(p) != 0 {
		t.Fatalf("endpoint-only mirror reported problems: %v", p)
	}
	if got := m.ImageLocation("ghcr.io/spiffe/spire-server:1.15.3"); got != "registry.customer.internal/ghcr.io/spiffe/spire-server:1.15.3" {
		t.Errorf("convention image location = %q", got)
	}
	if got := m.ImageLocation("docker.io/library/postgres:18.6"); got != "registry.customer.internal/docker.io/library/postgres:18.6" {
		t.Errorf("convention docker.io location = %q", got)
	}
	if got := m.ChartsPrefix(); got != "oci://registry.customer.internal/charts" {
		t.Errorf("convention charts prefix = %q", got)
	}
}

// TestNamespace_OverridesAreCaseInsensitive: registry hosts are
// case-insensitive, and an override written in upper case must still match
// the bundle's references.
func TestNamespace_OverridesAreCaseInsensitive(t *testing.T) {
	m := &Mirror{Endpoint: "m:5000", OCINamespaces: []OCINamespace{{Registry: "docker.io"}, {Registry: "GHCR.IO", Namespace: "ghcr"}}}
	for registry, want := range map[string]string{
		"docker.io": "m:5000", "registry-1.docker.io": "m:5000", "ghcr.io": "m:5000/ghcr", "cgr.dev": "m:5000/cgr.dev",
	} {
		if got := m.NamespacePrefix(registry); got != want {
			t.Errorf("NamespacePrefix(%q) = %q, want %q", registry, got, want)
		}
	}
}

func TestExpandRegistries(t *testing.T) {
	got := ExpandRegistries([]string{"ghcr.io", "DOCKER.IO", "registry-1.docker.io", "ghcr.io"})
	want := []string{"docker.io", "ghcr.io", "registry-1.docker.io"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExpandRegistries = %v, want %v", got, want)
	}
}

func TestCheckCredentialTransport(t *testing.T) {
	m := declared()
	m.PlainHTTP = true
	if err := m.CheckCredentialTransport(false); err != nil {
		t.Errorf("no credential, plain http: %v", err)
	}
	if err := m.CheckCredentialTransport(true); err == nil || !strings.Contains(err.Error(), "clear text") {
		t.Errorf("credential over plain http to a remote host must be refused; got %v", err)
	}
	m.Endpoint = "localhost:5000"
	if err := m.CheckCredentialTransport(true); err != nil {
		t.Errorf("loopback is allowed: %v", err)
	}
	m.Endpoint = "127.0.0.1:5000"
	if err := m.CheckCredentialTransport(true); err != nil {
		t.Errorf("loopback is allowed: %v", err)
	}
}

func TestCheckCACertFile(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(cert, selfSignedPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckCACertFile(cert); err != nil {
		t.Errorf("a certificate was rejected: %v", err)
	}

	key := filepath.Join(dir, "ca.key")
	if err := os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("x")}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckCACertFile(key); err == nil || !strings.Contains(err.Error(), "RSA PRIVATE KEY") {
		t.Errorf("a key file must be rejected naming the block type; got %v", err)
	}

	empty := filepath.Join(dir, "empty.crt")
	if err := os.WriteFile(empty, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckCACertFile(empty); err == nil || !strings.Contains(err.Error(), "no PEM CERTIFICATE") {
		t.Errorf("a non-PEM file must be rejected; got %v", err)
	}
	if err := CheckCACertFile(filepath.Join(dir, "missing.crt")); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Errorf("a missing file must be rejected; got %v", err)
	}
}

func TestImageLocation(t *testing.T) {
	m := declared()
	for in, want := range map[string]string{
		"docker.io/temporalio/server:1.30.3":                    "mirror.internal:5000/temporalio/server:1.30.3",
		"docker.io/library/postgres:18.6":                       "mirror.internal:5000/library/postgres:18.6",
		"ghcr.io/spiffe/spire-server:1.15.3":                    "mirror.internal:5000/ghcr/spiffe/spire-server:1.15.3",
		"ghcr.io/spiffe/spire-server:1.15.3@sha256:abc":         "mirror.internal:5000/ghcr/spiffe/spire-server:1.15.3@sha256:abc",
		"cgr.dev/chainguard/bash:latest@sha256:abc":             "mirror.internal:5000/cgr.dev/chainguard/bash:latest@sha256:abc", // no override: convention
		"registry.k8s.io/sig-storage/csi-node-driver-registrar": "mirror.internal:5000/registry.k8s.io/sig-storage/csi-node-driver-registrar",
		"cwe-controller:local":                                  "cwe-controller:local", // hostless: not a registry ref
		"../charts/x":                                           "../charts/x",
		"mirror.internal:5000/nke/controller:1.4.0":             "mirror.internal:5000/nke/controller:1.4.0", // already on the mirror
		"MIRROR.internal:5000/nke/controller:1.4.0":             "MIRROR.internal:5000/nke/controller:1.4.0", // hosts are case-insensitive
	} {
		if got := m.ImageLocation(in); got != want {
			t.Errorf("ImageLocation(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIsSelf: the mirror recognises its own host, whatever the case, and no
// other.
func TestIsSelf(t *testing.T) {
	m := declared()
	for host, want := range map[string]bool{
		"mirror.internal:5000": true,
		"MIRROR.INTERNAL:5000": true,
		"mirror.internal":      false, // the port is part of the host
		"ghcr.io":              false,
	} {
		if got := m.IsSelf(host); got != want {
			t.Errorf("IsSelf(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestChartLocation(t *testing.T) {
	m := declared()
	m.ChartsNamespace = "helm/nke"
	got := m.ChartLocation("temporal", "0.74.0")
	want := []string{"oci://mirror.internal:5000/helm/nke/temporal", "--version", "0.74.0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("overridden ChartLocation = %v, want %v", got, want)
	}
	m.ChartsNamespace = ""
	got = m.ChartLocation("temporal", "0.74.0")
	want = []string{"oci://mirror.internal:5000/charts/temporal", "--version", "0.74.0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("convention ChartLocation = %v, want %v", got, want)
	}
}

func TestParseImage(t *testing.T) {
	for in, want := range map[string]Image{
		"docker.io/temporalio/server:1.30.3":                 {Registry: "docker.io", Path: "temporalio/server", Tag: "1.30.3"},
		"temporalio/server:1.30.3":                           {Registry: "docker.io", Path: "temporalio/server", Tag: "1.30.3"},
		"docker.io/busybox:1.37.0-uclibc":                    {Registry: "docker.io", Path: "library/busybox", Tag: "1.37.0-uclibc"},
		"registry-1.docker.io/library/postgres:18.6":         {Registry: "docker.io", Path: "library/postgres", Tag: "18.6"},
		"localhost:5000/x:1":                                 {Registry: "localhost:5000", Path: "x", Tag: "1"},
		"host:5000/x@sha256:aa":                              {Registry: "host:5000", Path: "x", Digest: "sha256:aa"},
		"ghcr.io/spiffe/spire-server:v0.1.1-rc.2@sha256:aa":  {Registry: "ghcr.io", Path: "spiffe/spire-server", Tag: "v0.1.1-rc.2", Digest: "sha256:aa"},
		"oci://ghcr.io/spiffe/spire-server:1.15.3":           {Registry: "ghcr.io", Path: "spiffe/spire-server", Tag: "1.15.3"},
		"cgr.dev/chainguard/bash:latest@sha256:90041f375e30": {Registry: "cgr.dev", Path: "chainguard/bash", Tag: "latest", Digest: "sha256:90041f375e30"},
	} {
		got, ok := ParseImage(in)
		if !ok {
			t.Errorf("ParseImage(%q) reported not-a-reference", in)
			continue
		}
		if got != want {
			t.Errorf("ParseImage(%q) = %+v, want %+v", in, got, want)
		}
	}
	for _, in := range []string{"cwe-controller:local", "./rel", "/abs/path", "../x", "https://example.com/x", "", "x@md5:1"} {
		if got, ok := ParseImage(in); ok {
			t.Errorf("ParseImage(%q) = %+v; want not-a-reference", in, got)
		}
	}
}

// selfSignedPEM makes a throwaway certificate for the CA file checks.
func selfSignedPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
