package migrate

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestUpSQLite_CreatesSchema(t *testing.T) {
	p := filepath.ToSlash(filepath.Join(t.TempDir(), "m.db"))
	db, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	if err := UpSQLite(db); err != nil {
		t.Fatalf("UpSQLite: %v", err)
	}

	// The migration succeeded only if the table now accepts inserts.
	if _, err := db.Exec(`INSERT INTO folders (tg_account_id, channel_id, name) VALUES (1, 1, 'x')`); err != nil {
		t.Fatalf("insert after migration failed (table missing?): %v", err)
	}

	// Idempotent: running Up again is a no-op, not an error.
	if err := UpSQLite(db); err != nil {
		t.Fatalf("second UpSQLite: %v", err)
	}
}
