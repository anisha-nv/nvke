// Copyright (c) 2026, NVIDIA CORPORATION & AFFILIATES.  All rights reserved.

package dbsmoke

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// envDSN is the environment variable this smoke test reads its postgres
// connection string from. It stands in for cloud-workflow-engine's
// CWE_GOFAIL_E2E_DB_CONNECTION_STRING: the `make e2e-gofail` lane reads its DSN
// from an env var exactly like this, so proving the env-driven contract here
// de-risks that migration.
const envDSN = "DBSMOKE_DSN"

// TestDBSmoke opens the postgres service container named by DBSMOKE_DSN and
// runs `SELECT 1`. When DBSMOKE_DSN is unset it skips, so `go test ./...` in
// the hermetic unit-test lane stays green without any database. The
// service-smoke job in .github/workflows/qualification.yaml is what sets
// DBSMOKE_DSN (to the GitHub-hosted 127.0.0.1:<mapped-port> form).
func TestDBSmoke(t *testing.T) {
	dsn := os.Getenv(envDSN)
	if dsn == "" {
		t.Skipf("%s not set; skipping postgres service-container smoke test", envDSN)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Parse first so we can log the host/port we actually dialed without
	// echoing the password. This is the load-bearing evidence for Phase 3:
	// on GitHub-hosted runners the host must be 127.0.0.1 on the mapped port,
	// NOT a GitLab-style service hostname such as "postgres".
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgx.ParseConfig: %v", err)
	}

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgx.ConnectConfig to %s:%d/%s: %v", cfg.Host, cfg.Port, cfg.Database, err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var got int
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(&got); err != nil {
		t.Fatalf("SELECT 1 against %s:%d/%s: %v", cfg.Host, cfg.Port, cfg.Database, err)
	}
	if got != 1 {
		t.Fatalf("SELECT 1 = %d, want 1", got)
	}

	t.Logf("SELECT 1 succeeded against %s:%d/%s (host/port form — the GitHub-hosted 127.0.0.1:<mapped-port> shape, not a GitLab service hostname)",
		cfg.Host, cfg.Port, cfg.Database)
}
