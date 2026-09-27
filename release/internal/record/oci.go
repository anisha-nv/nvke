// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package record

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

// ngcOAuthUsername is the username NGC expects alongside an API key.
const ngcOAuthUsername = "$oauthtoken"

// blobMediaType is what a result and a report are stored as. Both are small
// text documents and neither is meant to be interpreted by the registry.
const blobMediaType = "application/octet-stream"

// OCI stores results as OCI artifacts in a registry.
type OCI struct {
	// Credential supplies registry credentials. Nil means anonymous, which
	// is enough to pull from a public repository and never enough to push.
	Credential auth.CredentialFunc

	// PlainHTTP addresses a registry without TLS, for a local registry in a
	// test. Production references are https.
	PlainHTTP bool
}

// NGCCredential reads the key NGC pushes and pulls are authenticated with.
//
// Returning nil for an unset key is deliberate: anonymous is the right
// client for pulling a public repository, and a push that needed the key
// fails at the registry with an authorization error naming the repository,
// which is a better message than one naming an environment variable.
func NGCCredential(registry string) auth.CredentialFunc {
	key := os.Getenv("NGC_API_KEY")
	if key == "" {
		return nil
	}
	return auth.StaticCredential(registry, auth.Credential{
		Username: ngcOAuthUsername,
		Password: key,
	})
}

// Push writes the artifact's files as one manifest.
func (o OCI) Push(ctx context.Context, ref string, files Files) error {
	repo, tag, err := o.repository(ref)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("refusing to push %s with no files", ref)
	}

	// Staged locally first so the manifest and its blobs go up as one copy:
	// a partial push would leave a tag whose blobs are missing, and the
	// aggregate would then see a result it cannot read.
	dir, err := os.MkdirTemp("", "qualify-push")
	if err != nil {
		return fmt.Errorf("staging the artifact: %w", err)
	}
	defer os.RemoveAll(dir)

	src, err := file.New(dir)
	if err != nil {
		return fmt.Errorf("opening the staging store: %w", err)
	}
	defer src.Close()

	// Sorted so that the same files produce the same manifest, which makes a
	// state pushed twice from the same results identical rather than merely
	// equivalent.
	layers := make([]ocispec.Descriptor, 0, len(files))
	for _, name := range slices.Sorted(maps.Keys(files)) {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("staging %s: %w", name, err)
		}
		if err := os.WriteFile(path, files[name], 0o644); err != nil {
			return fmt.Errorf("staging %s: %w", name, err)
		}
		desc, err := src.Add(ctx, name, blobMediaType, path)
		if err != nil {
			return fmt.Errorf("adding %s: %w", name, err)
		}
		layers = append(layers, desc)
	}

	manifest, err := oras.PackManifest(ctx, src, oras.PackManifestVersion1_1, ArtifactType,
		oras.PackManifestOptions{Layers: layers})
	if err != nil {
		return fmt.Errorf("packing the manifest: %w", err)
	}
	if err := src.Tag(ctx, manifest, tag); err != nil {
		return fmt.Errorf("tagging the manifest: %w", err)
	}
	if _, err := oras.Copy(ctx, src, tag, repo, tag, oras.DefaultCopyOptions); err != nil {
		return fmt.Errorf("pushing %s: %w", ref, err)
	}
	return nil
}

// Pull reads one artifact back.
func (o OCI) Pull(ctx context.Context, ref string) (Files, error) {
	repo, tag, err := o.repository(ref)
	if err != nil {
		return nil, err
	}

	desc, err := repo.Resolve(ctx, tag)
	if err != nil {
		if errors.Is(err, errdef.ErrNotFound) {
			return nil, fmt.Errorf("%s: %w", ref, ErrNotFound)
		}
		return nil, fmt.Errorf("resolving %s: %w", ref, err)
	}

	files, err := readFiles(ctx, repo, desc)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", ref, err)
	}
	return files, nil
}

// Tags lists a repository's tags.
func (o OCI) Tags(ctx context.Context, repository string) ([]string, error) {
	repo, err := o.newRepository(repository)
	if err != nil {
		return nil, err
	}
	var tags []string
	if err := repo.Tags(ctx, "", func(page []string) error {
		tags = append(tags, page...)
		return nil
	}); err != nil {
		if errors.Is(err, errdef.ErrNotFound) {
			// The repository has never been pushed to. That is the ordinary
			// state before the first result, not an error.
			return nil, nil
		}
		return nil, fmt.Errorf("listing tags in %s: %w", repository, err)
	}
	return tags, nil
}

// readFiles fetches a manifest's layers, keyed by their title annotation.
func readFiles(ctx context.Context, src content.Fetcher, desc ocispec.Descriptor) (Files, error) {
	b, err := content.FetchAll(ctx, src, desc)
	if err != nil {
		return nil, err
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		return nil, fmt.Errorf("parsing the manifest: %w", err)
	}

	files := Files{}
	for _, layer := range manifest.Layers {
		name := layer.Annotations[ocispec.AnnotationTitle]
		if name == "" {
			continue
		}
		if files[name], err = content.FetchAll(ctx, src, layer); err != nil {
			return nil, fmt.Errorf("fetching %s: %w", name, err)
		}
	}
	return files, nil
}

// repository splits a reference and opens it.
func (o OCI) repository(ref string) (*remote.Repository, string, error) {
	name, tag, ok := strings.Cut(ref, ":")
	if !ok || tag == "" {
		return nil, "", fmt.Errorf("reference %q has no tag", ref)
	}
	repo, err := o.newRepository(name)
	if err != nil {
		return nil, "", err
	}
	return repo, tag, nil
}

func (o OCI) newRepository(name string) (*remote.Repository, error) {
	repo, err := remote.NewRepository(name)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", name, err)
	}
	repo.PlainHTTP = o.PlainHTTP
	repo.Client = &auth.Client{
		Client:     retry.DefaultClient,
		Cache:      auth.NewCache(),
		Credential: o.Credential,
	}
	return repo, nil
}
