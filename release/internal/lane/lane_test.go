// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lane

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestForRejectsWhatItCannotRun(t *testing.T) {
	// The alternative to an error is a filter that selects nothing, and
	// ginkgo exits zero on an empty selection -- so the run would report a
	// pass it never earned.
	for _, provider := range []string{"", "forge", "capd", "NICO"} {
		t.Run(provider, func(t *testing.T) {
			if _, err := For(provider); err == nil {
				t.Fatalf("want an error for provider %q", provider)
			}
		})
	}
}

func TestForNamesWhatWouldHaveWorked(t *testing.T) {
	_, err := For("forge")
	if err == nil {
		t.Fatal("want an error")
	}
	// An error that lists the known providers is the difference between
	// fixing a typo and reading the source.
	if !strings.Contains(err.Error(), "nico") {
		t.Errorf("the error does not say what is known: %v", err)
	}
}

func TestNicoLane(t *testing.T) {
	l, err := For("nico")
	if err != nil {
		t.Fatal(err)
	}
	if l.Backend != "nico" {
		t.Errorf("backend is %q", l.Backend)
	}
	// nico-containers, not nico: nico-vms is a different backend with its own
	// label and its own exclusions.
	if l.Filter() != "nico-containers" {
		t.Errorf("filter is %q", l.Filter())
	}
	if !l.NeedsCAPNICoSource {
		t.Error("the lane needs the internal CAPNICo checkout")
	}
	// The Tiltfile rejects a provider list without capi_core.
	if !slices.Contains(l.Providers, "capi_core") {
		t.Errorf("capi_core is missing: %v", l.Providers)
	}
}

func TestFilterAppliesExclusions(t *testing.T) {
	// Built rather than written out, so an exclusion added to the table
	// cannot be forgotten in the expression.
	l := Lane{Label: "nico-vms", Exclude: []string{"gpu", "extended"}}
	if want := "nico-vms && !gpu && !extended"; l.Filter() != want {
		t.Errorf("got %q, want %q", l.Filter(), want)
	}
}

func TestProvidersJSONIsAnArrayTheWorkflowCanParse(t *testing.T) {
	l, err := For("nico")
	if err != nil {
		t.Fatal(err)
	}
	var back []string
	if err := json.Unmarshal([]byte(l.ProvidersJSON()), &back); err != nil {
		t.Fatalf("the workflow input would not parse: %v", err)
	}
	if !slices.Equal(back, l.Providers) {
		t.Errorf("round trip changed the list: %v -> %v", l.Providers, back)
	}
}

func TestKnownIsSorted(t *testing.T) {
	// It lands in an error message, so it should read the same every time.
	got := Known()
	if !slices.IsSorted(got) {
		t.Errorf("not sorted: %v", got)
	}
}
