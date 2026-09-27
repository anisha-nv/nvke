// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package capacity confirms the hardware a run needs is available before
// anything starts, so a shortage is reported as a shortage rather than as a
// failed test.
//
// The distinction is the reason this is its own component. A planner failure
// means the declared constraints cannot be satisfied and nothing should run.
// A shortage means the run cannot start yet and says nothing about the
// software. Only a failure reported back through the suite is a qualification
// result.
package capacity

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ErrUnschedulable is returned when the hardware a run needs is not
// available. It is a distinct outcome rather than an error on the software,
// and `run` records it as such.
var ErrUnschedulable = errors.New("no capacity available")

// Requirement is what one entry consumes.
//
// The suite declares it, because the suite is what decides how many nodes a
// site controller takes. Nothing in the qualification policy says a stream
// needs three CPU nodes: putting it there would duplicate a number the specs
// already own, and the two would drift the first time a spec changed shape.
type Requirement struct {
	Stream string

	// Nodes is machine type to count, {"cpu": 3, "gpu": 2}. A count per type
	// is all it carries; architecture and GPU SKU come from the stream, and
	// an instance type declares what it provides.
	Nodes map[string]int
}

// String renders a requirement for a log line, in a stable order.
func (r Requirement) String() string {
	if len(r.Nodes) == 0 {
		return "no hardware"
	}
	parts := make([]string, 0, len(r.Nodes))
	for _, k := range slices.Sorted(maps.Keys(r.Nodes)) {
		parts = append(parts, fmt.Sprintf("%s=%d", k, r.Nodes[k]))
	}
	return strings.Join(parts, " ")
}

// Provider confirms, and later reserves, the hardware a run needs.
type Provider interface {
	Acquire(ctx context.Context, need Requirement) (Lease, error)
}

// Lease is held for the length of a run. `run` defers Release immediately
// after acquiring rather than calling it as a final step, so it runs on
// success, on a failed assertion, on a panic and on cancellation: a lease
// that outlives its run is a bill nobody is watching.
type Lease interface {
	Release(ctx context.Context) error
}

// noLease is what a provider returns when there is nothing to give back.
type noLease struct{}

func (noLease) Release(context.Context) error { return nil }

// Runner is the provider for streams whose hardware is the CI runner itself.
//
// It is not a stub. A nico-sim stream builds its cluster in containers on the
// runner the job already holds, so there is no pool to exhaust and no
// reservation to make: the job either got a runner or never started. An
// availability check here would always pass, and one that always passes is
// worse than none because it reads as a check.
type Runner struct{}

// Acquire reports the runner as available.
func (Runner) Acquire(context.Context, Requirement) (Lease, error) { return noLease{}, nil }

// Providers resolves a stream's provider capability to the capacity provider
// that can speak for it.
//
// An unknown provider is an error rather than a default, and that is the
// point of the map existing at all. Falling back to Runner would let the
// first Forge stream run with no availability check and look like it had one.
type Providers map[string]Provider

// Default is the provider set that exists today.
//
// nico is nico-sim's container backend, which runs on the runner. There is no
// entry for a Forge provider yet: the policy declares no Forge stream, so an
// entry here would be a client for an API no plan reaches. It arrives with
// the stream, and the interface does not change when it does, because Acquire
// already returns something that must be released.
func Default() Providers {
	return Providers{"nico": Runner{}}
}

// For returns the provider for a stream's provider capability.
func (p Providers) For(provider string) (Provider, error) {
	if provider == "" {
		return nil, errors.New("the stream declares no provider capability, so no capacity provider can speak for it")
	}
	got, ok := p[provider]
	if !ok {
		return nil, fmt.Errorf(
			"no capacity provider for %q; known providers are %s",
			provider, strings.Join(slices.Sorted(maps.Keys(p)), ", "),
		)
	}
	return got, nil
}
