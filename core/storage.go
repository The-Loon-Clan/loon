package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// StorageService owns the shared DB pool + schema-scoped
// accessors. The pool itself is intentionally shared across
// plugins (one Postgres database, many schemas — see
// PLUGIN-ARCHITECTURE.md § 6) so connection budgeting stays a
// single tuning knob.
//
// SchemaDB scopes search_path to the plugin schema so a plugin
// can write `SELECT * FROM threads` rather than
// `SELECT * FROM forum.threads`. The fully-qualified form is
// still allowed for cross-schema reads (e.g. JOIN to core.users).
type StorageService interface {
	// DB returns the raw shared connection pool. Use this for
	// cross-schema reads (joining a plugin schema against
	// public.users, for example).
	DB() *sqlx.DB

	// SchemaDB returns a *SchemaDB scoped to the named plugin
	// schema. Every query through that wrapper runs with
	// `SET LOCAL search_path = <schema>, public` so unqualified
	// table references resolve inside the plugin's schema first.
	SchemaDB(plugin string) *SchemaDB

	// NoRowsAsNil collapses sql.ErrNoRows to a nil error so the
	// caller's contract becomes "return zero value if nothing
	// matches" rather than "return a typed not-found error".
	// Re-exported so plugins don't have to import pkg/storage.
	NoRowsAsNil(err error) error
}

// SchemaDB is a thin wrapper around *sqlx.DB that arranges for
// search_path to be scoped to the plugin schema for every
// connection-bound operation.
//
// The simplest correct way to do that in Postgres is to run
// `SET LOCAL search_path = <schema>, public` inside a
// transaction (the `LOCAL` is the important bit — it confines
// the change to that one transaction so we never pollute the
// pool). All write paths therefore go through Tx() / WithTx();
// quick read-only queries use ExecContext / QueryContext which
// run a one-shot `SET search_path` per call.
//
// In Phase 0 no plugin actually uses this wrapper (no plugins
// are registered) — the surface is here so that when the wiki
// plugin lands in Phase 1 it can be migrated with no further
// scaffolding work.
type SchemaDB struct {
	db     *sqlx.DB
	schema string
}

// DB returns the underlying *sqlx.DB. Useful for tests that want
// to assert against the same pool, or for plugin code that needs
// a feature the wrapper does not yet expose (LISTEN/NOTIFY,
// COPY, etc.). When you use this directly, you are responsible
// for setting search_path yourself if you want unqualified
// table names to resolve inside the plugin schema.
func (s *SchemaDB) DB() *sqlx.DB { return s.db }

// Schema returns the schema name this wrapper is scoped to.
func (s *SchemaDB) Schema() string { return s.schema }

// WithTx runs fn inside a transaction with search_path scoped to
// the plugin schema. The transaction is committed on a nil
// return value from fn, rolled back otherwise. The fn body
// should only use the supplied *sqlx.Tx — using s.DB() inside
// will get the unscoped connection.
func (s *SchemaDB) WithTx(ctx context.Context, fn func(*sqlx.Tx) error) error {
	if s == nil || s.db == nil {
		return errors.New("core: SchemaDB not initialised")
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	// sqllint:allow schema name is a compile-time identifier (the Caddy-style RegisterPlugin key); quoteIdent doubles any quotes.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL search_path = %s, public", quoteIdent(s.schema))); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// storageAdapter is the default StorageService impl. The host
// constructs one in cmd/main.go from the live *sqlx.DB.
type storageAdapter struct {
	db *sqlx.DB
}

// NewStorage constructs a StorageService over the given pool.
// Passing nil is allowed for tests; methods that would otherwise
// hit the pool return zero values without panicking.
func NewStorage(db *sqlx.DB) StorageService { return &storageAdapter{db: db} }

func (s *storageAdapter) DB() *sqlx.DB { return s.db }

func (s *storageAdapter) SchemaDB(plugin string) *SchemaDB {
	return &SchemaDB{db: s.db, schema: plugin}
}

// NoRowsAsNil is the local re-implementation of the pkg/storage
// helper of the same name. Reproduced here so pkg/core has no
// outbound dependency on pkg/storage.
func (s *storageAdapter) NoRowsAsNil(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// quoteIdent is a minimal SQL-identifier quoter for the
// search_path SET LOCAL above. We need it because we can't
// pull lib/pq's QuoteIdentifier without making pkg/core import
// a driver package; the rule here is the same as pq's
// (double-quote, escape embedded quotes by doubling them) and
// it runs against plugin names which are themselves validated
// to be [a-z0-9_] at registration time, so the surface area is
// tiny.
func quoteIdent(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			out = append(out, '"', '"')
			continue
		}
		out = append(out, s[i])
	}
	out = append(out, '"')
	return string(out)
}
