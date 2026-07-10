package core

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"
)

// RunPluginMigrations applies every registered plugin's
// migrations in topo order, after the core (legacy) migrations
// have already run.
//
// This function exists alongside the legacy pkg/storage
// migration runner rather than replacing it: the existing
// 1.sql..258.sql series stays in pkg/storage/migrations and is
// applied by the existing runner; Migration 259 creates the
// core.plugin_migrations tracking table that this function
// writes to.
//
// In Phase 0 no plugins are registered, so this function is a
// near-no-op: it confirms core.plugin_migrations exists (the
// numbered migration already created it) and returns. The first
// real call site lights up when the wiki plugin is extracted in
// Phase 1.
//
// Returning an error aborts boot — half-applied plugin
// migrations would leave the schema in a wedged state that's
// hard to recover from.
func RunPluginMigrations(ctx context.Context, db *sqlx.DB) error {
	if db == nil {
		return fmt.Errorf("core: RunPluginMigrations called with nil db")
	}
	if _, err := db.ExecContext(ctx, `
		CREATE SCHEMA IF NOT EXISTS core;
		CREATE TABLE IF NOT EXISTS core.plugin_migrations (
		    owner      TEXT        NOT NULL,
		    filename   TEXT        NOT NULL,
		    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		    PRIMARY KEY (owner, filename)
		)
	`); err != nil {
		return fmt.Errorf("core: ensure plugin_migrations table: %w", err)
	}
	plugins, err := LoadAll()
	if err != nil {
		return fmt.Errorf("core: load plugins for migration: %w", err)
	}
	for _, p := range plugins {
		m := p.Metadata()
		if err := ensurePluginSchema(ctx, db, m.Name); err != nil {
			return fmt.Errorf("core: ensure schema %q: %w", m.Name, err)
		}
		if err := applyPluginMigrations(ctx, db, m.Name, m.Migrations); err != nil {
			return fmt.Errorf("core: apply migrations for %q: %w", m.Name, err)
		}
	}
	return nil
}

// ensurePluginSchema creates the named schema if it does not
// already exist. Idempotent; safe to run on every boot.
func ensurePluginSchema(ctx context.Context, db *sqlx.DB, plugin string) error {
	// sqllint:allow plugin name is a compile-time identifier (the Caddy-style RegisterPlugin key); quoteIdent doubles any quotes.
	_, err := db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+quoteIdent(plugin))
	return err
}

// applyPluginMigrations applies every migration in the given
// embed.FS that hasn't already been recorded in
// core.plugin_migrations for this owner. The plugin's filename
// list is sorted lexicographically so "001_init.sql" runs
// before "002_…". Each migration body is wrapped in
// `SET LOCAL search_path = <plugin>, public` so unqualified
// table references resolve inside the plugin's schema.
//
// One migration per Exec: keep transactions small so a
// failure rolls back exactly that file, not the whole boot.
func applyPluginMigrations(ctx context.Context, db *sqlx.DB, owner string, fs embed.FS) error {
	files, err := readMigrationDir(fs)
	if err != nil {
		// An empty FS is the common Phase-0 case; readMigrationDir
		// distinguishes "no migrations directory" (ok) from "real
		// I/O error" (propagated).
		return err
	}
	for _, name := range files {
		applied, err := isApplied(ctx, db, owner, name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := fs.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		scoped := fmt.Sprintf("SET LOCAL search_path = %s, public;\n%s",
			quoteIdent(owner), string(body))
		tx, err := db.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, scoped); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply %s/%s: %w", owner, name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO core.plugin_migrations(owner, filename) VALUES ($1, $2)`,
			owner, name,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record %s/%s: %w", owner, name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s/%s: %w", owner, name, err)
		}
	}
	return nil
}

// readMigrationDir returns the sorted list of *.sql leaf names
// inside the embed.FS's "migrations" directory. An FS that has
// no "migrations" entry returns (nil, nil) so plugins without
// schema work compile cleanly.
func readMigrationDir(fs embed.FS) ([]string, error) {
	entries, err := fs.ReadDir("migrations")
	if err != nil {
		// embed.FS reports the missing directory with a
		// PathError; treat that as "no migrations" rather than
		// propagating. Any OTHER error (corrupt FS, etc.) still
		// propagates.
		return nil, nil //nolint:nilerr // intentional
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// isApplied returns true if (owner, filename) has a row in
// core.plugin_migrations. A no-row result is treated as
// "not applied" (the common case on a fresh DB).
func isApplied(ctx context.Context, db *sqlx.DB, owner, filename string) (bool, error) {
	var applied bool
	err := db.GetContext(ctx, &applied,
		`SELECT EXISTS (
		    SELECT 1 FROM core.plugin_migrations
		    WHERE owner=$1 AND filename=$2
		)`,
		owner, filename)
	if err != nil {
		return false, err
	}
	return applied, nil
}
