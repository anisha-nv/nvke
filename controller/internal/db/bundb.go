// SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
	"github.com/uptrace/bun/extra/bunotel"
	"github.com/uptrace/bun/extra/bunslog"
	"github.com/uptrace/bun/schema"
	"go.opentelemetry.io/otel/trace"

	"github.com/dsx-ai-factory/nvke/controller/internal/config"
)

// queryLogFormat returns the attribute builder for query events, including
// PostgreSQL protocol fields (SQLSTATE, severity, message) when the error is a
// pgdriver.Error.
//
// The query carries its values only when logValues is set. A failed or slow
// statement is logged at error or warn, and its values can be operator-supplied
// content such as cloud-init files, so by default it is logged with a "?" for
// each value, as the tracing hook records it.
func queryLogFormat(logValues bool) func(*bun.QueryEvent) []slog.Attr {
	return func(event *bun.QueryEvent) []slog.Attr {
		query := placeholderQuery(event)
		if logValues {
			query = event.Query
		}
		duration := time.Since(event.StartTime)
		attrs := []slog.Attr{
			slog.String("operation", event.Operation()),
			slog.String("query", query),
			slog.String("duration", duration.String()),
		}
		if event.Err != nil {
			attrs = append(attrs, slog.Any("error", event.Err))

			// Add PostgreSQL protocol fields when err wraps pgdriver.Error
			var pgErr pgdriver.Error
			if errors.As(event.Err, &pgErr) {
				if sqlstate := pgErr.Field('C'); sqlstate != "" {
					attrs = append(attrs,
						slog.String("pg_sqlstate", sqlstate),
						slog.String("pg_severity", pgErr.Field('S')),
						slog.String("pg_message", pgErr.Field('M')),
						slog.Bool("pg_statement_timeout", pgErr.StatementTimeout()),
					)
				}
			}
		}
		return attrs
	}
}

// placeholderQuery returns the SQL of event with a "?" where each value was.
//
// For ORM queries and bun.NewRaw, bun puts the values into both Query and
// QueryTemplate, so the SQL is rebuilt from the query object without formatting
// its arguments, as bunotel does for the db.statement span attribute. A query
// run with db.ExecContext has no query object and its template already has the
// placeholders. When the SQL cannot be rebuilt nothing is returned: falling
// back to either string would log the values.
func placeholderQuery(event *bun.QueryEvent) string {
	if event.IQuery == nil {
		return event.QueryTemplate
	}
	b, err := event.IQuery.AppendQuery(schema.NewNopQueryGen(), nil)
	if err != nil {
		return ""
	}
	return string(b)
}

// parentedTracingHook prevents background queries from becoming standalone
// root traces. Embedding preserves Bun's Init and AfterQuery behavior, including
// database metrics for queries that have no trace parent.
type parentedTracingHook struct {
	*bunotel.QueryHook
}

func (h *parentedTracingHook) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	return h.QueryHook.BeforeQuery(ctx, event)
}

// newTracingHook labels spans with the database actually connected to, so
// sites that set a different appDb.dbName are not reported as cloud-workflow.
func newTracingHook(connector *pgdriver.Connector, opts ...bunotel.Option) *parentedTracingHook {
	return &parentedTracingHook{
		QueryHook: bunotel.NewQueryHook(append([]bunotel.Option{bunotel.WithDBName(connector.Config().Database)}, opts...)...),
	}
}

// NewBunDB creates bun database object based on configuration.
func NewBunDB(cfg config.Config, logger *slog.Logger) (*bun.DB, error) {
	var db *bun.DB

	dsn, err := cfg.EffectiveDBConnectionString()
	if err != nil {
		return nil, fmt.Errorf("resolve DB connection string: %w", err)
	}
	connector := pgdriver.NewConnector(pgdriver.WithDSN(dsn))
	sqldb := sql.OpenDB(connector)

	// https://bun.uptrace.dev/guide/running-bun-in-production.html#running-bun-in-production
	// Note: The bun docs show 4 * GOMAXPROCS(0) as the default max open connections, however this causes
	// problems as our uber/automaxprocs library sets GOMAXPROCS to 128. 128 * 4 = 512, which is far greater
	// than our current PG limit of 100.
	sqldb.SetMaxOpenConns(cfg.DBMaxOpenConnections)
	sqldb.SetMaxIdleConns(cfg.DBMaxOpenConnections)

	db = bun.NewDB(sqldb, pgdialect.New())

	queryLogLevel := slog.LevelDebug
	if cfg.DBQueryLogLevel == "info" {
		queryLogLevel = slog.LevelInfo
	}

	db = db.WithQueryHook(bunslog.NewQueryHook(
		bunslog.WithQueryLogLevel(queryLogLevel),
		bunslog.WithSlowQueryLogLevel(slog.LevelWarn),
		bunslog.WithErrorQueryLogLevel(slog.LevelError),
		bunslog.WithSlowQueryThreshold(3*time.Second),
		bunslog.WithLogger(logger),
		bunslog.WithLogFormat(queryLogFormat(cfg.DBQueryLogValues)),
	))

	if cfg.Tracing.Enabled {
		db = db.WithQueryHook(newTracingHook(connector))
	}

	if err := db.PingContext(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return db, nil
}
