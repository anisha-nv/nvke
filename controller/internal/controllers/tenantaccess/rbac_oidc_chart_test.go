// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: LicenseRef-NvidiaProprietary
//
// NVIDIA CORPORATION, its affiliates and licensors retain all intellectual
// property and proprietary rights in and to this material, related
// documentation and any modifications thereto. Any use, reproduction,
// disclosure or distribution of this material and related documentation
// without an express license agreement from NVIDIA CORPORATION or
// its affiliates is strictly prohibited.

package tenantaccess

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRBACOIDCChartAlwaysGrantsNkeRBACConfigMapGet(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "deploy", "charts", "cloud-workflow-engine", "templates", "rbac-oidc.yaml")
	body, err := os.ReadFile(path)
	require.NoError(t, err)

	docs := strings.Split(string(body), "\n---\n")
	require.NotEmpty(t, docs)
	role := docs[0]
	require.Contains(t, role, "kind: Role")
	require.NotContains(t, role, "{{- if .Values.nkeOIDCAuth.enabled }}", "nke-rbac ConfigMap get must survive live OIDC flag-off")
	require.Contains(t, role, `resources: ["configmaps"]`)
	require.Contains(t, role, "- nke-rbac")
	require.Contains(t, role, `verbs: ["get"]`)
}
