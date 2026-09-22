// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package registrymirror

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestRenderCertsD_Golden pins the exact bytes containerd will read. hosts.toml
// is a positional TOML document containerd parses silently, so a table nested
// under the wrong parent or a missing /v2 would pass a substring check and
// fail only at the first in-cluster pull.
func TestRenderCertsD_Golden(t *testing.T) {
	// No server line anywhere: a miss at the mirror is a failure, never a
	// pull from the upstream registry.
	const ghcrOverride = `[host."https://mirror.internal:5000/v2/ghcr"]
  capabilities = ["pull", "resolve"]
  override_path = true
`
	const dockerRoot = `[host."https://mirror.internal:5000/v2"]
  capabilities = ["pull", "resolve"]
  override_path = true
`
	const cgrConvention = `[host."https://mirror.internal:5000/v2/cgr.dev"]
  capabilities = ["pull", "resolve"]
  override_path = true
`
	const ghcrStrictCAAuth = `[host."https://mirror.internal:5000/v2/ghcr"]
  capabilities = ["pull", "resolve"]
  override_path = true
  ca = "/etc/containerd/certs.d/mirror.internal:5000/ca.crt"
  [host."https://mirror.internal:5000/v2/ghcr".header]
    authorization = "Basic dXNlcjpzZWNyZXQ="
`
	// The self entry sets override_path like every other, so its host URL
	// must carry /v2 or containerd would issue API calls at the site root.
	const selfCAAuth = `[host."https://mirror.internal:5000/v2"]
  capabilities = ["pull", "resolve"]
  override_path = true
  ca = "/etc/containerd/certs.d/mirror.internal:5000/ca.crt"
  [host."https://mirror.internal:5000/v2".header]
    authorization = "Basic dXNlcjpzZWNyZXQ="
`
	const plainSkipless = `[host."http://mirror.internal:5000/v2/ghcr"]
  capabilities = ["pull", "resolve"]
  override_path = true
`
	const skipVerify = `[host."https://mirror.internal:5000/v2/ghcr"]
  capabilities = ["pull", "resolve"]
  override_path = true
  skip_verify = true
`

	caPEM := selfSignedPEM(t)
	caPath := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		mirror     *Mirror
		registries []string
		auth       *Auth
		wantDirs   []string
		want       map[string]string // relative path → exact content
	}{
		"override, root and convention": {
			mirror:     declared(),
			registries: []string{"docker.io", "ghcr.io", "cgr.dev"},
			wantDirs:   []string{"cgr.dev", "docker.io", "ghcr.io", "mirror.internal:5000", "registry-1.docker.io"},
			want: map[string]string{
				"ghcr.io/hosts.toml":              ghcrOverride,
				"docker.io/hosts.toml":            dockerRoot,
				"registry-1.docker.io/hosts.toml": dockerRoot,
				"cgr.dev/hosts.toml":              cgrConvention,
			},
		},
		"CA and auth": {
			mirror: &Mirror{
				Endpoint:      "mirror.internal:5000",
				OCINamespaces: []OCINamespace{{Registry: "ghcr.io", Namespace: "ghcr"}},
				CACertFile:    caPath,
			},
			registries: []string{"ghcr.io"},
			auth:       &Auth{Username: "user", Key: "secret"},
			wantDirs:   []string{"ghcr.io", "mirror.internal:5000"},
			want: map[string]string{
				"ghcr.io/hosts.toml":              ghcrStrictCAAuth,
				"mirror.internal:5000/hosts.toml": selfCAAuth,
				"mirror.internal:5000/ca.crt":     string(caPEM),
			},
		},
		"plain http": {
			mirror: &Mirror{
				Endpoint:      "mirror.internal:5000",
				OCINamespaces: []OCINamespace{{Registry: "ghcr.io", Namespace: "ghcr"}},
				PlainHTTP:     true,
			},
			registries: []string{"ghcr.io"},
			wantDirs:   []string{"ghcr.io", "mirror.internal:5000"},
			want:       map[string]string{"ghcr.io/hosts.toml": plainSkipless},
		},
		"skip verify": {
			mirror: &Mirror{
				Endpoint:              "mirror.internal:5000",
				OCINamespaces:         []OCINamespace{{Registry: "ghcr.io", Namespace: "ghcr"}},
				InsecureSkipTLSVerify: true,
			},
			registries: []string{"ghcr.io"},
			wantDirs:   []string{"ghcr.io", "mirror.internal:5000"},
			want:       map[string]string{"ghcr.io/hosts.toml": skipVerify},
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "certs.d")
			if err := RenderCertsD(dir, tc.mirror, tc.registries, tc.auth); err != nil {
				t.Fatalf("RenderCertsD: %v", err)
			}
			if got := subdirs(t, dir); !reflect.DeepEqual(got, tc.wantDirs) {
				t.Errorf("directories = %v, want %v", got, tc.wantDirs)
			}
			for rel, want := range tc.want {
				got, err := os.ReadFile(filepath.Join(dir, rel))
				if err != nil {
					t.Errorf("%s: %v", rel, err)
					continue
				}
				if string(got) != want {
					t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", rel, got, want)
				}
			}
			assertModes(t, dir)
		})
	}
}

// TestStripAuthorization_KeepsRedirectDropsCredential is the retained-cluster
// path: the node keeps its redirect and CA, the host stops holding the key.
// TestRenderCertsD_RefusesPathSegments: a host that is not a single path
// element must never reach filepath.Join.
func TestRenderCertsD_RefusesPathSegments(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "certs.d")
	err := RenderCertsD(dir, &Mirror{Endpoint: ".."}, []string{"ghcr.io"}, nil)
	if err == nil {
		t.Fatal("RenderCertsD accepted endpoint \"..\"")
	}
	if _, statErr := os.Stat(filepath.Join(root, hostsFile)); statErr == nil {
		t.Error("a hosts.toml was written above the target directory")
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("RenderCertsD wrote %v before refusing", entries)
	}
}

func TestStripAuthorization_KeepsRedirectDropsCredential(t *testing.T) {
	m := declared()
	dir := filepath.Join(t.TempDir(), "certs.d")
	if err := RenderCertsD(dir, m, []string{"ghcr.io"}, &Auth{Username: "u", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "ghcr.io", hostsFile))
	if !strings.Contains(string(before), "authorization") {
		t.Fatal("fixture did not render a credential")
	}
	if err := StripAuthorization(dir, m, []string{"ghcr.io"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "ghcr.io", hostsFile))
	if strings.Contains(string(after), "authorization") {
		t.Errorf("credential survived StripAuthorization:\n%s", after)
	}
	if !strings.Contains(string(after), `[host."https://mirror.internal:5000/v2/ghcr"]`) {
		t.Errorf("redirect did not survive StripAuthorization:\n%s", after)
	}
}

func TestRedactedCopy(t *testing.T) {
	caPath := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caPath, selfSignedPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	m := declared()
	m.CACertFile = caPath
	src := filepath.Join(t.TempDir(), "certs.d")
	if err := RenderCertsD(src, m, []string{"ghcr.io"}, &Auth{Username: "u", Key: "do-not-leak"}); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "diag")
	if err := RedactedCopy(src, dst); err != nil {
		t.Fatal(err)
	}
	hosts, _ := os.ReadFile(filepath.Join(dst, "ghcr.io", hostsFile))
	if strings.Contains(string(hosts), "do-not-leak") || strings.Contains(string(hosts), "dTpkby1ub3Qt") {
		t.Errorf("credential reached the redacted copy:\n%s", hosts)
	}
	if !strings.Contains(string(hosts), `authorization = "Basic ***"`) {
		t.Errorf("redaction marker missing:\n%s", hosts)
	}
	ca, _ := os.ReadFile(filepath.Join(dst, "mirror.internal:5000", caFile))
	if !strings.HasPrefix(string(ca), "sha256:") || strings.Contains(string(ca), "BEGIN CERTIFICATE") {
		t.Errorf("ca.crt should be a fingerprint in the redacted copy:\n%s", ca)
	}
}

func TestRenderCertsD_NilMirror(t *testing.T) {
	if err := RenderCertsD(t.TempDir(), nil, nil, nil); err == nil {
		t.Error("nil mirror must be an error")
	}
}

func subdirs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func assertModes(t *testing.T, dir string) {
	t.Helper()
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		want := os.FileMode(fileMode)
		if info.IsDir() {
			want = dirMode
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
		return nil
	})
}
