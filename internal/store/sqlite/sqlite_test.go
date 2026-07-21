package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/telecollection/telecollection/internal/store"
	"github.com/telecollection/telecollection/internal/store/storetest"
)

func TestSQLite_Contract(t *testing.T) {
	storetest.Contract(t, func(t *testing.T) store.Store {
		p := filepath.ToSlash(filepath.Join(t.TempDir(), "t.db"))
		dsn := "file:" + p + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
		s, err := Open(dsn)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
