// Package fileops performs per-file operations on documents stored in
// TeleCollection folders (Telegram broadcast channels): renaming a file's
// display caption, deleting it, and moving it between folders.
//
// A file is anchored to a single Telegram message inside a folder's channel, so
// every operation here is expressed in terms of a folder plus a message id:
//
//   - Rename edits the message caption via messages.editMessage.
//   - Delete removes the message via channels.deleteMessages.
//   - Move forwards the message into the destination folder via
//     messages.forwardMessages and then deletes the original from the source.
//
// Move is intentionally NOT atomic: Telegram offers no transactional "move", so
// it is a forward followed by a delete. If the forward fails the source is left
// untouched. If the delete fails after a successful forward, the file exists in
// BOTH folders; Move surfaces that state as an explicit error carrying the new
// destination id so the caller can reconcile (by deleting the source message)
// rather than mistaking a duplicate for a clean move.
//
// The RPC surface is abstracted behind the unexported messenger interface, which
// *tg.Client satisfies. That keeps the exported functions concrete (they take a
// *tg.Client) while letting the orchestration logic be unit-tested offline with
// a fake. RandomID generation is injectable for the same reason: the default is
// crypto/rand backed (never time or math/rand), and tests inject a deterministic
// generator.
//
// No secret material is logged. Errors are wrapped with %w.
package fileops

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"

	"github.com/telecollection/telecollection/internal/telegram/dialogs"
)

// messenger is the slice of *tg.Client this package depends on. Abstracting it
// lets the orchestration be driven by a fake in tests without a live connection.
type messenger interface {
	MessagesEditMessage(ctx context.Context, request *tg.MessagesEditMessageRequest) (tg.UpdatesClass, error)
	ChannelsDeleteMessages(ctx context.Context, request *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error)
	MessagesForwardMessages(ctx context.Context, request *tg.MessagesForwardMessagesRequest) (tg.UpdatesClass, error)
}

// moveOptions holds the tunables for Move. randInt64 supplies the per-forward
// RandomID Telegram requires to deduplicate sends; it is injectable so tests can
// make Move deterministic.
type moveOptions struct {
	randInt64 func() int64
}

// MoveOption customises a Move call.
type MoveOption func(*moveOptions)

// WithRandomID overrides the RandomID generator used when forwarding. It exists
// mainly for deterministic testing; production callers should rely on the
// crypto/rand default.
func WithRandomID(gen func() int64) MoveOption {
	return func(o *moveOptions) {
		if gen != nil {
			o.randInt64 = gen
		}
	}
}

// defaultMoveOptions returns the options used when a caller supplies none: a
// crypto/rand backed RandomID generator. It never falls back to time or
// math/rand.
func defaultMoveOptions() moveOptions {
	return moveOptions{randInt64: cryptoRandInt64}
}

// Rename edits the document caption (the file's display name) of message msgID
// in folder to newName, via messages.editMessage.
func Rename(ctx context.Context, api *tg.Client, folder dialogs.Folder, msgID int, newName string) error {
	if api == nil {
		return errors.New("fileops: api client is required")
	}
	if msgID <= 0 {
		return fmt.Errorf("fileops: message id must be positive, got %d", msgID)
	}
	if strings.TrimSpace(newName) == "" {
		return errors.New("fileops: new name is required")
	}
	return rename(ctx, api, folder, msgID, newName)
}

// Delete removes message msgID from folder's channel, via
// channels.deleteMessages.
func Delete(ctx context.Context, api *tg.Client, folder dialogs.Folder, msgID int) error {
	if api == nil {
		return errors.New("fileops: api client is required")
	}
	if msgID <= 0 {
		return fmt.Errorf("fileops: message id must be positive, got %d", msgID)
	}
	return deleteMsg(ctx, api, folder, msgID)
}

// Move forwards message msgID from src into dst and then deletes it from src,
// returning the new message id in dst.
//
// NOTE: this is not atomic. Telegram has no transactional move, so it is a
// forward followed by a delete. If the forward fails, src is untouched and the
// returned id is 0. If the delete fails after a successful forward, the file
// exists in BOTH folders: Move then returns the new dst id together with an
// error that names that state, so the caller can reconcile by deleting the src
// message rather than treating the duplicate as a clean move.
func Move(ctx context.Context, api *tg.Client, src dialogs.Folder, msgID int, dst dialogs.Folder, opts ...MoveOption) (newMsgID int, err error) {
	if api == nil {
		return 0, errors.New("fileops: api client is required")
	}
	if msgID <= 0 {
		return 0, fmt.Errorf("fileops: message id must be positive, got %d", msgID)
	}
	o := defaultMoveOptions()
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return move(ctx, api, src, msgID, dst, o)
}

// rename issues the messages.editMessage RPC. It is split from the exported
// Rename so tests can drive it with a fake messenger.
func rename(ctx context.Context, api messenger, folder dialogs.Folder, msgID int, newName string) error {
	req := &tg.MessagesEditMessageRequest{
		Peer: inputPeer(folder),
		ID:   msgID,
	}
	req.SetMessage(newName)
	if _, err := api.MessagesEditMessage(ctx, req); err != nil {
		return fmt.Errorf("fileops: renaming message %d: %w", msgID, err)
	}
	return nil
}

// deleteMsg issues the channels.deleteMessages RPC. It is split from the
// exported Delete so tests (and Move) can drive it with a fake messenger.
func deleteMsg(ctx context.Context, api messenger, folder dialogs.Folder, msgID int) error {
	if _, err := api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
		Channel: inputChannel(folder),
		ID:      []int{msgID},
	}); err != nil {
		return fmt.Errorf("fileops: deleting message %d: %w", msgID, err)
	}
	return nil
}

// move implements the forward-then-delete orchestration behind Move against the
// messenger abstraction.
func move(ctx context.Context, api messenger, src dialogs.Folder, msgID int, dst dialogs.Folder, o moveOptions) (int, error) {
	upd, err := api.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
		FromPeer: inputPeer(src),
		ToPeer:   inputPeer(dst),
		ID:       []int{msgID},
		RandomID: []int64{o.randInt64()},
	})
	if err != nil {
		return 0, fmt.Errorf("fileops: forwarding message %d: %w", msgID, err)
	}

	newID, err := newMessageID(upd)
	if err != nil {
		// The forward succeeded but we could not read the new id back. The file
		// is now in dst as well; surface that instead of pretending it failed.
		return 0, fmt.Errorf("fileops: message %d forwarded to destination but the new id could not be read; the file may now exist in BOTH folders: %w", msgID, err)
	}

	if err := deleteMsg(ctx, api, src, msgID); err != nil {
		// Non-atomic failure mode: the copy is already in dst but the original
		// could not be removed. Return the new id so the caller can reconcile,
		// and make the duplicated state explicit rather than silent.
		return newID, fmt.Errorf("fileops: move not atomic: message %d was forwarded to the destination as message %d but could not be deleted from the source; the file now exists in BOTH folders, reconcile by deleting source message %d: %w", msgID, newID, msgID, err)
	}
	return newID, nil
}

// newMessageID extracts the id of the newly created message from the Updates
// envelope returned by messages.forwardMessages. It is network-free: it only
// inspects the decoded reply, so it is unit-testable offline.
//
// It accepts any Updates variant exposing GetUpdates (updates and
// updatesCombined), scans for the first update carrying a concrete *tg.Message
// (updateNewMessage / updateNewChannelMessage) and returns its id. Anything else
// is an error, since a successful forward must echo the new message back.
func newMessageID(upd tg.UpdatesClass) (int, error) {
	if upd == nil {
		return 0, errors.New("fileops: nil updates from forward")
	}
	provider, ok := upd.(interface{ GetUpdates() []tg.UpdateClass })
	if !ok {
		return 0, fmt.Errorf("fileops: unexpected updates type %T without update list", upd)
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
		return msg.GetID(), nil
	}
	return 0, errors.New("fileops: forwarded message not found in updates")
}

// cryptoRandInt64 returns a non-zero random int64 suitable for a Telegram
// RandomID, drawn from crypto/rand. It retries the vanishingly unlikely zero
// draw so the value is always usable as a deduplication nonce.
func cryptoRandInt64() int64 {
	var b [8]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			// crypto/rand should never fail; if it does, there is no safe
			// non-random fallback for a dedup nonce, so surface a distinctive
			// non-zero value rather than panicking in a normal path.
			return -1
		}
		// Reinterpret the random bits as int64; every bit pattern is a valid
		// nonce, so the wrap-around gosec warns about is exactly what we want.
		v := int64(binary.LittleEndian.Uint64(b[:])) //nolint:gosec // G115: intentional bit reinterpretation for a random nonce
		if v != 0 {
			return v
		}
	}
}

// inputPeer builds the *tg.InputPeerChannel used to address a folder's channel
// in peer-based RPCs (editMessage, forwardMessages).
func inputPeer(f dialogs.Folder) *tg.InputPeerChannel {
	return &tg.InputPeerChannel{ChannelID: f.ChannelID, AccessHash: f.AccessHash}
}

// inputChannel builds the *tg.InputChannel used to address a folder's channel in
// channel management RPCs (deleteMessages).
func inputChannel(f dialogs.Folder) *tg.InputChannel {
	return &tg.InputChannel{ChannelID: f.ChannelID, AccessHash: f.AccessHash}
}
