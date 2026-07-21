// Package storetest provides a reusable behavioural contract that every
// store.Store implementation must satisfy. Concrete implementations (sqlite,
// postgres) call Contract from their own tests, so behaviour is defined once.
package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/telecollection/telecollection/internal/store"
)

// Contract runs the shared Store behaviour suite against a fresh store produced
// by newStore. newStore must return an isolated, empty store per call.
func Contract(t *testing.T, newStore func(t *testing.T) store.Store) {
	t.Helper()

	t.Run("Ping", func(t *testing.T) {
		s := newStore(t)
		if err := s.Ping(context.Background()); err != nil {
			t.Fatalf("Ping: %v", err)
		}
	})

	t.Run("CreateAndGetFolder", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		created, err := s.CreateFolder(ctx, store.Folder{TGAccountID: 1, ChannelID: 100, Name: "Docs"})
		if err != nil {
			t.Fatalf("CreateFolder: %v", err)
		}
		if created.ID == 0 {
			t.Fatal("CreateFolder must assign a non-zero ID")
		}
		got, err := s.GetFolder(ctx, created.ID)
		if err != nil {
			t.Fatalf("GetFolder: %v", err)
		}
		if got.Name != "Docs" || got.ChannelID != 100 {
			t.Fatalf("GetFolder returned %+v, want name=Docs channel=100", got)
		}
	})

	t.Run("GetFolderNotFound", func(t *testing.T) {
		s := newStore(t)
		_, err := s.GetFolder(context.Background(), 999999)
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetFolder(missing) error = %v, want ErrNotFound", err)
		}
	})

	t.Run("CreateFolderIsIdempotentPerChannel", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		first, err := s.CreateFolder(ctx, store.Folder{TGAccountID: 1, ChannelID: 42, Name: "old"})
		if err != nil {
			t.Fatalf("CreateFolder(first): %v", err)
		}
		second, err := s.CreateFolder(ctx, store.Folder{TGAccountID: 1, ChannelID: 42, Name: "new"})
		if err != nil {
			t.Fatalf("CreateFolder(second): %v", err)
		}
		if second.ID != first.ID {
			t.Fatalf("re-create of same (account, channel) must reuse row: first=%d second=%d", first.ID, second.ID)
		}
		list, err := s.ListFolders(ctx, 1)
		if err != nil {
			t.Fatalf("ListFolders: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("want exactly 1 folder after re-create, got %d", len(list))
		}
		if list[0].Name != "new" {
			t.Fatalf("re-create did not update name: %+v", list[0])
		}
	})

	t.Run("ListFoldersFiltersByAccount", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		if _, err := s.CreateFolder(ctx, store.Folder{TGAccountID: 1, ChannelID: 1, Name: "A"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateFolder(ctx, store.Folder{TGAccountID: 2, ChannelID: 2, Name: "B"}); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListFolders(ctx, 1)
		if err != nil {
			t.Fatalf("ListFolders: %v", err)
		}
		if len(got) != 1 || got[0].Name != "A" {
			t.Fatalf("ListFolders(1) = %+v, want exactly folder A", got)
		}
	})

	t.Run("UpsertAndGetFile", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		folder, err := s.CreateFolder(ctx, store.Folder{TGAccountID: 1, ChannelID: 10, Name: "F"})
		if err != nil {
			t.Fatalf("CreateFolder: %v", err)
		}
		created, err := s.UpsertFile(ctx, store.File{
			FolderID: folder.ID, MessageID: 500, Name: "a.pdf", Size: 1234, MIME: "application/pdf",
		})
		if err != nil {
			t.Fatalf("UpsertFile: %v", err)
		}
		if created.ID == 0 {
			t.Fatal("UpsertFile must assign a non-zero ID")
		}
		if created.CreatedAt.IsZero() {
			t.Fatal("UpsertFile must populate CreatedAt")
		}
		got, err := s.GetFile(ctx, created.ID)
		if err != nil {
			t.Fatalf("GetFile: %v", err)
		}
		if got.MessageID != 500 || got.Name != "a.pdf" || got.Size != 1234 || got.MIME != "application/pdf" {
			t.Fatalf("GetFile returned %+v, want msg=500 name=a.pdf size=1234 mime=application/pdf", got)
		}
	})

	t.Run("UpsertFileIsIdempotentPerMessage", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		folder, err := s.CreateFolder(ctx, store.Folder{TGAccountID: 1, ChannelID: 10, Name: "F"})
		if err != nil {
			t.Fatalf("CreateFolder: %v", err)
		}
		first, err := s.UpsertFile(ctx, store.File{
			FolderID: folder.ID, MessageID: 7, Name: "old.txt", Size: 1, MIME: "text/plain",
		})
		if err != nil {
			t.Fatalf("UpsertFile(first): %v", err)
		}
		second, err := s.UpsertFile(ctx, store.File{
			FolderID: folder.ID, MessageID: 7, Name: "new.txt", Size: 2, MIME: "text/markdown",
		})
		if err != nil {
			t.Fatalf("UpsertFile(second): %v", err)
		}
		if second.ID != first.ID {
			t.Fatalf("re-upsert of same (folder, message) must reuse row: first=%d second=%d", first.ID, second.ID)
		}
		list, err := s.ListFiles(ctx, folder.ID)
		if err != nil {
			t.Fatalf("ListFiles: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("want exactly 1 file after re-upsert, got %d", len(list))
		}
		if list[0].Name != "new.txt" || list[0].Size != 2 || list[0].MIME != "text/markdown" {
			t.Fatalf("re-upsert did not update fields: %+v", list[0])
		}
	})

	t.Run("ListFilesFiltersByFolder", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		f1, err := s.CreateFolder(ctx, store.Folder{TGAccountID: 1, ChannelID: 1, Name: "A"})
		if err != nil {
			t.Fatal(err)
		}
		f2, err := s.CreateFolder(ctx, store.Folder{TGAccountID: 1, ChannelID: 2, Name: "B"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpsertFile(ctx, store.File{FolderID: f1.ID, MessageID: 1, Name: "x", Size: 1, MIME: "text/plain"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpsertFile(ctx, store.File{FolderID: f1.ID, MessageID: 2, Name: "y", Size: 1, MIME: "text/plain"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpsertFile(ctx, store.File{FolderID: f2.ID, MessageID: 3, Name: "z", Size: 1, MIME: "text/plain"}); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListFiles(ctx, f1.ID)
		if err != nil {
			t.Fatalf("ListFiles: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("ListFiles(f1) = %d files, want 2", len(got))
		}
		for _, f := range got {
			if f.FolderID != f1.ID {
				t.Fatalf("ListFiles(f1) leaked file from folder %d", f.FolderID)
			}
		}
	})

	t.Run("GetFileNotFound", func(t *testing.T) {
		s := newStore(t)
		_, err := s.GetFile(context.Background(), 987654)
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetFile(missing) error = %v, want ErrNotFound", err)
		}
	})

	t.Run("DeleteFile", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		folder, err := s.CreateFolder(ctx, store.Folder{TGAccountID: 1, ChannelID: 10, Name: "F"})
		if err != nil {
			t.Fatalf("CreateFolder: %v", err)
		}
		created, err := s.UpsertFile(ctx, store.File{
			FolderID: folder.ID, MessageID: 42, Name: "gone.bin", Size: 9, MIME: "application/octet-stream",
		})
		if err != nil {
			t.Fatalf("UpsertFile: %v", err)
		}
		if err := s.DeleteFile(ctx, created.ID); err != nil {
			t.Fatalf("DeleteFile: %v", err)
		}
		if _, err := s.GetFile(ctx, created.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetFile after delete = %v, want ErrNotFound", err)
		}
		if err := s.DeleteFile(ctx, created.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("DeleteFile(missing) = %v, want ErrNotFound", err)
		}
	})
}
