// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package record

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nke/release/internal/policy"
)

func TestKeyTagIsCompositeAndLegal(t *testing.T) {
	// The tag carries component, version, combination and stream so that
	// listing tags says which results exist without pulling any of them.
	// The component is in the repository rather than the tag.
	k := Key{
		Component:   "controller",
		Version:     "v0.1.1-rc.8",
		Combination: "os-image-v0.1.0-rc.4_cli-v0.1.0-rc.7",
		Stream:      "nico-amd64-kube-1.34",
	}

	tag := k.Tag()
	for _, part := range []string{"v0.1.1-rc.8", "nico-amd64-kube-1.34"} {
		if !strings.Contains(tag, part) {
			t.Errorf("tag %q does not carry %q", tag, part)
		}
	}
	// _ is legal in a tag and appears in a combination id, so the separator
	// has to survive sanitising while the id's own _ does too.
	for _, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_':
		default:
			t.Errorf("tag %q holds %q, which an OCI tag does not admit", tag, r)
		}
	}
}

func TestKeyReference(t *testing.T) {
	k := Key{Component: "controller", Version: "v1.0.0", Combination: "c1", Stream: "s1"}

	if got, want := k.Repository("nvcr.io/org/qualification"), "nvcr.io/org/qualification/controller"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// A trailing slash on the base is a plausible way to write it and must
	// not produce a doubled separator the registry rejects.
	if got := k.Repository("nvcr.io/org/qualification/"); strings.Contains(got, "//") {
		t.Errorf("got %q, want no doubled separator", got)
	}
	if got := k.Reference("nvcr.io/org/qualification"); !strings.HasPrefix(got, k.Repository("nvcr.io/org/qualification")+":") {
		t.Errorf("got %q, want repository:tag", got)
	}
}

func TestSanitise(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"v1.0.0", "v1.0.0"},
		{"nico-amd64-kube-1.34", "nico-amd64-kube-1.34"},
		{"a/b", "a-b"},
		{"a+b", "a-b"},
		{"a b", "a-b"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := sanitise(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	want := New(
		policy.ComponentSpec{
			Component: "controller",
			Version:   "v1.0.0",
			Streams:   []string{"nico-amd64-kube-1.34"},
		},
		policy.StatusCombination{
			ID:           "c1",
			Dependencies: []policy.ResolvedDependency{{Name: "cli", Version: "v0.9.0"}},
			Streams: []policy.StreamResult{{
				Name:    "nico-amd64-kube-1.34",
				Scope:   "full",
				Result:  policy.ResultPassed,
				Summary: &policy.Summary{Passed: 5, Skipped: 267},
			}},
		},
	)

	b, err := Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Files{ResultFile: b}.Result()
	if err != nil {
		t.Fatal(err)
	}

	if got.Spec.Version != want.Spec.Version || got.Metadata.Name != want.Metadata.Name {
		t.Errorf("got %+v, want %+v", got.Spec, want.Spec)
	}
	if got.Status.Outcome("c1", "nico-amd64-kube-1.34") != policy.ResultPassed {
		t.Errorf("the outcome did not survive the round trip: %+v", got.Status)
	}
	if got.Status.Combinations[0].Streams[0].Summary.Skipped != 267 {
		t.Errorf("the summary did not survive the round trip: %+v", got.Status)
	}
}

func TestDecodeRejectsAnotherKind(t *testing.T) {
	// The repository holds one kind. Reading a chart or a policy as a result
	// would fold a document with no status into the state as a record with
	// nothing qualified.
	b := []byte("apiVersion: " + policy.APIVersion + "\nkind: ReleasePolicy\n")
	if _, err := Decode(b); err == nil {
		t.Fatal("want an error for a document of another kind")
	}
}

func TestResultRequiresTheFile(t *testing.T) {
	files := Files{ReportFile: []byte("{}")}
	if _, err := files.Result(); err == nil {
		t.Fatal("want an error for an artifact holding no result")
	}
}
