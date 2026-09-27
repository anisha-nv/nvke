// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package lane maps a stream's provider to the CI lane that can execute it.
//
// A stream declares a provider; a provider implies a backend, a label filter
// and the set of Tilt providers the backend needs installed. That mapping used
// to live in two places -- a filter table in the suite package and a literal in
// the workflow -- which meant a workflow expression decided what a provider
// needs, untested, with no way to reject one it does not know.
//
// It lives here instead so `plan` can refuse to schedule a stream nothing can
// run. That refusal is the point: before this, a stream naming an unknown
// provider would have been handed whichever lane wiring the workflow had
// written down, run somebody else's specs, and recorded the result under its
// own name.
//
// What stays out: runner labels. Which runner satisfies an architecture is the
// workflow's knowledge, and putting `linux-amd64-cpu16` here would make the
// release policy depend on CI's machine naming.
package lane

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Lane is one backend's execution recipe, in the terms backend-e2e.yml takes.
type Lane struct {
	// Backend names the lane for run ids, artifact names and logs.
	Backend string

	// Label selects this backend's specs. It is narrower than the provider
	// name: `nico` has two backends, containers and VMs, with a label each
	// and different exclusions.
	Label string

	// Exclude names labels the backend cannot satisfy. A nico-vms stream
	// excludes gpu; a Forge stream excludes nothing.
	Exclude []string

	// Providers are the Tilt providers the lane installs. capi_core is
	// required by every backend; the Tiltfile rejects a list omitting it.
	Providers []string

	// NeedsCAPNICoSource checks out dsx-ai-factory/cluster-api-provider-nico
	// beside the monorepo. It is an internal repository, so this is also what
	// makes the lane need a token.
	NeedsCAPNICoSource bool
}

// lanes is deliberately not a default-carrying lookup. A provider absent here
// is an error, because the alternative is a filter that selects nothing and
// ginkgo exits zero on an empty selection -- so the run would report a pass it
// never earned.
var lanes = map[string]Lane{
	"nico": {
		Backend:            "nico",
		Label:              "nico-containers",
		Providers:          []string{"capi_core", "bgp-containers", "nico"},
		NeedsCAPNICoSource: true,
	},
}

// For returns the lane a provider runs in.
func For(provider string) (Lane, error) {
	if provider == "" {
		return Lane{}, fmt.Errorf("the stream declares no provider")
	}
	l, ok := lanes[provider]
	if !ok {
		return Lane{}, fmt.Errorf("provider %q has no lane; known: %v", provider, Known())
	}
	return l, nil
}

// Known lists the providers that can be scheduled, for an error message that
// says what would have worked.
func Known() []string {
	out := make([]string, 0, len(lanes))
	for p := range lanes {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// Filter is the ginkgo label expression this lane runs with.
//
// Built rather than written out, so an exclusion cannot be added to the table
// and forgotten in the expression -- and so `plan` and the suite runner derive
// the identical string from the identical source.
func (l Lane) Filter() string {
	expr := l.Label
	for _, x := range l.Exclude {
		expr += " && !" + x
	}
	return expr
}

// ProvidersJSON renders the Tilt provider list as the JSON array the workflow
// input expects. Marshalling rather than joining by hand, so a provider name
// needing an escape cannot produce a document the Tiltfile silently misreads.
func (l Lane) ProvidersJSON() string {
	b, err := json.Marshal(l.Providers)
	if err != nil {
		// A []string cannot fail to marshal. Panicking here rather than
		// returning an error keeps the signature usable in a template.
		panic(err)
	}
	return string(b)
}
