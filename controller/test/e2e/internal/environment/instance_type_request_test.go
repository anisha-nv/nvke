// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package environment

import (
	"maps"
	"testing"
)

func TestInstanceTypeRequest(t *testing.T) {
	gpuWorkerPool := map[string]string{"machineType": "gpu", "arch": "amd64"}

	cases := []struct {
		name               string
		defaults, override map[string]string
		run                map[string]string
		want               map[string]string
	}{{
		name:     "nothing asked is a CPU instance type",
		defaults: DefaultCPURequest(),
		want:     map[string]string{"machineType": "cpu"},
	}, {
		name:     "an override puts a tenant control plane on a GPU instance type",
		defaults: DefaultCPURequest(),
		override: map[string]string{"machineType": "gpu"},
		want:     map[string]string{"machineType": "gpu"},
	}, {
		name:     "the run's GPU model does not turn a CPU request into a GPU one",
		defaults: DefaultCPURequest(),
		run:      map[string]string{"gpu": "gb200", "arch": "arm64"},
		want:     map[string]string{"machineType": "cpu", "arch": "arm64"},
	}, {
		name:     "a GPU worker pool takes the run's GPU model",
		defaults: gpuWorkerPool,
		run:      map[string]string{"gpu": "gb200"},
		want:     map[string]string{"machineType": "gpu", "arch": "amd64", "gpu": "gb200"},
	}, {
		name:     "a tenant control plane overridden to GPU takes the run's GPU model too",
		defaults: DefaultCPURequest(),
		override: map[string]string{"machineType": "gpu"},
		run:      map[string]string{"gpu": "l40"},
		want:     map[string]string{"machineType": "gpu", "gpu": "l40"},
	}, {
		name:     "what a request sets itself is not replaced by the run",
		defaults: gpuWorkerPool,
		run:      map[string]string{"arch": "arm64"},
		want:     map[string]string{"machineType": "gpu", "arch": "amd64"},
	}, {
		name:     "software capabilities select no instance type and are not copied",
		defaults: DefaultCPURequest(),
		run:      map[string]string{"kube": "1.37"},
		want:     map[string]string{"machineType": "cpu"},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := InstanceTypeRequest(tc.defaults, tc.override, tc.run)
			if !maps.Equal(got, tc.want) {
				t.Errorf("InstanceTypeRequest() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInstanceTypeRequestDoesNotWriteItsInputs(t *testing.T) {
	defaults := DefaultCPURequest()
	InstanceTypeRequest(defaults, map[string]string{"machineType": "gpu"}, map[string]string{"arch": "arm64"})
	if !maps.Equal(defaults, DefaultCPURequest()) {
		t.Errorf("defaults = %v after the call, want it unchanged", defaults)
	}
}
