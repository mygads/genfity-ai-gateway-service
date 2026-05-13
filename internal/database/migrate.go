// Package database provides embedded goose migrations so the service
// applies schema changes automatically on startup.
//
// Migrations live in internal/database/migrations/*.sql and are embedded
// into the binary at compile time. RunMigrations opens a short-lived
// *sql.DB via pgx/v5 stdlib, runs goose.UpContext, and closes the
// connection. The main process continues with pgxpool for runtime
// queries.
//
// Auto-migrate on startup matches the pattern used by
// genfity-order-service (see internal/db/migrate.go there). It keeps
// the CI/CD flow simple — there's no separate "migrate" step in the
// deploy pipeline, the service simply runs its migrations when it
// starts. Failed migrations crash the startup, which surfaces as a
// health-check failure and the CI/CD deployment step aborts.
package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"

	// pgx stdlib adapter — registers "pgx" as a database/sql driver.
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var embedMigrations embed.FS

// RunMigrations executes all pending goose migrations against databaseURL.
// Called from main before the HTTP server starts. Safe to run on every
// boot — goose is idempotent.
func RunMigrations(ctx context.Context, databaseURL string) error {
	goose.SetBaseFS(embedMigrations)

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose set dialect: %w", err)
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("goose open db: %w", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("goose ping db: %w", err)
	}

	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

// Status prints the current migration status to stdout. Used by ops
// scripts (`./genfity-ai-gateway migrate status`) when debugging.
func Status(ctx context.Context, databaseURL string) error {
	goose.SetBaseFS(embedMigrations)

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose set dialect: %w", err)
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("goose open db: %w", err)
	}
	defer func() { _ = db.Close() }()

	return goose.StatusContext(ctx, db, "migrations")
}
