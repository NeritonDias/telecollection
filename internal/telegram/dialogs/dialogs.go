// Package dialogs discovers the Telegram channels that act as TeleCollection
// drive folders.
//
// A folder is modelled as a Telegram broadcast channel whose title carries the
// TeleCollection marker "[TC]" (see docs/ARQUITETURA.md: folders = marked
// broadcast channels). IsFolderTitle encodes that marking rule as a pure,
// network-free predicate; List walks the account's dialogs and returns the
// channels that qualify.
//
// No secret material is logged by this package.
package dialogs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/tg"
)

// marker is the case-insensitive substring that tags a channel title as a
// TeleCollection folder.
const marker = "[TC]"

// Folder identifies a Telegram channel used as a TeleCollection drive folder.
// ChannelID and AccessHash together form the InputPeerChannel needed to address
// the channel in later RPCs; Title is the raw channel title (marker included).
type Folder struct {
	ChannelID  int64
	AccessHash int64
	Title      string
}

// IsFolderTitle reports whether a channel title marks it as a TeleCollection
// folder, i.e. it contains the "[TC]" marker (case-insensitive).
func IsFolderTitle(title string) bool {
	return strings.Contains(strings.ToLower(title), strings.ToLower(marker))
}

// DisplayName strips the "[TC]" marker from a folder title (case-insensitive,
// every occurrence) and trims surrounding whitespace, yielding the name to show
// to the user. It does not assume the title is actually a folder title.
func DisplayName(title string) string {
	// Remove the marker case-insensitively without allocating a regexp.
	lower := strings.ToLower(title)
	lowerMarker := strings.ToLower(marker)
	var b strings.Builder
	for {
		i := strings.Index(lower, lowerMarker)
		if i < 0 {
			b.WriteString(title)
			break
		}
		b.WriteString(title[:i])
		title = title[i+len(marker):]
		lower = lower[i+len(lowerMarker):]
	}
	return strings.TrimSpace(b.String())
}

// List fetches the account's dialogs and returns the channels marked as
// TeleCollection folders. It iterates every dialog via gotd's dialog query
// helper and keeps the broadcast channels whose title satisfies IsFolderTitle.
func List(ctx context.Context, api *tg.Client) ([]Folder, error) {
	if api == nil {
		return nil, errors.New("dialogs: api client is nil")
	}

	elems, err := query.GetDialogs(api).Collect(ctx)
	if err != nil {
		return nil, fmt.Errorf("dialogs: collecting dialogs: %w", err)
	}

	folders := make([]Folder, 0, len(elems))
	for _, e := range elems {
		if f, ok := folderFrom(e); ok {
			folders = append(folders, f)
		}
	}
	return folders, nil
}

// folderFrom decides whether a single dialog element is a folder and, if so,
// extracts its Folder. It is pure with respect to the network: it only reads
// the element's peer and resolved entities, which makes it unit-testable
// without a live Telegram connection.
//
// A dialog qualifies when its peer is a channel, the channel is a broadcast
// channel (not a megagroup), and its title satisfies IsFolderTitle.
func folderFrom(e dialogs.Elem) (Folder, bool) {
	// Only ordinary dialogs carry a peer we care about; DialogFolder is
	// Telegram's archive container, not a channel.
	d, ok := e.Dialog.(*tg.Dialog)
	if !ok {
		return Folder{}, false
	}
	pc, ok := d.GetPeer().(*tg.PeerChannel)
	if !ok {
		return Folder{}, false
	}
	ch, ok := e.Entities.Channel(pc.GetChannelID())
	if !ok {
		return Folder{}, false
	}
	if !ch.GetBroadcast() {
		return Folder{}, false
	}
	title := ch.GetTitle()
	if !IsFolderTitle(title) {
		return Folder{}, false
	}
	accessHash, _ := ch.GetAccessHash()
	return Folder{
		ChannelID:  ch.GetID(),
		AccessHash: accessHash,
		Title:      title,
	}, true
}
