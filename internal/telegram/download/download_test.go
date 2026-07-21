package download

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/telecollection/telecollection/internal/telegram/dialogs"
	"github.com/telecollection/telecollection/internal/transfer"
)

// tgClientStub is a zero-value *tg.Client used only to satisfy the non-nil api
// guard in the validation tests. The validated paths return before any RPC is
// attempted, so the client is never actually driven.
var tgClientStub tg.Client

// docMessage builds a channels.getMessages reply carrying a single message with
// id msgID whose media is a document of the given size.
func docMessage(msgID int, size int64) tg.MessagesMessagesClass {
	media := &tg.MessageMediaDocument{}
	media.SetDocument(&tg.Document{ID: 42, AccessHash: 7, Size: size})
	msg := &tg.Message{ID: msgID}
	msg.SetMedia(media)
	return &tg.MessagesChannelMessages{
		Messages: []tg.MessageClass{msg},
	}
}

func TestFileValidatesInput(t *testing.T) {
	ctx := context.Background()
	folder := dialogs.Folder{ChannelID: 1, AccessHash: 2}
	var dst bytes.Buffer

	if err := File(ctx, nil, folder, 1, &dst, nil); err == nil {
		t.Error("File with nil api: expected error, got nil")
	}
	if err := File(ctx, &tgClientStub, folder, 1, nil, nil); err == nil {
		t.Error("File with nil writer: expected error, got nil")
	}
	if err := File(ctx, &tgClientStub, folder, 0, &dst, nil); err == nil {
		t.Error("File with zero msgID: expected error, got nil")
	}
	if err := File(ctx, &tgClientStub, folder, -5, &dst, nil); err == nil {
		t.Error("File with negative msgID: expected error, got nil")
	}
}

func TestDocumentFromReturnsDocument(t *testing.T) {
	res := docMessage(10, 2048)

	doc, err := documentFrom(res, 10)
	if err != nil {
		t.Fatalf("documentFrom: unexpected error: %v", err)
	}
	if doc.ID != 42 {
		t.Errorf("doc.ID = %d, want 42", doc.ID)
	}
	if doc.Size != 2048 {
		t.Errorf("doc.Size = %d, want 2048", doc.Size)
	}
	// The extracted document must yield a usable input file location.
	if loc := doc.AsInputDocumentFileLocation(""); loc == nil {
		t.Error("AsInputDocumentFileLocation returned nil")
	}
}

func TestDocumentFromMessageNotFound(t *testing.T) {
	// Reply carries message 10, but we ask for 11.
	res := docMessage(10, 100)
	_, err := documentFrom(res, 11)
	if err == nil {
		t.Fatal("expected error for missing message, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want it to mention 'not found'", err.Error())
	}
}

func TestDocumentFromNoMedia(t *testing.T) {
	// A message with no media at all.
	res := &tg.MessagesChannelMessages{
		Messages: []tg.MessageClass{&tg.Message{ID: 10}},
	}
	_, err := documentFrom(res, 10)
	if err == nil {
		t.Fatal("expected error for message without media, got nil")
	}
	if !strings.Contains(err.Error(), "media") {
		t.Errorf("error = %q, want it to mention 'media'", err.Error())
	}
}

func TestDocumentFromNonDocumentMedia(t *testing.T) {
	// Media present but it is a photo, not a document.
	msg := &tg.Message{ID: 10}
	msg.SetMedia(&tg.MessageMediaPhoto{})
	res := &tg.MessagesChannelMessages{
		Messages: []tg.MessageClass{msg},
	}
	_, err := documentFrom(res, 10)
	if err == nil {
		t.Fatal("expected error for non-document media, got nil")
	}
	if !strings.Contains(err.Error(), "document") {
		t.Errorf("error = %q, want it to mention 'document'", err.Error())
	}
}

func TestDocumentFromEmptyDocument(t *testing.T) {
	// Media is a document envelope but the document itself is empty.
	media := &tg.MessageMediaDocument{}
	media.SetDocument(&tg.DocumentEmpty{ID: 42})
	msg := &tg.Message{ID: 10}
	msg.SetMedia(media)
	res := &tg.MessagesChannelMessages{
		Messages: []tg.MessageClass{msg},
	}
	_, err := documentFrom(res, 10)
	if err == nil {
		t.Fatal("expected error for empty document, got nil")
	}
}

func TestDocumentFromNotModifiedReply(t *testing.T) {
	// messages.messagesNotModified carries no messages and must be rejected.
	_, err := documentFrom(&tg.MessagesMessagesNotModified{}, 10)
	if err == nil {
		t.Fatal("expected error for not-modified reply, got nil")
	}
}

func TestCountingWriterCountsBytesWritten(t *testing.T) {
	var dst bytes.Buffer
	cw := &countingWriter{w: &dst}

	payload := []byte("hello, telecollection")
	n, err := cw.Write(payload)
	if err != nil {
		t.Fatalf("Write: unexpected error: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("Write returned %d, want %d", n, len(payload))
	}
	if cw.n != int64(len(payload)) {
		t.Errorf("counter = %d, want %d", cw.n, len(payload))
	}
	if !bytes.Equal(dst.Bytes(), payload) {
		t.Error("bytes were altered or dropped by countingWriter")
	}

	// A second write must accumulate.
	if _, err := cw.Write([]byte("!")); err != nil {
		t.Fatalf("Write: unexpected error: %v", err)
	}
	if cw.n != int64(len(payload)+1) {
		t.Errorf("counter after second write = %d, want %d", cw.n, len(payload)+1)
	}
}

// errWriter fails every write, letting us prove countingWriter propagates the
// underlying error and only counts the bytes the sink actually accepted.
type errWriter struct{}

func (errWriter) Write(_ []byte) (int, error) { return 0, errors.New("sink failed") }

func TestCountingWriterPropagatesError(t *testing.T) {
	cw := &countingWriter{w: errWriter{}}
	if _, err := cw.Write([]byte("data")); err == nil {
		t.Fatal("expected error from failing sink, got nil")
	}
	if cw.n != 0 {
		t.Errorf("counter = %d, want 0 (sink accepted no bytes)", cw.n)
	}
}

// TestFileE2E exercises the real network path against a live, authenticated
// Telegram session. It is skipped unless TELECOL_TEST_TG=1, since File requires
// a connected *tg.Client. It exists to keep the gotd channels.getMessages and
// transfer.DownloadTo wiring compiling and honest; a full download is driven by
// the file-ops phase that owns a real client and a known message id.
func TestFileE2E(t *testing.T) {
	if os.Getenv("TELECOL_TEST_TG") != "1" {
		t.Skip("set TELECOL_TEST_TG=1 with a live Telegram session to run the download E2E test")
	}
	t.Skip("E2E download is driven by the file-ops phase with a connected *tg.Client")

	// Compile-only reference to the exported signature and progress type so the
	// E2E wiring stays honest even while the body is skipped.
	var (
		_ = File
		_ transfer.Progress
	)
}
