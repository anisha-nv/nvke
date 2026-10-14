// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderTemplateItem(t *testing.T) {
	nico := filepath.Join("..", "..", "..", "deploy", "nico", "nvke-base-config")
	config := filepath.Join("..", "..", "..", "deploy", "testdata", "bootstrap-config.json")
	out := filepath.Join(t.TempDir(), "out")
	stdout, _, err := runGenerate(t, "render", "template-item", "--config", config, "--item", nico, "--out", out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout, "rendered 3 templates of ") {
		t.Errorf("stdout: %q", stdout)
	}
	secret, err := os.ReadFile(filepath.Join(out, "20-nico-credentials.yaml"))
	if err != nil || !strings.Contains(string(secret), "token: \"") {
		t.Errorf("rendered secret: %v\n%s", err, secret)
	}
	if _, err := os.Stat(filepath.Join(out, "00-namespaces.yaml")); err != nil {
		t.Error("plain manifest not copied")
	}
	if left, _ := filepath.Glob(filepath.Join(out, "*.tmpl")); len(left) != 0 {
		t.Errorf("templates copied: %v", left)
	}
	// Refusals: an empty value in the document, a field the document lacks, a
	// directory with no template.
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{"Nico":{"Endpoint":"https://x","OrgID":"","Token":"t","APIName":"forge"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runGenerate(t, "render", "template-item", "--config", empty, "--item", nico, "--out", filepath.Join(dir, "o1")); err == nil || !strings.Contains(err.Error(), ".Nico.OrgID is empty") {
		t.Errorf("empty value: %v", err)
	}
	lacking := filepath.Join(dir, "lacking.json")
	if err := os.WriteFile(lacking, []byte(`{"Nico":{"Endpoint":"https://x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runGenerate(t, "render", "template-item", "--config", lacking, "--item", nico, "--out", filepath.Join(dir, "o2")); err == nil || !strings.Contains(err.Error(), "20-nico-credentials.yaml.tmpl") {
		t.Errorf("missing field: %v", err)
	}
	plain := filepath.Join(dir, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, "a.yaml"), []byte("a: b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runGenerate(t, "render", "template-item", "--config", config, "--item", plain, "--out", filepath.Join(dir, "o3")); err == nil || !strings.Contains(err.Error(), "no .yaml.tmpl file") {
		t.Errorf("no template: %v", err)
	}
}
