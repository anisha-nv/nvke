// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package osprofileconvert reads an OS profile source tree and emits one
// MachineImage, one ClusterRelease, and one NodePoolRelease per profile.
//
// The resource name is the profile name, which is the value existing osProfile
// selections already store. A profile listed in deprecated.yaml is emitted with
// spec.lifecycle.deprecated set on both releases. Regeneration may change that
// field on a manifest that already exists, and fails when any other published
// spec or Kubernetes-version annotation would change.
package osprofileconvert
