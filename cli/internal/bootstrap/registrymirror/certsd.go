// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package registrymirror

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

const (
	// NodeCertsDir is where the Kind node mounts the rendered tree.
	NodeCertsDir = "/etc/containerd/certs.d"

	hostsFile  = "hosts.toml"
	caFile     = "ca.crt"
	dirMode    = 0o700
	fileMode   = 0o600
	redactedCA = "sha256:%x\n"
)

// hostsTemplate renders one hosts.toml.
//
//go:embed hosts.toml.tmpl
var hostsTemplateText string

var hostsTemplate = template.Must(template.New(hostsFile).Parse(hostsTemplateText))

// Auth is the credential for the Basic authorization header. String hides it.
type Auth struct {
	Username string
	Key      string
}

// String keeps the credential out of any %v formatting.
func (Auth) String() string { return "Auth{***}" }

func (a *Auth) header() string {
	if a == nil || a.Key == "" {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(a.Username + ":" + a.Key))
}

// hostsParams is what the template needs for one registry directory.
type hostsParams struct {
	HostURL       string
	CAPath        string
	SkipVerify    bool
	Authorization string
}

// RenderCertsD writes the containerd certs.d tree for m into dir: one
// hosts.toml per upstream registry, one for the mirror host, and the CA once.
// No entry falls back to the upstream registry. Files are 0600.
func RenderCertsD(dir string, m *Mirror, registries []string, auth *Auth) error {
	if m == nil {
		return fmt.Errorf("registrymirror: RenderCertsD needs a mirror")
	}
	hosts := ExpandRegistries(registries)
	// Each host becomes a directory name.
	for _, segment := range append([]string{m.Endpoint}, hosts...) {
		if !safePathSegment(segment) {
			return fmt.Errorf("registrymirror: %q is not usable as a certs.d directory name", segment)
		}
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("registrymirror: creating %s: %w", dir, err)
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		return fmt.Errorf("registrymirror: securing %s: %w", dir, err)
	}

	caPath := ""
	if m.CACertFile != "" {
		pemBytes, err := os.ReadFile(m.CACertFile)
		if err != nil {
			return fmt.Errorf("registrymirror: reading %s.caCertFile: %w", FieldPrefix, err)
		}
		if err := writeFile(filepath.Join(dir, m.Endpoint, caFile), pemBytes); err != nil {
			return err
		}
		caPath = NodeCertsDir + "/" + m.Endpoint + "/" + caFile
	}

	authorization := auth.header()
	mirrorURL := m.Scheme() + "://" + m.Endpoint

	// The mirror host itself gets the same CA and credential.
	self, err := render(hostsParams{
		HostURL: mirrorURL + "/v2", CAPath: caPath,
		SkipVerify: m.InsecureSkipTLSVerify, Authorization: authorization,
	})
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, m.Endpoint, hostsFile), self); err != nil {
		return err
	}

	for _, registry := range hosts {
		content, err := render(hostsParams{
			HostURL:       mirrorURL + "/v2" + slashPrefix(m.Namespace(registry)),
			CAPath:        caPath,
			SkipVerify:    m.InsecureSkipTLSVerify,
			Authorization: authorization,
		})
		if err != nil {
			return err
		}
		if err := writeFile(filepath.Join(dir, registry, hostsFile), content); err != nil {
			return err
		}
	}
	return nil
}

// StripAuthorization rewrites the tree without a credential.
func StripAuthorization(dir string, m *Mirror, registries []string) error {
	return RenderCertsD(dir, m, registries, nil)
}

var authorizationLine = regexp.MustCompile(`(?m)^(\s*authorization = "Basic )[^"]*"`)

// RedactedCopy copies the tree for diagnostics. It masks each authorization
// header and replaces each ca.crt with its SHA-256.
func RedactedCopy(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		switch d.Name() {
		case caFile:
			content = fmt.Appendf(nil, redactedCA, sha256.Sum256(content))
		case hostsFile:
			content = authorizationLine.ReplaceAll(content, []byte(`${1}***"`))
		}
		return os.WriteFile(target, content, 0o640) //nolint:gosec // G306: diagnostics are meant to be read
	})
}

func render(params hostsParams) ([]byte, error) {
	var buf bytes.Buffer
	if err := hostsTemplate.Execute(&buf, params); err != nil {
		return nil, fmt.Errorf("registrymirror: rendering %s: %w", hostsFile, err)
	}
	return buf.Bytes(), nil
}

func writeFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return fmt.Errorf("registrymirror: creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, fileMode); err != nil {
		return fmt.Errorf("registrymirror: writing %s: %w", path, err)
	}
	return os.Chmod(path, fileMode)
}

// safePathSegment reports whether s is a single, safe path element.
func safePathSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`) && filepath.Clean(s) == s
}

func slashPrefix(namespace string) string {
	if namespace == "" {
		return ""
	}
	return "/" + namespace
}
