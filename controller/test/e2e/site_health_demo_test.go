// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSiteHealthDemoCleanup(t *testing.T) {
	for _, tool := range []string{"python3", "jq", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("demo command fixture requires %s", tool)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "python3", "../../hack/site-health/test_demo.py").CombinedOutput()
	require.NoError(t, err, "%s", output)
}
