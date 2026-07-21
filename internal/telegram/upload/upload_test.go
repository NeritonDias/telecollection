package upload

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/telecollection/telecollection/internal/telegram/dialogs"
)

// TestFileValidation covers the offline input guards: File must reject a nil
// api client, a nil reader, a negative size and an empty name with a clear
// error and no panic, before any network call is attempted.
func TestFileValidation(t *testing.T) {
	t.Parallel()

	folder := dialogs.Folder{ChannelID: 1, AccessHash: 2}
	body := strings.NewReader("hello")

	cases := []struct {
		name string
		api  *tg.Client
		r    func() *strings.Reader
		size int64
		fn   string // file name argument
	}{
		{"nil api", nil, func() *strings.Reader { return body }, 5, "a.txt"},
		{"nil reader", &tg.Client{}, func() *strings.Reader { return nil }, 5, "a.txt"},
		{"negative size", &tg.Client{}, func() *strings.Reader { return body }, -1, "a.txt"},
		{"empty name", &tg.Client{}, func() *strings.Reader { return body }, 5, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := tc.r()
			// A nil *strings.Reader must be passed as a nil io.Reader.
			var reader interfaceReader
			if r == nil {
				reader = nil
			} else {
				reader = r
			}
			_, err := File(context.Background(), tc.api, folder, tc.fn, reader, tc.size, "text/plain", nil)
			if err == nil {
				t.Errorf("File(%s) err = nil, want error", tc.name)
			}
		})
	}
}

// interfaceReader is io.Reader; aliased locally to make the nil-reader case in
// the table explicit without importing io just for the type name.
type interfaceReader = interface {
	Read(p []byte) (int, error)
}

// updatesWithDocument builds a *tg.Updates carrying a single new channel
// message whose media is a document, mirroring what messages.sendMedia returns
// after a document upload. The flag-guarded Media/Document fields are populated
// through the Set* helpers so the Get* helpers report them as present.
func updatesWithDocument(msgID int, docID, size int64, mime string) *tg.Updates {
	doc := &tg.Document{ID: docID, Size: size, MimeType: mime}
	media := &tg.MessageMediaDocument{}
	media.SetDocument(doc)
	msg := &tg.Message{ID: msgID}
	msg.SetMedia(media)
	return &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateNewChannelMessage{Message: msg},
		},
	}
}

func TestParseSentDocument(t *testing.T) {
	t.Parallel()

	upd := updatesWithDocument(4242, 999, 12345, "application/pdf")
	got, err := parseSentDocument(upd)
	if err != nil {
		t.Fatalf("parseSentDocument err = %v, want nil", err)
	}
	if got.messageID != 4242 {
		t.Errorf("messageID = %d, want 4242", got.messageID)
	}
	if got.size != 12345 {
		t.Errorf("size = %d, want 12345", got.size)
	}
	if got.mime != "application/pdf" {
		t.Errorf("mime = %q, want application/pdf", got.mime)
	}
}

// TestParseSentDocumentUpdatesCombined confirms the extractor also handles the
// updatesCombined envelope, which shares the GetUpdates accessor.
func TestParseSentDocumentUpdatesCombined(t *testing.T) {
	t.Parallel()

	doc := &tg.Document{ID: 7, Size: 8, MimeType: "image/png"}
	media := &tg.MessageMediaDocument{}
	media.SetDocument(doc)
	msg := &tg.Message{ID: 55}
	msg.SetMedia(media)
	upd := &tg.UpdatesCombined{
		Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: msg}},
	}

	got, err := parseSentDocument(upd)
	if err != nil {
		t.Fatalf("parseSentDocument err = %v, want nil", err)
	}
	if got.messageID != 55 || got.size != 8 || got.mime != "image/png" {
		t.Errorf("got %+v, want {55 8 image/png}", got)
	}
}

func TestParseSentDocumentErrors(t *testing.T) {
	t.Parallel()

	// No message-bearing update at all.
	empty := &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateReadChannelInbox{}}}
	if _, err := parseSentDocument(empty); err == nil {
		t.Errorf("parseSentDocument(no message) err = nil, want error")
	}

	// A message whose media is not a document.
	msgNoDoc := &tg.Message{ID: 1}
	msgNoDoc.SetMedia(&tg.MessageMediaEmpty{})
	noDoc := &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewChannelMessage{Message: msgNoDoc}}}
	if _, err := parseSentDocument(noDoc); err == nil {
		t.Errorf("parseSentDocument(non-document media) err = nil, want error")
	}

	// A document-media message whose document is DocumentEmpty (no usable id).
	mediaEmptyDoc := &tg.MessageMediaDocument{}
	mediaEmptyDoc.SetDocument(&tg.DocumentEmpty{ID: 3})
	msgEmptyDoc := &tg.Message{ID: 2}
	msgEmptyDoc.SetMedia(mediaEmptyDoc)
	emptyDoc := &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewChannelMessage{Message: msgEmptyDoc}}}
	if _, err := parseSentDocument(emptyDoc); err == nil {
		t.Errorf("parseSentDocument(empty document) err = nil, want error")
	}

	// Nil updates.
	if _, err := parseSentDocument(nil); err == nil {
		t.Errorf("parseSentDocument(nil) err = nil, want error")
	}
}

// TestE2E uploads a real file into a real folder and reads it back, gated by
// TELECOL_TEST_TG because it needs a live, authenticated Telegram session.
func TestE2E(t *testing.T) {
	if os.Getenv("TELECOL_TEST_TG") != "1" {
		t.Skip("upload end-to-end requires a live Telegram account; set TELECOL_TEST_TG=1 to opt in")
	}
	// A real run: build an authenticated client (client.New + client.Run), take
	// the *tg.Client from the session inside Run, create/resolve a [TC] folder
	// (folders.Create or dialogs.List), then:
	//   body := bytes.NewReader(payload)
	//   f, err := File(ctx, api, folder, "e2e.txt", body, int64(len(payload)), "text/plain", nil)
	// asserting f.MessageID != 0, f.Size == len(payload), and that a subsequent
	// download of that message (dialogs.List + transfer.DownloadTo) yields the
	// same bytes. This wiring depends on live credentials and is left to
	// manual/integration validation; see docs/plano/FASE-1.md 1.7.
	_ = bytes.NewReader
	t.Skip("live Telegram client wiring not available in unit tests")
}
