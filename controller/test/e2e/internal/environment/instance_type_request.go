// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package environment

import (
	"maps"
)

// DefaultCPURequest is the instance type request made when nothing asks otherwise:
// for the Site Controller, a tenant control-plane pool, and a tenant worker
// pool that declares nothing. A GPU instance type has to be requested; it is
// never what an unset request lands on because of the order a Forge site
// lists its types.
func DefaultCPURequest() map[string]string {
	return map[string]string{capabilityMachineType: "cpu"}
}

// InstanceTypeRequest is the request MatchInstanceType is given for one thing
// being placed: the Site Controller, a tenant control-plane pool, or a tenant
// worker pool.
//
// defaults is what that thing asks for itself, override is what a run set for
// it specifically, and run is the run's shared -e2e.capabilities. override
// wins key by key over defaults, so a Forge site with only GPU instance types
// can still host a tenant control plane, by asking for machineType=gpu.
//
// From run, a request takes only what applies to it and has not set itself:
// arch always, and the GPU model only when it asks for a GPU instance type. A
// run that names gb200 is choosing the model for its GPU worker pools, not
// asking for its tenant control plane to be a GPU instance type as well --
// adding gpu to a CPU request would leave nothing at the Forge site able to
// answer it.
func InstanceTypeRequest(defaults, override, run map[string]string) map[string]string {
	out := make(map[string]string, len(defaults)+len(override)+2)
	maps.Copy(out, defaults)
	maps.Copy(out, override)
	if arch, ok := run[capabilityArch]; ok {
		if _, set := out[capabilityArch]; !set {
			out[capabilityArch] = arch
		}
	}
	if model, ok := run[capabilityGPU]; ok && out[capabilityMachineType] == "gpu" {
		if _, set := out[capabilityGPU]; !set {
			out[capabilityGPU] = model
		}
	}
	return out
}
