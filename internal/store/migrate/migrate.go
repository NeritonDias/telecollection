// Package migrate applies database migrations for both dialects via goose.
// Migrations live in per-dialect subdirs (sqlite/, postgres/) with the same
// logical shape but dialect-specific SQL (AUTOINCREMENT vs BIGSERIAL, etc).
//
// NOTE: goose dialect for SQLite is "sqlite3", while the modernc driver name is
// "sqlite" — do not confuse them.
package migrate

import (
	"database/sql"
	"embed"
	"fmt"
	"sync"

	"github.com/pressly/goose/v3"
)

// goose keeps process-global state (SetBaseFS/SetDialect); serialize access so a
// concurrent UpSQLite/UpPostgres in the same process cannot interleave.
var gooseMu sync.Mutex

//go:embed sqlite/*.sql
var sqliteFS embed.FS

//go:embed postgres/*.sql
var postgresFS embed.FS

// UpSQLite applies all pending SQLite migrations.
func UpSQLite(db *sql.DB) error { return up(db, sqliteFS, "sqlite", "sqlite3") }

// UpPostgres applies all pending Postgres migrations.
func UpPostgres(db *sql.DB) error { return up(db, postgresFS, "postgres", "postgres") }

func up(db *sql.DB, fsys embed.FS, dir, dialect string) error {
	gooseMu.Lock()
	defer gooseMu.Unlock()
	goose.SetBaseFS(fsys)
	if err := goose.SetDialect(dialect); err != nil {
		return fmt.Errorf("migrate: set dialect %q: %w", dialect, err)
	}
	if err := goose.Up(db, dir); err != nil {
		return fmt.Errorf("migrate: up (%s): %w", dialect, err)
	}
	return nil
}
