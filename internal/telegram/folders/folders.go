// Package folders manages the Telegram broadcast channels that act as
// TeleCollection drive folders.
//
// A folder is a broadcast channel whose title carries the "[TC]" marker (see
// docs/ARQUITETURA.md and the dialogs package, which discovers them). This
// package owns the write side of that model: creating, renaming and deleting
// those channels via gotd's raw MTProto client. Listing lives in the dialogs
// package and is reused rather than duplicated.
//
// No secret material is logged by this package.
package folders

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"

	"github.com/telecollection/telecollection/internal/telegram/dialogs"
)

// marker is the canonical TeleCollection tag appended to folder channel titles.
// It matches (case-insensitively) the marker dialogs.IsFolderTitle looks for,
// so every title produced here is discovered back as a folder.
const marker = "[TC]"

// FolderName maps a user-facing display name to the canonical channel title,
// appending the "[TC]" marker. It is idempotent: a name that already carries a
// marker (in any case) is not double-marked. The result always satisfies
// dialogs.IsFolderTitle.
func FolderName(displayName string) string {
	clean := dialogs.DisplayName(displayName)
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return marker
	}
	return clean + " " + marker
}

// Create creates a broadcast channel titled "{displayName} [TC]" and disables
// its history auto-delete (TTL), so files placed in the folder are never
// silently expired. It returns the resulting Folder addressed by its channel
// ID and access hash.
//
// It uses channels.createChannel (Broadcast=true) to create the channel and
// messages.setHistoryTTL (Period=0) to turn off auto-delete. On a TTL failure
// the created folder is still returned alongside the error so the caller can
// address or delete the orphaned channel.
func Create(ctx context.Context, api *tg.Client, displayName string) (dialogs.Folder, error) {
	if api == nil {
		return dialogs.Folder{}, errors.New("folders: api client is nil")
	}

	title := FolderName(displayName)
	upd, err := api.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{
		Broadcast: true,
		Title:     title,
	})
	if err != nil {
		return dialogs.Folder{}, fmt.Errorf("folders: creating channel: %w", err)
	}

	ch, err := channelFromUpdates(upd)
	if err != nil {
		return dialogs.Folder{}, err
	}

	accessHash, _ := ch.GetAccessHash()
	f := dialogs.Folder{
		ChannelID:  ch.GetID(),
		AccessHash: accessHash,
		Title:      ch.GetTitle(),
	}

	if _, err := api.MessagesSetHistoryTTL(ctx, &tg.MessagesSetHistoryTTLRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: f.ChannelID, AccessHash: f.AccessHash},
		Period: 0,
	}); err != nil {
		return f, fmt.Errorf("folders: disabling history auto-delete: %w", err)
	}

	return f, nil
}

// Rename edits the folder channel's title to "{newDisplayName} [TC]" via
// channels.editTitle. The Folder's own Title is not mutated; callers that need
// the updated value should re-read it (e.g. via dialogs.List).
func Rename(ctx context.Context, api *tg.Client, f dialogs.Folder, newDisplayName string) error {
	if api == nil {
		return errors.New("folders: api client is nil")
	}
	if _, err := api.ChannelsEditTitle(ctx, &tg.ChannelsEditTitleRequest{
		Channel: inputChannel(f),
		Title:   FolderName(newDisplayName),
	}); err != nil {
		return fmt.Errorf("folders: editing channel title: %w", err)
	}
	return nil
}

// Delete removes the folder channel entirely via channels.deleteChannel.
func Delete(ctx context.Context, api *tg.Client, f dialogs.Folder) error {
	if api == nil {
		return errors.New("folders: api client is nil")
	}
	if _, err := api.ChannelsDeleteChannel(ctx, inputChannel(f)); err != nil {
		return fmt.Errorf("folders: deleting channel: %w", err)
	}
	return nil
}

// inputChannel builds the *tg.InputChannel gotd needs to address a folder's
// channel in channel management RPCs.
func inputChannel(f dialogs.Folder) *tg.InputChannel {
	return &tg.InputChannel{ChannelID: f.ChannelID, AccessHash: f.AccessHash}
}

// channelFromUpdates extracts the freshly created broadcast channel from the
// Updates returned by channels.createChannel. The channel (with its ID and
// access hash) is carried in the update's Chats collection.
func channelFromUpdates(upd tg.UpdatesClass) (*tg.Channel, error) {
	provider, ok := upd.(interface{ GetChats() []tg.ChatClass })
	if !ok {
		return nil, fmt.Errorf("folders: unexpected updates type %T without chats", upd)
	}
	for _, ch := range tg.ChatClassArray(provider.GetChats()).AsChannel() {
		created := ch
		return &created, nil
	}
	return nil, errors.New("folders: created channel not found in updates")
}
