// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPublication(t *testing.T) {
	tests := []struct {
		name, component, base, tag, wantError string
		prior                                 []string
	}{
		{"reported RC after GA", "controller", "v0.1.2", "controller/v0.1.1-rc.11", "already stable", []string{"controller/v0.1.1"}},
		{"older GA", "controller", "v0.1.0", "controller/v0.1.0", "already stable", []string{"controller/v0.1.1"}},
		{"maintenance release", "controller", "v0.1.2", "controller/v0.1.2-rc.0", "", []string{"controller/v0.2.0", "controller/v0.1.1"}},
		{"GA retry", "controller", "v0.1.2", "controller/v0.1.2", "", nil},
		{"first RC", "agent", "v0.2.2", "agent/v0.2.2-rc.0", "", []string{"agent/v0.2.1"}},
		{"skipped RC0", "cli", "v0.1.1", "cli/v0.1.1-rc.1", "must start", nil},
		{"delayed RC retry", "os-image", "v0.1.1", "os-image/v0.1.1-rc.1", "", []string{"os-image/v0.1.1-rc.0", "os-image/v0.1.1-rc.2"}},
		{"wrong base", "cluster-api-provider-provisioned", "v0.1.1", "cluster-api-provider-provisioned/v9.0.0", "must match", nil},
		{"empty required base", "controller", "", "controller/v9.0.0", "release base", nil},
		{"prerelease base", "controller", "v0.1.2-rc.0", "controller/v0.1.2-rc.0", "release base", nil},
		{"wrong namespace", "controller", "v0.1.2", "agent/v0.1.2", "does not belong", nil},
		{"malformed RC", "controller", "v0.1.2", "controller/v0.1.2-rc0", "require rc.N", nil},
		{"leading zero", "controller", "v0.1.2", "controller/v0.1.2-rc.01", "parsing", nil},
		{"build metadata", "controller", "v0.1.2", "controller/v0.1.2+build", "metadata", nil},
		{"existing NKTP policy", "nktp", "", "nktp/v0.1.0-rc.2", "", []string{"nktp/v0.1.0-rc.1"}},
		{"NKTP RC after GA", "nktp", "", "nktp/v0.1.0-rc.2", "already stable", []string{"nktp/v0.1.0"}},
		{"root GA ordering", "repository", "", "v0.21.0", "already stable", []string{"v0.21.1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refs := map[string]string{tt.tag: "head"}
			for _, tag := range tt.prior {
				refs[tag] = "head"
			}
			err := check(tt.component, tt.base, tt.tag, "head", refs)
			if tt.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantError)
			}
		})
	}
}

func TestRemoteIdentity(t *testing.T) {
	tag := "controller/v0.1.2-rc.0"
	require.ErrorContains(t, check("controller", "v0.1.2", tag, "head", nil), "checked-out commit")
	require.ErrorContains(t, check("controller", "v0.1.2", tag, "head", map[string]string{tag: "other"}), "checked-out commit")
	require.NoError(t, check("controller", "v0.1.2", tag, "head", map[string]string{tag: "tag-object", tag + "^{}": "head"}))
}
