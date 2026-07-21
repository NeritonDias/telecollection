package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/telecollection/telecollection/internal/store"
	"github.com/telecollection/telecollection/internal/store/storetest"
)

// Set TELECOL_TEST_PG_DSN (e.g. from a CI Postgres service container) to run the
// full Store contract against Postgres. Skipped when unset.
func TestPostgres_Contract(t *testing.T) {
	dsn := os.Getenv("TELECOL_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set TELECOL_TEST_PG_DSN to run the postgres contract")
	}
	ctx := context.Background()

	storetest.Contract(t, func(t *testing.T) store.Store {
		s, err := Open(ctx, dsn)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		// Fresh state per subtest (single shared DB).
		if _, err := s.pool.Exec(ctx, "TRUNCATE files, folders RESTART IDENTITY"); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
