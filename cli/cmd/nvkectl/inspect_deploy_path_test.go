// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectDeployPath(t *testing.T) {
	nico := filepath.Join("..", "..", "..", "deploy", "nico")
	stdout, _, err := runGenerate(t, "inspect", "deploy-path", nico)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 11 || lines[0] != "nvke-base-config\ttemplate" || lines[10] != "controller\thelm" {
		t.Errorf("items:\n%s", stdout)
	}
	// One helm item as a record: fields in the executor's read order.
	stdout, _, err = runGenerate(t, "inspect", "deploy-path", nico, "--item", "spire")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(strings.TrimSuffix(stdout, "\n"), "\x1f")
	if len(fields) != 9 || fields[0] != "spire" || fields[1] != "0.30.1" || fields[2] != "https://spiffe.github.io/helm-charts-hardened/" || fields[3] != "" ||
		fields[4] != "spire" || fields[5] != "spire-server" || fields[6] != "values.yaml\x1eoverlay.yaml" ||
		fields[7] != "certificate:oidc-provider-upstream-ca:spire-server:Ready" || fields[8] != "" {
		t.Errorf("spire record: %q", fields)
	}
	// A rendered setFile list comes through entry by entry.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nvke-deploy.yaml"), []byte("items:\n  - {name: x, type: helm}\n  - {name: m, type: manifest}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x", "release.yaml"), []byte("chart:\n  name: x\n  version: \"1\"\n  path: /abs/x\n  setFile:\n    - a.b=/etc/a\n    - c.d=/etc/c\nrelease:\n  name: x\n  namespace: n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = runGenerate(t, "inspect", "deploy-path", dir, "--item", "x")
	if err != nil {
		t.Fatal(err)
	}
	if fields := strings.Split(strings.TrimSuffix(stdout, "\n"), "\x1f"); fields[3] != "/abs/x" || fields[8] != "a.b=/etc/a\x1ec.d=/etc/c" {
		t.Errorf("x record: %q", fields)
	}
	// Refusals name their cause.
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"inspect", "deploy-path", dir, "--item", "nope"}, `no item "nope"`},
		{[]string{"inspect", "deploy-path", dir, "--item", "m"}, "is a manifest item, not helm"},
		{[]string{"inspect", "deploy-path", filepath.Join(dir, "absent")}, "no such file"},
	} {
		if _, _, err := runGenerate(t, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: want %q, got %v", tc.args, tc.want, err)
		}
	}
}
