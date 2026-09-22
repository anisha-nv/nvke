// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package registrymirror

import "strings"

// Image is a container image reference split into the parts a mirror acts on.
type Image struct {
	// Registry is the image host. docker.io is spelled out.
	Registry string
	// Path is the repository path. library/ is spelled out for Docker Hub.
	Path string
	// Tag is the tag, or "" when the reference carries only a digest.
	Tag string
	// Digest is the sha256:… digest, or "".
	Digest string
}

// ParseImage splits a registry image reference. It reports false for a path,
// a URL, or a hostless reference such as cwe-controller:local.
func ParseImage(ref string) (Image, bool) {
	ref = strings.TrimPrefix(ref, "oci://")
	if ref == "" || strings.HasPrefix(ref, ".") || strings.HasPrefix(ref, "/") ||
		strings.Contains(ref, "://") || strings.ContainsAny(ref, " \t\n") {
		return Image{}, false
	}

	var img Image
	if at := strings.Index(ref, "@"); at >= 0 {
		img.Digest = ref[at+1:]
		ref = ref[:at]
		if !strings.HasPrefix(img.Digest, "sha256:") {
			return Image{}, false
		}
	}

	first, rest, hasPath := strings.Cut(ref, "/")
	if !hasPath {
		// A single segment is a hostless local image.
		return Image{}, false
	}
	if !looksLikeRegistry(first) {
		// docker.io shorthand: temporalio/server:1.30.3
		img.Registry = DockerHubRegistry
		rest = ref
	} else {
		img.Registry = normalizeRegistry(first)
	}

	if colon := strings.LastIndex(rest, ":"); colon >= 0 && !strings.Contains(rest[colon:], "/") {
		img.Tag = rest[colon+1:]
		rest = rest[:colon]
	}
	if img.Registry == DockerHubRegistry && !strings.Contains(rest, "/") {
		rest = "library/" + rest
	}
	img.Path = rest
	if img.Path == "" {
		return Image{}, false
	}
	return img, true
}

// String renders the reference back in canonical form.
func (i Image) String() string {
	s := i.Registry + "/" + i.Path
	if i.Tag != "" {
		s += ":" + i.Tag
	}
	if i.Digest != "" {
		s += "@" + i.Digest
	}
	return s
}

// looksLikeRegistry reports whether the first segment is a host: it has a
// dot, a port, or is "localhost".
func looksLikeRegistry(segment string) bool {
	return segment == "localhost" || strings.ContainsAny(segment, ".:")
}
