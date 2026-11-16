package backend

import (
	"context"
	"database/sql"
	"fmt"
)

// Migrate applies ordered migrations atomically under a database lock.
func Migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(7392041)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	migrations := []string{Schema, `ALTER TABLE samples ADD COLUMN app_process_id TEXT NOT NULL DEFAULT '' CHECK(octet_length(app_process_id)<=128)`, heartbeatSchema, applicationHealthSchema, regressionSchema, evidenceSchema}
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema is newer than backend")
	}
	for i, m := range migrations {
		if i+1 <= version {
			continue
		}
		if _, err = tx.ExecContext(ctx, m); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, i+1); err != nil {
			return err
		}
	}
	return tx.Commit()
}
