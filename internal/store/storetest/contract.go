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
}
