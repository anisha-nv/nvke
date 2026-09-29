// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package v1

const (
	// MachineInitKubernetesVersionAnnotation records the Kubernetes version a
	// fixed-version profile boots. The value is empty when
	// MachineInitKubernetesVersionInjectedAnnotation is "true" and the version
	// is chosen at provisioning time. Annotations carry this because the
	// release spec is immutable boot content; fixed-versus-injected
	// compatibility is backward-compatible metadata.
	MachineInitKubernetesVersionAnnotation = GroupName + "/machine-init-kubernetes-version"

	// MachineInitKubernetesVersionInjectedAnnotation is "true" when the profile
	// accepts a Kubernetes version at provisioning time, and "false" when
	// MachineInitKubernetesVersionAnnotation is fixed.
	MachineInitKubernetesVersionInjectedAnnotation = GroupName + "/machine-init-kubernetes-version-injected"
)
