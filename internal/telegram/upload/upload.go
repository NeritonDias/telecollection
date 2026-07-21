// Package upload sends a file into a TeleCollection folder as a Telegram
// document.
//
// It is the file-level layer on top of internal/transfer: transfer streams the
// bytes and yields a gotd InputFile, and this package attaches that InputFile
// to a message in the folder's channel via gotd's message.Sender, then reads
// the resulting metadata (message id, document size and MIME) back out of the
// Updates the server returns.
//
// The store.File returned carries MessageID (the anchor into Telegram, which is
// the source of truth) but leaves FolderID at zero: the numeric folder id is a
// property of the metadata index (internal/index), not of Telegram, so it is
// filled in by the index layer when the file is recorded. The channel the file
// lives in is identified by the folder passed in, not by store.File.FolderID.
//
// No secret material is logged. Errors are wrapped with %w.
package upload

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"

	"github.com/telecollection/telecollection/internal/store"
	"github.com/telecollection/telecollection/internal/telegram/dialogs"
	"github.com/telecollection/telecollection/internal/transfer"
)

// defaultMIME is used when the caller does not supply a MIME type, so the
// document is still sent with a valid, generic content type.
const defaultMIME = "application/octet-stream"

// File uploads exactly size bytes read from r and sends them as a document into
// folder, returning the resulting file metadata. name is used as the document's
// filename and mime as its content type (defaulting to application/octet-stream
// when empty). onProgress, if non-nil, is invoked as the bytes are uploaded,
// finishing with a report where Done == Total.
//
// The document is sent with ForceFile(true) so Telegram stores it as a plain
// downloadable file rather than reinterpreting it as a photo, video or other
// rich media. The returned store.File has FolderID == 0 by design (see the
// package comment); MessageID together with folder.ChannelID locate the file.
// Size and MIME are resolved from the document Telegram echoed back, falling
// back to the caller-provided values when the server omits them.
func File(
	ctx context.Context,
	api *tg.Client,
	folder dialogs.Folder,
	name string,
	r io.Reader,
	size int64,
	mime string,
	onProgress func(transfer.Progress),
) (store.File, error) {
	if api == nil {
		return store.File{}, errors.New("upload: api client is required")
	}
	if r == nil {
		return store.File{}, errors.New("upload: reader is required")
	}
	if size < 0 {
		return store.File{}, fmt.Errorf("upload: size must be non-negative, got %d", size)
	}
	if name == "" {
		return store.File{}, errors.New("upload: file name is required")
	}

	mime = orDefaultMIME(mime)

	inputFile, err := transfer.UploadBytes(ctx, api, name, r, size, onProgress)
	if err != nil {
		return store.File{}, fmt.Errorf("upload: streaming %q: %w", name, err)
	}

	doc := message.UploadedDocument(inputFile).
		Filename(name).
		MIME(mime).
		ForceFile(true)

	peer := &tg.InputPeerChannel{ChannelID: folder.ChannelID, AccessHash: folder.AccessHash}
	upd, err := message.NewSender(api).To(peer).Media(ctx, doc)
	if err != nil {
		return store.File{}, fmt.Errorf("upload: sending document %q: %w", name, err)
	}

	sent, err := parseSentDocument(upd)
	if err != nil {
		return store.File{}, err
	}

	return store.File{
		MessageID: sent.messageID,
		Name:      name,
		Size:      preferServer(sent.size, size),
		MIME:      preferServerMIME(sent.mime, mime),
	}, nil
}

// sentDocument holds the metadata extracted from the Updates returned by
// messages.sendMedia: the id of the message that now carries the document and
// the document's own size and MIME type as Telegram resolved them.
type sentDocument struct {
	messageID int64
	size      int64
	mime      string
}

// parseSentDocument walks an Updates envelope for the new message that carries
// the just-sent document and extracts its id, size and MIME. It works on a
// value built by hand (no network), so it is unit-testable offline.
//
// It accepts any Updates variant exposing GetUpdates (updates and
// updatesCombined), scans for an updateNewMessage / updateNewChannelMessage
// whose message is a concrete *tg.Message carrying a messageMediaDocument with
// a concrete *tg.Document, and returns that. Anything else is an error, since a
// successful document send must echo the stored document back.
func parseSentDocument(upd tg.UpdatesClass) (sentDocument, error) {
	if upd == nil {
		return sentDocument{}, errors.New("upload: nil updates from send")
	}
	provider, ok := upd.(interface{ GetUpdates() []tg.UpdateClass })
	if !ok {
		return sentDocument{}, fmt.Errorf("upload: unexpected updates type %T without update list", upd)
	}

	for _, u := range provider.GetUpdates() {
		msgHolder, ok := u.(interface{ GetMessage() tg.MessageClass })
		if !ok {
			continue
		}
		msg, ok := msgHolder.GetMessage().(*tg.Message)
		if !ok {
			continue
		}
		mediaClass, ok := msg.GetMedia()
		if !ok {
			continue
		}
		docMedia, ok := mediaClass.(*tg.MessageMediaDocument)
		if !ok {
			continue
		}
		docClass, ok := docMedia.GetDocument()
		if !ok {
			continue
		}
		doc, ok := docClass.(*tg.Document)
		if !ok {
			// documentEmpty or another variant: the send did not yield a
			// usable stored document.
			continue
		}
		return sentDocument{
			messageID: int64(msg.GetID()),
			size:      doc.GetSize(),
			mime:      doc.GetMimeType(),
		}, nil
	}

	return sentDocument{}, errors.New("upload: sent document not found in updates")
}

// orDefaultMIME returns a usable MIME type, substituting the generic default
// for an empty caller value.
func orDefaultMIME(mime string) string {
	if mime == "" {
		return defaultMIME
	}
	return mime
}

// preferServerMIME prefers the MIME Telegram resolved, falling back to the
// value we sent when the server echoes an empty type.
func preferServerMIME(fromServer, fallback string) string {
	if fromServer != "" {
		return fromServer
	}
	return fallback
}

// preferServer prefers the size Telegram resolved, falling back to the
// requested size when the server reports a non-positive value.
func preferServer(fromServer, fallback int64) int64 {
	if fromServer > 0 {
		return fromServer
	}
	return fallback
}
