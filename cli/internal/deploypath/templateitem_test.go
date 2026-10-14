// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package deploypath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadTemplateConfig(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"good.json":  `{"ClusterName": "site", "Nico": {"Endpoint": "https://nico.example", "Token": "tok"}, "Hosts": ["a", "b"]}`,
		"empty.json": `{"ClusterName": "site", "Nico": {"Endpoint": "", "Token": "tok"}}`,
		"null.json":  `{"ClusterName": null}`,
		"list.json":  `{"Hosts": ["a", ""]}`,
		"bad.json":   `{"ClusterName": `,
	})
	cfg, err := LoadTemplateConfig(filepath.Join(dir, "good.json"))
	if err != nil || cfg["ClusterName"] != "site" {
		t.Fatalf("good config: %v %v", cfg, err)
	}
	for file, want := range map[string]string{
		"empty.json": ".Nico.Endpoint is empty",
		"null.json":  ".ClusterName is null",
		"list.json":  ".Hosts[1] is empty",
		"bad.json":   "unexpected end of JSON input",
	} {
		_, err := LoadTemplateConfig(filepath.Join(dir, file))
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), file) {
			t.Errorf("%s: want %q naming the file, got %v", file, want, err)
		}
	}
}

func TestRenderTemplateDir(t *testing.T) {
	src := t.TempDir()
	writeFiles(t, src, map[string]string{
		"10-secret.yaml.tmpl": "kind: Secret\nstringData:\n  token: \"{{ .Nico.Token }}\"\n",
		"20-plain.yaml":       "kind: Namespace\n",
		"notes.md":            "ignored\n",
	})
	data := map[string]any{"Nico": map[string]any{"Token": "tok"}}
	dst := filepath.Join(t.TempDir(), "out")
	n, err := RenderTemplateDir(src, dst, data)
	if err != nil || n != 1 {
		t.Fatalf("rendered %d templates: %v", n, err)
	}
	// The template is rendered, the plain manifest copied, the rest left out.
	rendered, _ := os.ReadFile(filepath.Join(dst, "10-secret.yaml"))
	if !strings.Contains(string(rendered), `token: "tok"`) {
		t.Errorf("rendered secret:\n%s", rendered)
	}
	if _, err := os.Stat(filepath.Join(dst, "20-plain.yaml")); err != nil {
		t.Error("the plain manifest was not copied")
	}
	for _, name := range []string{"10-secret.yaml.tmpl", "notes.md"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err == nil {
			t.Errorf("%s should not be in the output", name)
		}
	}
	if info, err := os.Stat(dst); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("output directory mode: %v %v", info, err)
	}

	// A field the data lacks is refused, and so is a template that renders
	// to nothing, and a directory with no template.
	cases := map[string]struct {
		files map[string]string
		data  map[string]any
		want  string
	}{
		"missing field":  {map[string]string{"a.yaml.tmpl": "x: {{ .Missing }}\n"}, data, "map has no entry for key"},
		"renders empty":  {map[string]string{"a.yaml.tmpl": "{{ if false }}x{{ end }}\n"}, data, "rendered to nothing"},
		"not a template": {map[string]string{"a.yaml.tmpl": "x: {{ .Nico.Token \n"}, data, "a.yaml.tmpl:"},
		"no template":    {map[string]string{"a.yaml": "kind: Namespace\n"}, data, "holds no .yaml.tmpl file"},
	}
	for name, tc := range cases {
		src := t.TempDir()
		writeFiles(t, src, tc.files)
		if _, err := RenderTemplateDir(src, filepath.Join(t.TempDir(), "out"), tc.data); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want %q, got %v", name, tc.want, err)
		}
	}
}
