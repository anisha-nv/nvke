// Copyright (c) 2026, NVIDIA CORPORATION & AFFILIATES.  All rights reserved.

// Command hello is a trivial entrypoint that prints a greeting produced by the
// internal greeting package. It gives the Phase 2 CI POC a `main` package for
// `go build ./...` to compile.
package main

import (
	"fmt"

	"github.com/NVIDIA/nke/internal/greeting"
)

func main() {
	fmt.Println(greeting.Greet("NKE"))
}
