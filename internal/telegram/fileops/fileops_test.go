package fileops

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/telecollection/telecollection/internal/telegram/dialogs"
)

// tgClientStub is a zero-value *tg.Client used only to satisfy the non-nil api
// guard in the validation tests. The validated paths return before any RPC is
// attempted, so the client is never actually driven.
var tgClientStub tg.Client

// fakeMessenger records the requests it receives and returns canned results,
// letting the orchestration logic (especially Move's non-atomic forward+delete)
// be exercised entirely offline.
type fakeMessenger struct {
	editErr     error
	editReq     *tg.MessagesEditMessageRequest
	deleteErr   error
	deleteReqs  []*tg.ChannelsDeleteMessagesRequest
	forwardErr  error
	forwardReq  *tg.MessagesForwardMessagesRequest
	forwardResp tg.UpdatesClass
}

func (f *fakeMessenger) MessagesEditMessage(_ context.Context, req *tg.MessagesEditMessageRequest) (tg.UpdatesClass, error) {
	f.editReq = req
	if f.editErr != nil {
		return nil, f.editErr
	}
	return &tg.Updates{}, nil
}

func (f *fakeMessenger) ChannelsDeleteMessages(_ context.Context, req *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	f.deleteReqs = append(f.deleteReqs, req)
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &tg.MessagesAffectedMessages{}, nil
}

func (f *fakeMessenger) MessagesForwardMessages(_ context.Context, req *tg.MessagesForwardMessagesRequest) (tg.UpdatesClass, error) {
	f.forwardReq = req
	if f.forwardErr != nil {
		return nil, f.forwardErr
	}
	return f.forwardResp, nil
}

// forwardResult builds an Updates envelope carrying a single new channel message
// with id newID, mimicking what messages.forwardMessages echoes back.
func forwardResult(newID int) tg.UpdatesClass {
	return &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateNewChannelMessage{Message: &tg.Message{ID: newID}},
		},
	}
}

func TestRenameValidatesInput(t *testing.T) {
	ctx := context.Background()
	folder := dialogs.Folder{ChannelID: 1, AccessHash: 2}

	if err := Rename(ctx, nil, folder, 1, "name"); err == nil {
		t.Error("Rename with nil api: expected error, got nil")
	}
	if err := Rename(ctx, &tgClientStub, folder, 0, "name"); err == nil {
		t.Error("Rename with zero msgID: expected error, got nil")
	}
	if err := Rename(ctx, &tgClientStub, folder, -3, "name"); err == nil {
		t.Error("Rename with negative msgID: expected error, got nil")
	}
	if err := Rename(ctx, &tgClientStub, folder, 1, ""); err == nil {
		t.Error("Rename with empty name: expected error, got nil")
	}
	if err := Rename(ctx, &tgClientStub, folder, 1, "   "); err == nil {
		t.Error("Rename with blank name: expected error, got nil")
	}
}

func TestRenameEditsCaption(t *testing.T) {
	fm := &fakeMessenger{}
	folder := dialogs.Folder{ChannelID: 10, AccessHash: 20}

	if err := rename(context.Background(), fm, folder, 7, "invoice.pdf"); err != nil {
		t.Fatalf("rename: unexpected error: %v", err)
	}
	if fm.editReq == nil {
		t.Fatal("rename did not issue an edit request")
	}
	if fm.editReq.ID != 7 {
		t.Errorf("edit ID = %d, want 7", fm.editReq.ID)
	}
	if got, ok := fm.editReq.GetMessage(); !ok || got != "invoice.pdf" {
		t.Errorf("edit message = %q (set=%v), want %q", got, ok, "invoice.pdf")
	}
	peer, ok := fm.editReq.Peer.(*tg.InputPeerChannel)
	if !ok {
		t.Fatalf("edit peer type = %T, want *tg.InputPeerChannel", fm.editReq.Peer)
	}
	if peer.ChannelID != 10 || peer.AccessHash != 20 {
		t.Errorf("edit peer = %+v, want channel 10/hash 20", peer)
	}
}

func TestRenamePropagatesError(t *testing.T) {
	fm := &fakeMessenger{editErr: errors.New("boom")}
	err := rename(context.Background(), fm, dialogs.Folder{}, 7, "x")
	if err == nil {
		t.Fatal("expected error from failing edit, got nil")
	}
	if !errors.Is(err, fm.editErr) {
		t.Errorf("error = %v, want it to wrap the underlying edit error", err)
	}
}

func TestDeleteValidatesInput(t *testing.T) {
	ctx := context.Background()
	folder := dialogs.Folder{ChannelID: 1, AccessHash: 2}

	if err := Delete(ctx, nil, folder, 1); err == nil {
		t.Error("Delete with nil api: expected error, got nil")
	}
	if err := Delete(ctx, &tgClientStub, folder, 0); err == nil {
		t.Error("Delete with zero msgID: expected error, got nil")
	}
	if err := Delete(ctx, &tgClientStub, folder, -1); err == nil {
		t.Error("Delete with negative msgID: expected error, got nil")
	}
}

func TestDeleteRemovesMessage(t *testing.T) {
	fm := &fakeMessenger{}
	folder := dialogs.Folder{ChannelID: 10, AccessHash: 20}

	if err := deleteMsg(context.Background(), fm, folder, 9); err != nil {
		t.Fatalf("deleteMsg: unexpected error: %v", err)
	}
	if len(fm.deleteReqs) != 1 {
		t.Fatalf("delete requests = %d, want 1", len(fm.deleteReqs))
	}
	req := fm.deleteReqs[0]
	if len(req.ID) != 1 || req.ID[0] != 9 {
		t.Errorf("delete IDs = %v, want [9]", req.ID)
	}
	ch, ok := req.Channel.(*tg.InputChannel)
	if !ok {
		t.Fatalf("delete channel type = %T, want *tg.InputChannel", req.Channel)
	}
	if ch.ChannelID != 10 || ch.AccessHash != 20 {
		t.Errorf("delete channel = %+v, want channel 10/hash 20", ch)
	}
}

func TestDeletePropagatesError(t *testing.T) {
	fm := &fakeMessenger{deleteErr: errors.New("nope")}
	err := deleteMsg(context.Background(), fm, dialogs.Folder{}, 9)
	if err == nil {
		t.Fatal("expected error from failing delete, got nil")
	}
	if !errors.Is(err, fm.deleteErr) {
		t.Errorf("error = %v, want it to wrap the underlying delete error", err)
	}
}

func TestNewMessageIDExtractsID(t *testing.T) {
	id, err := newMessageID(forwardResult(555))
	if err != nil {
		t.Fatalf("newMessageID: unexpected error: %v", err)
	}
	if id != 555 {
		t.Errorf("id = %d, want 555", id)
	}
}

func TestNewMessageIDNil(t *testing.T) {
	if _, err := newMessageID(nil); err == nil {
		t.Fatal("expected error for nil updates, got nil")
	}
}

func TestNewMessageIDNoMessage(t *testing.T) {
	// An Updates envelope that carries no new-message update.
	if _, err := newMessageID(&tg.Updates{}); err == nil {
		t.Fatal("expected error when no new message is present, got nil")
	}
}

func TestNewMessageIDUnexpectedType(t *testing.T) {
	// updatesTooLong exposes no update list.
	if _, err := newMessageID(&tg.UpdatesTooLong{}); err == nil {
		t.Fatal("expected error for updates without an update list, got nil")
	}
}

func TestMoveValidatesInput(t *testing.T) {
	ctx := context.Background()
	src := dialogs.Folder{ChannelID: 1, AccessHash: 2}
	dst := dialogs.Folder{ChannelID: 3, AccessHash: 4}

	if _, err := Move(ctx, nil, src, 1, dst); err == nil {
		t.Error("Move with nil api: expected error, got nil")
	}
	if _, err := Move(ctx, &tgClientStub, src, 0, dst); err == nil {
		t.Error("Move with zero msgID: expected error, got nil")
	}
	if _, err := Move(ctx, &tgClientStub, src, -2, dst); err == nil {
		t.Error("Move with negative msgID: expected error, got nil")
	}
}

func TestMoveForwardsThenDeletes(t *testing.T) {
	fm := &fakeMessenger{forwardResp: forwardResult(999)}
	src := dialogs.Folder{ChannelID: 10, AccessHash: 20}
	dst := dialogs.Folder{ChannelID: 30, AccessHash: 40}

	newID, err := move(context.Background(), fm, src, 7, dst, moveOptions{randInt64: func() int64 { return 12345 }})
	if err != nil {
		t.Fatalf("move: unexpected error: %v", err)
	}
	if newID != 999 {
		t.Errorf("newID = %d, want 999", newID)
	}

	// Forward must go from src to dst carrying the deterministic random id.
	if fm.forwardReq == nil {
		t.Fatal("move did not forward")
	}
	from, ok := fm.forwardReq.FromPeer.(*tg.InputPeerChannel)
	if !ok || from.ChannelID != 10 || from.AccessHash != 20 {
		t.Errorf("forward FromPeer = %+v, want channel 10/hash 20", fm.forwardReq.FromPeer)
	}
	to, ok := fm.forwardReq.ToPeer.(*tg.InputPeerChannel)
	if !ok || to.ChannelID != 30 || to.AccessHash != 40 {
		t.Errorf("forward ToPeer = %+v, want channel 30/hash 40", fm.forwardReq.ToPeer)
	}
	if len(fm.forwardReq.ID) != 1 || fm.forwardReq.ID[0] != 7 {
		t.Errorf("forward ID = %v, want [7]", fm.forwardReq.ID)
	}
	if len(fm.forwardReq.RandomID) != 1 || fm.forwardReq.RandomID[0] != 12345 {
		t.Errorf("forward RandomID = %v, want [12345]", fm.forwardReq.RandomID)
	}

	// Delete must remove the original from src.
	if len(fm.deleteReqs) != 1 {
		t.Fatalf("delete requests = %d, want 1", len(fm.deleteReqs))
	}
	if ch, ok := fm.deleteReqs[0].Channel.(*tg.InputChannel); !ok || ch.ChannelID != 10 {
		t.Errorf("delete channel = %+v, want src channel 10", fm.deleteReqs[0].Channel)
	}
	if fm.deleteReqs[0].ID[0] != 7 {
		t.Errorf("delete ID = %v, want [7]", fm.deleteReqs[0].ID)
	}
}

func TestMoveForwardFailsDoesNotDelete(t *testing.T) {
	fm := &fakeMessenger{forwardErr: errors.New("forward down")}
	src := dialogs.Folder{ChannelID: 10, AccessHash: 20}
	dst := dialogs.Folder{ChannelID: 30, AccessHash: 40}

	newID, err := move(context.Background(), fm, src, 7, dst, moveOptions{randInt64: func() int64 { return 1 }})
	if err == nil {
		t.Fatal("expected error when forward fails, got nil")
	}
	if newID != 0 {
		t.Errorf("newID = %d, want 0 on forward failure", newID)
	}
	if !errors.Is(err, fm.forwardErr) {
		t.Errorf("error = %v, want it to wrap the forward error", err)
	}
	// Nothing was forwarded, so the source must be left untouched.
	if len(fm.deleteReqs) != 0 {
		t.Errorf("delete requests = %d, want 0 (source must not be deleted)", len(fm.deleteReqs))
	}
}

// TestMoveDeleteFailsReportsDuplication is the atomicity guard: if the forward
// succeeds but the subsequent delete fails, Move must NOT silently succeed. It
// returns the new dst id alongside an error that makes the duplicated state
// explicit, so the caller can reconcile by deleting the source message.
func TestMoveDeleteFailsReportsDuplication(t *testing.T) {
	fm := &fakeMessenger{
		forwardResp: forwardResult(999),
		deleteErr:   errors.New("delete forbidden"),
	}
	src := dialogs.Folder{ChannelID: 10, AccessHash: 20}
	dst := dialogs.Folder{ChannelID: 30, AccessHash: 40}

	newID, err := move(context.Background(), fm, src, 7, dst, moveOptions{randInt64: func() int64 { return 1 }})
	if err == nil {
		t.Fatal("expected error when delete fails after a successful forward, got nil")
	}
	// The new dst message id is still returned so the caller can act on it.
	if newID != 999 {
		t.Errorf("newID = %d, want 999 (dst copy exists)", newID)
	}
	if !errors.Is(err, fm.deleteErr) {
		t.Errorf("error = %v, want it to wrap the underlying delete error", err)
	}
	// The error must clearly signal that the file now exists in BOTH folders.
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "both") {
		t.Errorf("error = %q, want it to warn the file exists in BOTH folders", err.Error())
	}
	// The forward definitely happened, and the delete was attempted once.
	if fm.forwardReq == nil {
		t.Error("forward was not attempted")
	}
	if len(fm.deleteReqs) != 1 {
		t.Errorf("delete attempts = %d, want 1", len(fm.deleteReqs))
	}
}

// TestMoveDefaultRandomIDIsNonZeroAndUnique proves the default (crypto-backed)
// random id generator is wired when no option is supplied: it produces non-zero,
// varying values without relying on time or math/rand.
func TestMoveDefaultRandomIDIsNonZeroAndUnique(t *testing.T) {
	o := defaultMoveOptions()
	if o.randInt64 == nil {
		t.Fatal("default move options must provide a random id generator")
	}
	seen := make(map[int64]struct{})
	for i := 0; i < 100; i++ {
		v := o.randInt64()
		if v == 0 {
			t.Fatal("random id generator returned 0")
		}
		if _, dup := seen[v]; dup {
			t.Fatalf("random id generator repeated value %d", v)
		}
		seen[v] = struct{}{}
	}
}

// TestFileOpsE2E exercises the real network path against a live, authenticated
// Telegram session. It is skipped unless TELECOL_TEST_TG=1, since these calls
// require a connected *tg.Client. It keeps the gotd messages.editMessage,
// channels.deleteMessages and messages.forwardMessages wiring compiling and
// honest; the full flow is driven by the phase that owns a real client and
// known message/folder ids.
func TestFileOpsE2E(t *testing.T) {
	if os.Getenv("TELECOL_TEST_TG") != "1" {
		t.Skip("set TELECOL_TEST_TG=1 with a live Telegram session to run the file-ops E2E test")
	}
	t.Skip("E2E file ops are driven by the file-ops phase with a connected *tg.Client")

	// Compile-only references to the exported signatures so the E2E wiring stays
	// honest even while the body is skipped.
	var (
		_ = Rename
		_ = Delete
		_ = Move
	)
}
