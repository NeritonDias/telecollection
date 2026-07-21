package migrate

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// Runs only when TELECOL_TEST_PG_DSN is set (CI provides a Postgres service).
// This is the symmetric counterpart to TestUpSQLite so the postgres migration
// and goose "postgres" dialect are actually exercised.
func TestUpPostgres_CreatesSchema(t *testing.T) {
	dsn := os.Getenv("TELECOL_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set TELECOL_TEST_PG_DSN to run the postgres migration test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(context.Background(), `DROP TABLE IF EXISTS files, folders, goose_db_version CASCADE`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := UpPostgres(db); err != nil {
		t.Fatalf("UpPostgres: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO folders (tg_account_id, channel_id, name) VALUES (1, 1, 'x')`); err != nil {
		t.Fatalf("insert after migration failed (table missing?): %v", err)
	}
}
