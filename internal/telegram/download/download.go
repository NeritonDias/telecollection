// Package download fetches a document stored in a TeleCollection folder (a
// Telegram broadcast channel) and streams it into an io.Writer through the
// transfer engine, verifying that the number of bytes written matches the
// document's declared size.
//
// The network-free core is documentFrom, which extracts the *tg.Document from a
// channels.getMessages reply built by File; it is unit-tested offline against
// hand-built replies. File wires that extraction to a live *tg.Client and
// transfer.DownloadTo, wrapping the destination in a byte counter so a truncated
// or over-long transfer is caught rather than reported as success.
//
// No secret material is logged. Errors are wrapped with %w.
package download

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gotd/td/tg"

	"github.com/telecollection/telecollection/internal/telegram/dialogs"
	"github.com/telecollection/telecollection/internal/transfer"
)

// File downloads the document carried by message msgID in folder into w,
// verifying the written size matches the document size. onProgress may be nil.
//
// Integrity: the document's declared Size is the known length. File wraps w in a
// countingWriter and, once transfer.DownloadTo has streamed the file, checks
// that the bytes actually written equal doc.Size. A mismatch (a short read that
// the downloader surfaced as success, or a retry that re-streamed extra bytes
// into w) fails the call, so a corrupt or partial download is never mistaken for
// a complete one.
func File(ctx context.Context, api *tg.Client, folder dialogs.Folder, msgID int, w io.Writer, onProgress func(transfer.Progress)) error {
	if api == nil {
		return errors.New("download: api client is required")
	}
	if w == nil {
		return errors.New("download: writer is required")
	}
	if msgID <= 0 {
		return fmt.Errorf("download: message id must be positive, got %d", msgID)
	}

	res, err := api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{
			ChannelID:  folder.ChannelID,
			AccessHash: folder.AccessHash,
		},
		ID: []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
	})
	if err != nil {
		return fmt.Errorf("download: fetching message %d: %w", msgID, err)
	}

	doc, err := documentFrom(res, msgID)
	if err != nil {
		return err
	}

	loc := doc.AsInputDocumentFileLocation("")

	cw := &countingWriter{w: w}
	if err := transfer.DownloadTo(ctx, api, loc, doc.Size, cw, onProgress); err != nil {
		return fmt.Errorf("download: message %d: %w", msgID, err)
	}
	if cw.n != doc.Size {
		return fmt.Errorf("download: integrity check failed for message %d: wrote %d bytes, expected %d", msgID, cw.n, doc.Size)
	}
	return nil
}

// documentFrom extracts the *tg.Document attached to message msgID from a
// channels.getMessages reply. It is network-free: it only inspects the decoded
// reply, which makes it unit-testable without a live Telegram connection.
//
// It returns a clear error when the reply carries no usable messages
// (messages.messagesNotModified), when the requested message is absent or is an
// empty/service message, when the message has no media, or when the media is not
// a non-empty document.
func documentFrom(res tg.MessagesMessagesClass, msgID int) (*tg.Document, error) {
	modified, ok := res.AsModified()
	if !ok {
		return nil, fmt.Errorf("download: unexpected messages reply %T for message %d", res, msgID)
	}

	var msg *tg.Message
	for _, mc := range modified.GetMessages() {
		m, ok := mc.(*tg.Message)
		if ok && m.ID == msgID {
			msg = m
			break
		}
	}
	if msg == nil {
		return nil, fmt.Errorf("download: message %d not found", msgID)
	}

	media, ok := msg.GetMedia()
	if !ok {
		return nil, fmt.Errorf("download: message %d has no media", msgID)
	}
	docMedia, ok := media.(*tg.MessageMediaDocument)
	if !ok {
		return nil, fmt.Errorf("download: message %d media is %T, not a document", msgID, media)
	}
	docClass, ok := docMedia.GetDocument()
	if !ok {
		return nil, fmt.Errorf("download: message %d has no document", msgID)
	}
	doc, ok := docClass.(*tg.Document)
	if !ok {
		return nil, fmt.Errorf("download: message %d document is %T, not a full document", msgID, docClass)
	}
	return doc, nil
}

// countingWriter forwards writes to w unchanged while accumulating the number of
// bytes the sink accepted. It counts only bytes the underlying Write reports as
// written, so a partial or failing write is reflected honestly in n.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
