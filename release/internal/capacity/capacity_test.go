// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package capacity

import (
	"context"
	"testing"
)

func TestUnknownProviderIsAnError(t *testing.T) {
	// Falling back to the runner would run a Forge stream on a CI machine
	// and record the result under the Forge stream's name.
	if _, err := Default().For("forge"); err == nil {
		t.Fatal("want an error for a provider with no client")
	}
	if _, err := Default().For(""); err == nil {
		t.Fatal("want an error for a stream declaring no provider")
	}
}

func TestRunnerIsAlwaysAvailable(t *testing.T) {
	// The runner's hardware is the job itself, so there is nothing to wait
	// for and nothing to hand back.
	p, err := Default().For("nico")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := p.Acquire(context.Background(), Requirement{Stream: "s", Nodes: map[string]int{"cpu": 3}})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}
