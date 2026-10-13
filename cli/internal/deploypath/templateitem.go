// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package deploypath

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

// LoadTemplateConfig reads a BootstrapConfig JSON document for
// RenderTemplateDir. Every leaf must be a non-empty scalar: a missing value
// would otherwise render an empty Secret and fail far from the cause.
func LoadTemplateConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := checkLeaves(cfg, ""); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func checkLeaves(node any, at string) error {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			if err := checkLeaves(child, at+"."+k); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range v {
			if err := checkLeaves(child, fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
	case nil:
		return fmt.Errorf("%s is null", at)
	case string:
		if v == "" {
			return fmt.Errorf("%s is empty", at)
		}
	}
	return nil
}

// RenderTemplateDir renders every .yaml.tmpl file of src against data into
// dst, copies the plain .yaml and .yml files beside them, and returns how
// many templates it rendered. A template naming a field data lacks, or
// rendering to nothing, is an error; so is a directory with no template.
func RenderTemplateDir(src, dst string, data any) (int, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return 0, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	rendered := 0
	for _, name := range names {
		path := filepath.Join(src, name)
		switch {
		case strings.HasSuffix(name, ".yaml.tmpl"):
			text, err := os.ReadFile(path)
			if err != nil {
				return rendered, err
			}
			tmpl, err := template.New(name).Option("missingkey=error").Parse(string(text))
			if err != nil {
				return rendered, fmt.Errorf("%s: %w", name, err)
			}
			var out bytes.Buffer
			if err := tmpl.Execute(&out, data); err != nil {
				return rendered, fmt.Errorf("%s: %w", name, err)
			}
			if strings.TrimSpace(out.String()) == "" {
				return rendered, fmt.Errorf("%s: rendered to nothing", name)
			}
			if err := os.WriteFile(filepath.Join(dst, strings.TrimSuffix(name, ".tmpl")), out.Bytes(), 0o600); err != nil {
				return rendered, err
			}
			rendered++
		case strings.HasSuffix(name, ".yaml"), strings.HasSuffix(name, ".yml"):
			if err := copyFile(path, filepath.Join(dst, name)); err != nil {
				return rendered, err
			}
		}
	}
	if rendered == 0 {
		return 0, fmt.Errorf("%s holds no .yaml.tmpl file; it is a manifest item, not a template one", src)
	}
	return rendered, nil
}
