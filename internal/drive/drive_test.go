package drive

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/telecollection/telecollection/internal/index"
	"github.com/telecollection/telecollection/internal/store"
	"github.com/telecollection/telecollection/internal/store/sqlite"
	"github.com/telecollection/telecollection/internal/telegram/dialogs"
)

// fakeConn is a test double for Conn. When err is non-nil, WithAPI returns it
// without invoking fn (simulating a connection/session failure); otherwise it
// invokes fn with api (which may be nil to exercise the orchestrated packages'
// own nil-client guards).
type fakeConn struct {
	err    error
	api    *tg.Client
	called bool
}

func (c *fakeConn) WithAPI(ctx context.Context, fn func(ctx context.Context, api *tg.Client) error) error {
	c.called = true
	if c.err != nil {
		return c.err
	}
	return fn(ctx, c.api)
}

// okConn is a Conn double whose WithAPI reports a successful network operation
// without invoking fn. It lets tests exercise the post-network index bookkeeping
// (pruning/reindexing) that only runs once the Telegram call has succeeded,
// which the nil-client fakeConn cannot reach.
type okConn struct{}

func (okConn) WithAPI(_ context.Context, _ func(ctx context.Context, api *tg.Client) error) error {
	return nil
}

func newIndex(t *testing.T) *index.Index {
	t.Helper()
	p := filepath.ToSlash(filepath.Join(t.TempDir(), "t.db"))
	dsn := "file:" + p + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	s, err := sqlite.Open(dsn)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return index.New(s)
}

func sampleFolder() dialogs.Folder {
	return dialogs.Folder{ChannelID: 100, AccessHash: 7, Title: "Docs [TC]"}
}

func TestNew_ReturnsService(t *testing.T) {
	svc := New(&fakeConn{}, nil)
	if svc == nil {
		t.Fatal("New returned nil Service")
	}
}

func TestValidation_ShortCircuitsBeforeNetwork(t *testing.T) {
	ctx := context.Background()

	t.Run("CreateFolder empty name", func(t *testing.T) {
		c := &fakeConn{}
		svc := New(c, nil)
		if _, err := svc.CreateFolder(ctx, "   "); err == nil {
			t.Fatal("want error for empty folder name")
		}
		if c.called {
			t.Fatal("WithAPI must not be called when validation fails")
		}
	})

	t.Run("RenameFolder empty name", func(t *testing.T) {
		c := &fakeConn{}
		svc := New(c, nil)
		if err := svc.RenameFolder(ctx, sampleFolder(), ""); err == nil {
			t.Fatal("want error for empty new name")
		}
		if c.called {
			t.Fatal("WithAPI must not be called when validation fails")
		}
	})
}

// TestConnError_Propagates proves every network-facing method wraps and returns
// the error the Conn surfaces, without panicking and without touching the index
// (idx is nil here, so any premature dereference would crash).
func TestConnError_Propagates(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("boom")
	folder := sampleFolder()

	ops := map[string]func(Service) error{
		"ListFolders":  func(s Service) error { _, err := s.ListFolders(ctx); return err },
		"CreateFolder": func(s Service) error { _, err := s.CreateFolder(ctx, "x"); return err },
		"RenameFolder": func(s Service) error { return s.RenameFolder(ctx, folder, "x") },
		"DeleteFolder": func(s Service) error { return s.DeleteFolder(ctx, folder) },
		"UploadFile": func(s Service) error {
			_, err := s.UploadFile(ctx, folder, "a.txt", strings.NewReader("x"), 1, "text/plain", nil)
			return err
		},
		"DownloadFile": func(s Service) error { return s.DownloadFile(ctx, folder, 1, io.Discard, nil) },
		"RenameFile":   func(s Service) error { return s.RenameFile(ctx, folder, 1, "x") },
		"DeleteFile":   func(s Service) error { return s.DeleteFile(ctx, folder, 1) },
		"MoveFile":     func(s Service) error { _, err := s.MoveFile(ctx, folder, 1, folder); return err },
	}

	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			c := &fakeConn{err: sentinel}
			svc := New(c, nil)
			err := op(svc)
			if err == nil {
				t.Fatal("want error propagated from Conn")
			}
			if !errors.Is(err, sentinel) {
				t.Fatalf("error must wrap the Conn error: %v", err)
			}
			if !c.called {
				t.Fatal("WithAPI should have been invoked")
			}
		})
	}
}

// TestNilClient_NoPanic feeds a nil *tg.Client through WithAPI so the
// orchestrated Telegram packages hit their own nil-client guards. Every method
// must return a non-nil error and not panic.
func TestNilClient_NoPanic(t *testing.T) {
	ctx := context.Background()
	folder := sampleFolder()

	ops := map[string]func(Service) error{
		"ListFolders":  func(s Service) error { _, err := s.ListFolders(ctx); return err },
		"CreateFolder": func(s Service) error { _, err := s.CreateFolder(ctx, "x"); return err },
		"RenameFolder": func(s Service) error { return s.RenameFolder(ctx, folder, "x") },
		"DeleteFolder": func(s Service) error { return s.DeleteFolder(ctx, folder) },
		"UploadFile": func(s Service) error {
			_, err := s.UploadFile(ctx, folder, "a.txt", strings.NewReader("x"), 1, "text/plain", nil)
			return err
		},
		"DownloadFile": func(s Service) error { return s.DownloadFile(ctx, folder, 1, io.Discard, nil) },
		"RenameFile":   func(s Service) error { return s.RenameFile(ctx, folder, 1, "x") },
		"DeleteFile":   func(s Service) error { return s.DeleteFile(ctx, folder, 1) },
		"MoveFile":     func(s Service) error { _, err := s.MoveFile(ctx, folder, 1, folder); return err },
	}

	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			// api nil, err nil => fn runs with a nil client.
			svc := New(&fakeConn{}, nil)
			if err := op(svc); err == nil {
				t.Fatal("want error from nil Telegram client")
			}
		})
	}
}

// TestListFiles_FromIndex proves ListFiles reads the metadata index (not the
// network) and reflects what has been recorded there.
func TestListFiles_FromIndex(t *testing.T) {
	ctx := context.Background()
	idx := newIndex(t)
	// A Conn that would fail loudly if the network path were taken.
	c := &fakeConn{err: errors.New("network must not be used by ListFiles")}
	svc := New(c, idx)
	folder := sampleFolder()

	files, err := svc.ListFiles(ctx, folder)
	if err != nil {
		t.Fatalf("ListFiles (empty): %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("want empty file list, got %d", len(files))
	}
	if c.called {
		t.Fatal("ListFiles must not touch the network in Fase 2")
	}

	// Resolve the auto-created index folder and seed a file into it.
	fid := indexFolderID(ctx, t, idx, folder.ChannelID)
	if _, err := idx.UpsertFile(ctx, store.File{
		FolderID:  fid,
		MessageID: 5,
		Name:      "a.txt",
		Size:      3,
		MIME:      "text/plain",
	}); err != nil {
		t.Fatalf("seed UpsertFile: %v", err)
	}

	files, err = svc.ListFiles(ctx, folder)
	if err != nil {
		t.Fatalf("ListFiles (seeded): %v", err)
	}
	if len(files) != 1 || files[0].Name != "a.txt" || files[0].MessageID != 5 {
		t.Fatalf("unexpected files: %+v", files)
	}
}

// TestEnsureFolder_Idempotent proves the ChannelID -> index folder resolution is
// get-or-create: repeated calls for the same folder never duplicate rows.
func TestEnsureFolder_Idempotent(t *testing.T) {
	ctx := context.Background()
	idx := newIndex(t)
	svc := New(&fakeConn{err: errors.New("unused")}, idx)
	folder := sampleFolder()

	for i := 0; i < 3; i++ {
		if _, err := svc.ListFiles(ctx, folder); err != nil {
			t.Fatalf("ListFiles #%d: %v", i, err)
		}
	}

	folders, err := idx.ListFolders(ctx, indexAccountID)
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	if len(folders) != 1 {
		t.Fatalf("want exactly one indexed folder, got %d", len(folders))
	}
	if folders[0].ChannelID != folder.ChannelID || folders[0].Name != "Docs" {
		t.Fatalf("unexpected indexed folder: %+v", folders[0])
	}
}

func TestReindexRenamedFile(t *testing.T) {
	ctx := context.Background()
	idx := newIndex(t)
	svc := New(&fakeConn{}, idx).(*driveService)
	folder := dialogs.Folder{ChannelID: 200, Title: "Photos [TC]"}

	fid, err := svc.ensureFolder(ctx, folder)
	if err != nil {
		t.Fatalf("ensureFolder: %v", err)
	}
	if _, err := idx.UpsertFile(ctx, store.File{FolderID: fid.ID, MessageID: 7, Name: "old.txt", Size: 2, MIME: "text/plain"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := svc.reindexRenamedFile(ctx, folder, 7, "new.txt"); err != nil {
		t.Fatalf("reindexRenamedFile: %v", err)
	}

	files, err := idx.ListFiles(ctx, fid.ID)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 || files[0].Name != "new.txt" || files[0].Size != 2 {
		t.Fatalf("rename not reflected: %+v", files)
	}

	// A rename for a message not present in the cache is a no-op success.
	if err := svc.reindexRenamedFile(ctx, folder, 999, "x"); err != nil {
		t.Fatalf("reindex miss should be no-op: %v", err)
	}
}

func TestReindexMovedFile(t *testing.T) {
	ctx := context.Background()
	idx := newIndex(t)
	svc := New(&fakeConn{}, idx).(*driveService)
	src := dialogs.Folder{ChannelID: 300, Title: "Src [TC]"}
	dst := dialogs.Folder{ChannelID: 301, Title: "Dst [TC]"}

	srcFid, err := svc.ensureFolder(ctx, src)
	if err != nil {
		t.Fatalf("ensureFolder src: %v", err)
	}
	if _, err := idx.UpsertFile(ctx, store.File{FolderID: srcFid.ID, MessageID: 9, Name: "m.bin", Size: 11, MIME: "application/octet-stream"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := svc.reindexMovedFile(ctx, src, 9, dst, 42); err != nil {
		t.Fatalf("reindexMovedFile: %v", err)
	}

	dstFid, err := svc.ensureFolder(ctx, dst)
	if err != nil {
		t.Fatalf("ensureFolder dst: %v", err)
	}
	dstFiles, err := idx.ListFiles(ctx, dstFid.ID)
	if err != nil {
		t.Fatalf("ListFiles dst: %v", err)
	}
	if len(dstFiles) != 1 || dstFiles[0].MessageID != 42 || dstFiles[0].Name != "m.bin" || dstFiles[0].Size != 11 {
		t.Fatalf("move not reflected in dst: %+v", dstFiles)
	}

	// The origin must be pruned so ListFiles on src no longer serves the moved
	// file (stale-index fix).
	srcFiles, err := idx.ListFiles(ctx, srcFid.ID)
	if err != nil {
		t.Fatalf("ListFiles src: %v", err)
	}
	if len(srcFiles) != 0 {
		t.Fatalf("source not pruned after move: %+v", srcFiles)
	}
}

// TestDeleteFile_PrunesIndex proves that after a successful delete the cached
// row is pruned, so ListFiles no longer returns the deleted file.
func TestDeleteFile_PrunesIndex(t *testing.T) {
	ctx := context.Background()
	idx := newIndex(t)
	svc := New(okConn{}, idx).(*driveService)
	folder := sampleFolder()

	fid, err := svc.ensureFolder(ctx, folder)
	if err != nil {
		t.Fatalf("ensureFolder: %v", err)
	}
	if _, err := idx.UpsertFile(ctx, store.File{FolderID: fid.ID, MessageID: 5, Name: "a.txt", Size: 3, MIME: "text/plain"}); err != nil {
		t.Fatalf("seed UpsertFile: %v", err)
	}

	// Sanity: the file is listed before the delete.
	if files, err := svc.ListFiles(ctx, folder); err != nil || len(files) != 1 {
		t.Fatalf("pre-delete ListFiles = %+v (err %v), want exactly one file", files, err)
	}

	if err := svc.DeleteFile(ctx, folder, 5); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}

	files, err := svc.ListFiles(ctx, folder)
	if err != nil {
		t.Fatalf("post-delete ListFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("stale index: ListFiles after delete = %+v, want empty", files)
	}
}

// TestDrive_RoundTrip_E2E is the real upload->download round trip; it needs a
// live Telegram session and is skipped unless explicitly enabled.
func TestDrive_RoundTrip_E2E(t *testing.T) {
	if os.Getenv("TELECOL_TEST_TG") != "1" {
		t.Skip("set TELECOL_TEST_TG=1 to run the live Telegram round-trip test")
	}
	t.Skip("E2E harness (persistent client.Run + real folder) lands with the daemon in phase 8")
}

// indexFolderID resolves the numeric index id for a channel by scanning the
// account namespace the drive service uses.
func indexFolderID(ctx context.Context, t *testing.T, idx *index.Index, channelID int64) int64 {
	t.Helper()
	folders, err := idx.ListFolders(ctx, indexAccountID)
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	for _, f := range folders {
		if f.ChannelID == channelID {
			return f.ID
		}
	}
	t.Fatalf("channel %d not found in index", channelID)
	return 0
}
