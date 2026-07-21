package index

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/telecollection/telecollection/internal/store"
	"github.com/telecollection/telecollection/internal/store/sqlite"
)

// newIndex returns an Index backed by a fresh, isolated SQLite store in a
// tempdir. The index is exercised entirely offline against the real store.
func newIndex(t *testing.T) *Index {
	t.Helper()
	p := filepath.ToSlash(filepath.Join(t.TempDir(), "idx.db"))
	dsn := "file:" + p + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	s, err := sqlite.Open(dsn)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return New(s)
}

func TestIndex_FolderRoundtrip(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	f, err := idx.UpsertFolder(ctx, store.Folder{TGAccountID: 1, ChannelID: 100, Name: "Docs"})
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	if f.ID == 0 {
		t.Fatal("UpsertFolder must assign a non-zero ID")
	}
	got, err := idx.ListFolders(ctx, 1)
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Docs" {
		t.Fatalf("ListFolders(1) = %+v, want exactly folder Docs", got)
	}
}

func TestIndex_FileRoundtripAndFilter(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	folder, err := idx.UpsertFolder(ctx, store.Folder{TGAccountID: 1, ChannelID: 100, Name: "Docs"})
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	other, err := idx.UpsertFolder(ctx, store.Folder{TGAccountID: 1, ChannelID: 200, Name: "Other"})
	if err != nil {
		t.Fatalf("UpsertFolder(other): %v", err)
	}

	saved, err := idx.UpsertFile(ctx, store.File{
		FolderID: folder.ID, MessageID: 1, Name: "a.pdf", Size: 10, MIME: "application/pdf",
	})
	if err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	if saved.ID == 0 || saved.CreatedAt.IsZero() {
		t.Fatalf("UpsertFile must populate ID/CreatedAt, got %+v", saved)
	}
	if _, err := idx.UpsertFile(ctx, store.File{FolderID: other.ID, MessageID: 2, Name: "b.txt", Size: 3, MIME: "text/plain"}); err != nil {
		t.Fatalf("UpsertFile(other): %v", err)
	}

	got, err := idx.ListFiles(ctx, folder.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(got) != 1 || got[0].Name != "a.pdf" {
		t.Fatalf("ListFiles(folder) = %+v, want exactly a.pdf", got)
	}
}

func TestIndex_DeleteFilePrunesRow(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	folder, err := idx.UpsertFolder(ctx, store.Folder{TGAccountID: 1, ChannelID: 100, Name: "Docs"})
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	if _, err := idx.UpsertFile(ctx, store.File{
		FolderID: folder.ID, MessageID: 5, Name: "a.pdf", Size: 10, MIME: "application/pdf",
	}); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	// Deleting the cached message must remove it from ListFiles.
	if err := idx.DeleteFile(ctx, folder.ID, 5); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	got, err := idx.ListFiles(ctx, folder.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListFiles after delete = %+v, want empty", got)
	}

	// Deleting a message that is not cached is an idempotent no-op success.
	if err := idx.DeleteFile(ctx, folder.ID, 999); err != nil {
		t.Fatalf("DeleteFile miss should be no-op: %v", err)
	}
}

func TestIndex_UpsertFileDedupes(t *testing.T) {
	idx := newIndex(t)
	ctx := context.Background()

	folder, err := idx.UpsertFolder(ctx, store.Folder{TGAccountID: 1, ChannelID: 100, Name: "Docs"})
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}

	first, err := idx.UpsertFile(ctx, store.File{FolderID: folder.ID, MessageID: 7, Name: "old", Size: 1, MIME: "text/plain"})
	if err != nil {
		t.Fatalf("UpsertFile(first): %v", err)
	}
	second, err := idx.UpsertFile(ctx, store.File{FolderID: folder.ID, MessageID: 7, Name: "new", Size: 2, MIME: "text/plain"})
	if err != nil {
		t.Fatalf("UpsertFile(second): %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("re-upsert must reuse row: first=%d second=%d", first.ID, second.ID)
	}
	got, err := idx.ListFiles(ctx, folder.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(got) != 1 || got[0].Name != "new" {
		t.Fatalf("ListFiles after re-upsert = %+v, want single 'new'", got)
	}
}
