// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package charts

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the public shell entrypoint: supplied packages must never invoke
// Helm, including when the archive or its expected metadata is invalid.
func TestPackageAssertionsExternalArchive(t *testing.T) {
	script, err := filepath.Abs("../../hack/chart-package-assertions.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		members map[string]string
		env     []string
		want    string
		pass    bool
	}{
		{name: "supplied package", members: map[string]string{"nke-controller/Chart.yaml": "version: 1.2.3\nappVersion: 1.2.3\n"}, pass: true},
		{name: "independent app version", members: map[string]string{"nke-controller/Chart.yaml": "version: 1.2.3\nappVersion: v1.2.3\n"}, env: []string{"CHART_PACKAGE_APP_VERSION=v1.2.3"}, pass: true},
		{name: "wrong version", members: map[string]string{"nke-controller/Chart.yaml": "version: 9.9.9\nappVersion: 1.2.3\n"}, want: "expected 1.2.3"},
		{name: "wrong app version", members: map[string]string{"nke-controller/Chart.yaml": "version: 1.2.3\nappVersion: wrong\n"}, want: "appVersion is wrong"},
		{name: "missing metadata", members: map[string]string{"nke-controller/values.yaml": "x: y\n"}},
		{name: "ambiguous roots", members: map[string]string{"one/Chart.yaml": "version: 1.2.3\n", "two/Chart.yaml": "version: 1.2.3\n"}},
		{name: "nested dependency is not root", members: map[string]string{"root/charts/dependency/Chart.yaml": "version: 1.2.3\nappVersion: 1.2.3\n"}},
		{name: "missing version expectation", env: []string{"CHART_PACKAGE_VERSION="}, want: "CHART_PACKAGE_VERSION is required"},
		{name: "missing archive", env: []string{"CHART_ARCHIVE=/no-such-chart.tgz"}, want: "readable packaged chart"},
		{name: "empty archive input", env: []string{"CHART_ARCHIVE="}, want: "readable packaged chart"},
		{name: "conflicting source", env: []string{"CHART=controller"}, want: "not both"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "supplied chart.tgz")
			var content bytes.Buffer
			gz := gzip.NewWriter(&content)
			tw := tar.NewWriter(gz)
			for name, body := range tc.members {
				if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write([]byte(body)); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(archive, content.Bytes(), 0o444); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dir, "helm-invoked")
			if err := os.WriteFile(filepath.Join(dir, "helm"), []byte("#!/bin/sh\ntouch \"$HELM_MARKER\"\nexit 99\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", script)
			for _, item := range os.Environ() {
				if !strings.HasPrefix(item, "CHART") {
					cmd.Env = append(cmd.Env, item)
				}
			}
			cmd.Env = append(cmd.Env, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "HELM_MARKER="+marker,
				"CHART_ARCHIVE="+archive, "CHART_PACKAGE_VERSION=1.2.3")
			cmd.Env = append(cmd.Env, tc.env...)
			out, runErr := cmd.CombinedOutput()
			if (runErr == nil) != tc.pass || !strings.Contains(string(out), tc.want) {
				t.Fatalf("entrypoint: %v\n%s", runErr, out)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("external mode invoked Helm")
			}
			after, err := os.ReadFile(archive)
			if err != nil || !bytes.Equal(after, content.Bytes()) {
				t.Fatal("external mode changed supplied package bytes")
			}
		})
	}
}
