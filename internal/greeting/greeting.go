// Copyright (c) 2026, NVIDIA CORPORATION & AFFILIATES.  All rights reserved.

// Package greeting provides a small, pure helper for composing greeting
// strings. It exists solely to give the Phase 2 CI POC a real Go module to
// build, test, and lint — it carries no product logic.
package greeting

import "fmt"

// Greet returns a friendly greeting addressed to name. An empty name falls
// back to "world" so callers always receive non-empty output.
func Greet(name string) string {
	if name == "" {
		name = "world"
	}
	return fmt.Sprintf("Hello, %s!", name)
}
