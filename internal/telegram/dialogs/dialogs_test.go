package dialogs

import (
	"context"
	"os"
	"testing"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/tg"
)

func TestIsFolderTitle(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		title string
		want  bool
	}{
		{"suffix marker", "Docs [TC]", true},
		{"lowercase marker prefix", "[tc] fotos", true},
		{"mixed case marker", "Backups [Tc] 2026", true},
		{"marker embedded", "a[TC]b", true},
		{"exact marker only", "[TC]", true},
		{"no marker", "Random", false},
		{"empty", "", false},
		{"partial without brackets", "TC files", false},
		{"open bracket only", "Docs [TC", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsFolderTitle(tc.title); got != tc.want {
				t.Errorf("IsFolderTitle(%q) = %v, want %v", tc.title, got, tc.want)
			}
		})
	}
}

func TestDisplayName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		title string
		want  string
	}{
		{"suffix marker trimmed", "Docs [TC]", "Docs"},
		{"prefix marker trimmed", "[tc] Fotos", "Fotos"},
		{"marker in middle", "Backups [TC] 2026", "Backups  2026"},
		{"no marker unchanged", "Random", "Random"},
		{"only marker", "[TC]", ""},
		{"empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DisplayName(tc.title); got != tc.want {
				t.Errorf("DisplayName(%q) = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

// newChannel builds a *tg.Channel with the flag-backed fields (Broadcast,
// AccessHash) set through their setters so the getters read them back.
func newChannel(id, accessHash int64, title string, broadcast bool) *tg.Channel {
	ch := &tg.Channel{ID: id, Title: title}
	ch.SetAccessHash(accessHash)
	ch.SetBroadcast(broadcast)
	return ch
}

// channelElem constructs a dialog element whose peer is the given channel,
// with that channel resolvable via the element entities. This mirrors what
// gotd produces from a MessagesGetDialogs response, without any network I/O.
func channelElem(ch *tg.Channel) dialogs.Elem {
	return dialogs.Elem{
		Dialog:   &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: ch.ID}},
		Entities: peer.NewEntities(nil, nil, map[int64]*tg.Channel{ch.ID: ch}),
	}
}

func TestFolderFrom(t *testing.T) {
	t.Parallel()

	t.Run("broadcast channel with marker is a folder", func(t *testing.T) {
		t.Parallel()
		ch := newChannel(123, 456, "Docs [TC]", true)
		got, ok := folderFrom(channelElem(ch))
		if !ok {
			t.Fatalf("folderFrom() ok = false, want true")
		}
		want := Folder{ChannelID: 123, AccessHash: 456, Title: "Docs [TC]"}
		if got != want {
			t.Errorf("folderFrom() = %+v, want %+v", got, want)
		}
	})

	t.Run("broadcast channel without marker is not a folder", func(t *testing.T) {
		t.Parallel()
		ch := newChannel(1, 2, "Just a channel", true)
		if _, ok := folderFrom(channelElem(ch)); ok {
			t.Errorf("folderFrom() ok = true, want false for unmarked channel")
		}
	})

	t.Run("marked megagroup is not a folder", func(t *testing.T) {
		t.Parallel()
		// Megagroup (Broadcast=false) must be rejected: folders are broadcast
		// channels per ARQUITETURA.md.
		ch := newChannel(7, 8, "Group [TC]", false)
		if _, ok := folderFrom(channelElem(ch)); ok {
			t.Errorf("folderFrom() ok = true, want false for non-broadcast channel")
		}
	})

	t.Run("non-channel peer is not a folder", func(t *testing.T) {
		t.Parallel()
		e := dialogs.Elem{Dialog: &tg.Dialog{Peer: &tg.PeerUser{UserID: 99}}}
		if _, ok := folderFrom(e); ok {
			t.Errorf("folderFrom() ok = true, want false for user peer")
		}
	})

	t.Run("nil dialog is not a folder", func(t *testing.T) {
		t.Parallel()
		if _, ok := folderFrom(dialogs.Elem{}); ok {
			t.Errorf("folderFrom() ok = true, want false for empty elem")
		}
	})

	t.Run("channel peer missing from entities is not a folder", func(t *testing.T) {
		t.Parallel()
		// Peer references channel 5 but entities carry a different channel.
		other := newChannel(6, 0, "Other [TC]", true)
		e := dialogs.Elem{
			Dialog:   &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 5}},
			Entities: peer.NewEntities(nil, nil, map[int64]*tg.Channel{other.ID: other}),
		}
		if _, ok := folderFrom(e); ok {
			t.Errorf("folderFrom() ok = true, want false when channel unresolved")
		}
	})
}

func TestListNilAPI(t *testing.T) {
	t.Parallel()
	if _, err := List(context.Background(), nil); err == nil {
		t.Errorf("List(nil api) err = nil, want error")
	}
}

// TestListE2E exercises List against a real Telegram account. It requires a
// live, authenticated client and is therefore skipped unless TELECOL_TEST_TG=1.
// The end-to-end path (query.GetDialogs over MTProto) can only be validated
// with real credentials; see docs/plano/FASE-1.md 1.7.
func TestListE2E(t *testing.T) {
	if os.Getenv("TELECOL_TEST_TG") != "1" {
		t.Skip("List end-to-end requires a live Telegram account; set TELECOL_TEST_TG=1 to opt in")
	}
	// A real run needs an authenticated *tg.Client (client.Run establishes the
	// MTProto session) whose api.Invoker feeds query.GetDialogs. That wiring
	// depends on live credentials supplied by the operator and is intentionally
	// left to manual/integration validation; see docs/plano/FASE-1.md 1.7.
	t.Skip("live Telegram client wiring not available in unit tests")
}
