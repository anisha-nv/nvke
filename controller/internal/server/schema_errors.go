// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"
	"strconv"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
)

// Schema errors must not echo request values, unknown keys, or whole schemas.
// Only field names from the published schema and bounded array indices appear.
func safeRequestValidationOptions(spec *openapi3.T) openapi3filter.Options {
	names := map[string]bool{}
	seen := map[*openapi3.Schema]bool{}
	var collect func(*openapi3.Schema)
	collect = func(s *openapi3.Schema) {
		if s == nil || seen[s] {
			return
		}
		seen[s] = true
		for name, ref := range s.Properties {
			names[name] = true
			collect(ref.Value)
		}
		if s.Items != nil {
			collect(s.Items.Value)
		}
		for _, refs := range []openapi3.SchemaRefs{s.AllOf, s.AnyOf, s.OneOf} {
			for _, ref := range refs {
				collect(ref.Value)
			}
		}
	}
	for _, ref := range spec.Components.Schemas {
		collect(ref.Value)
	}
	options := openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}
	options.WithCustomSchemaErrorFunc(func(err *openapi3.SchemaError) string {
		parts := []string{}
		for _, part := range err.JSONPointer() {
			if len(parts) >= 12 {
				break
			}
			if names[part] {
				parts = append(parts, part)
				continue
			}
			if i, parseErr := strconv.Atoi(part); parseErr == nil && i >= 0 && i < 100000 {
				parts = append(parts, part)
				continue
			}
			break
		}
		path := ""
		for _, part := range parts {
			if _, err := strconv.Atoi(part); err == nil {
				path += "[" + part + "]"
			} else {
				if path != "" {
					path += "."
				}
				path += part
			}
		}
		if path == "" {
			path = "request"
		}
		return fmt.Sprintf("%s: does not match the API schema", path)
	})
	return options
}
