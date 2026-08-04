// Copyright (c) 2026, NVIDIA CORPORATION & AFFILIATES.  All rights reserved.

// Package dbsmoke hosts the Phase 3 postgres service-container smoke test for
// the CI POC. The test (dbsmoke_test.go) reads a connection string from the
// DBSMOKE_DSN environment variable and runs `SELECT 1`; when the variable is
// unset it skips, so the hermetic `go test ./...` unit lane is unaffected.
//
// This mirrors how cloud-workflow-engine's `make e2e-gofail` lane consumes a
// DSN from the CWE_GOFAIL_E2E_DB_CONNECTION_STRING environment variable. The
// package intentionally carries no runtime code — this file exists only to
// give the package a non-test Go source so `go build ./...` and
// `go vet ./...` have something to compile.
package dbsmoke
