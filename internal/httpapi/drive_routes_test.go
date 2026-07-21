package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/telecollection/telecollection/internal/store"
	"github.com/telecollection/telecollection/internal/telegram/dialogs"
	"github.com/telecollection/telecollection/internal/transfer"
)

// fakeDrive is a configurable drive.Service double for testing the HTTP layer.
// It records the arguments it is called with and returns pre-seeded values.
type fakeDrive struct {
	// configured returns
	folders  []dialogs.Folder
	folder   dialogs.Folder
	files    []store.File
	file     store.File
	newMsgID int
	dlBytes  []byte // bytes DownloadFile streams into w
	err      error  // when set, every method returns it

	// captured arguments
	createdName string

	renamedFolder  dialogs.Folder
	renamedNewName string

	deletedFolder dialogs.Folder

	listedFolder dialogs.Folder

	uploadFolder dialogs.Folder
	uploadName   string
	uploadMIME   string
	uploadSize   int64
	uploadBytes  []byte

	downloadFolder dialogs.Folder
	downloadMsgID  int

	fileRenameFolder  dialogs.Folder
	fileRenameMsgID   int
	fileRenameNewName string

	moveSrc   dialogs.Folder
	moveDst   dialogs.Folder
	moveMsgID int

	deleteFileFolder dialogs.Folder
	deleteFileMsgID  int
}

func (f *fakeDrive) ListFolders(_ context.Context) ([]dialogs.Folder, error) {
	return f.folders, f.err
}

func (f *fakeDrive) CreateFolder(_ context.Context, name string) (dialogs.Folder, error) {
	f.createdName = name
	if f.err != nil {
		return dialogs.Folder{}, f.err
	}
	return f.folder, nil
}

func (f *fakeDrive) RenameFolder(_ context.Context, folder dialogs.Folder, newName string) error {
	f.renamedFolder = folder
	f.renamedNewName = newName
	return f.err
}

func (f *fakeDrive) DeleteFolder(_ context.Context, folder dialogs.Folder) error {
	f.deletedFolder = folder
	return f.err
}

func (f *fakeDrive) ListFiles(_ context.Context, folder dialogs.Folder) ([]store.File, error) {
	f.listedFolder = folder
	return f.files, f.err
}

func (f *fakeDrive) UploadFile(_ context.Context, folder dialogs.Folder, name string, r io.Reader, size int64, mime string, _ func(transfer.Progress)) (store.File, error) {
	f.uploadFolder = folder
	f.uploadName = name
	f.uploadSize = size
	f.uploadMIME = mime
	b, _ := io.ReadAll(r)
	f.uploadBytes = b
	if f.err != nil {
		return store.File{}, f.err
	}
	return f.file, nil
}

func (f *fakeDrive) DownloadFile(_ context.Context, folder dialogs.Folder, msgID int, w io.Writer, _ func(transfer.Progress)) error {
	f.downloadFolder = folder
	f.downloadMsgID = msgID
	if f.err != nil {
		return f.err
	}
	_, err := w.Write(f.dlBytes)
	return err
}

func (f *fakeDrive) RenameFile(_ context.Context, folder dialogs.Folder, msgID int, newName string) error {
	f.fileRenameFolder = folder
	f.fileRenameMsgID = msgID
	f.fileRenameNewName = newName
	return f.err
}

func (f *fakeDrive) DeleteFile(_ context.Context, folder dialogs.Folder, msgID int) error {
	f.deleteFileFolder = folder
	f.deleteFileMsgID = msgID
	return f.err
}

func (f *fakeDrive) MoveFile(_ context.Context, src dialogs.Folder, msgID int, dst dialogs.Folder) (int, error) {
	f.moveSrc = src
	f.moveMsgID = msgID
	f.moveDst = dst
	if f.err != nil {
		return 0, f.err
	}
	return f.newMsgID, nil
}

// mountDrive builds a bare chi router with only the drive routes registered.
func mountDrive(svc *fakeDrive) http.Handler {
	r := chi.NewRouter()
	RegisterDriveRoutes(r, svc)
	return r
}

func TestListFolders_ReturnsSnakeCasePayload(t *testing.T) {
	f := &fakeDrive{folders: []dialogs.Folder{{ChannelID: 11, AccessHash: 22, Title: "[TC] docs"}}}
	rec := doJSON(t, mountDrive(f), http.MethodGet, "/folders", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var out []struct {
		ChannelID  int64  `json:"channel_id"`
		AccessHash int64  `json:"access_hash"`
		Title      string `json:"title"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(out) != 1 || out[0].ChannelID != 11 || out[0].AccessHash != 22 || out[0].Title != "[TC] docs" {
		t.Fatalf("unexpected payload %+v", out)
	}
}

func TestCreateFolder_CallsCreateAndReturnsFolder(t *testing.T) {
	f := &fakeDrive{folder: dialogs.Folder{ChannelID: 7, AccessHash: 8, Title: "[TC] photos"}}
	rec := doJSON(t, mountDrive(f), http.MethodPost, "/folders", `{"name":"photos"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if f.createdName != "photos" {
		t.Fatalf("CreateFolder got name %q, want photos", f.createdName)
	}
	var out struct {
		ChannelID int64 `json:"channel_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ChannelID != 7 {
		t.Fatalf("channel_id = %d, want 7", out.ChannelID)
	}
}

func TestRenameFolder_Returns204WithArgs(t *testing.T) {
	f := &fakeDrive{}
	rec := doJSON(t, mountDrive(f), http.MethodPatch, "/folders",
		`{"channel_id":3,"access_hash":4,"title":"[TC] old","new_name":"new"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204 (body %q)", rec.Code, rec.Body.String())
	}
	if f.renamedFolder.ChannelID != 3 || f.renamedFolder.AccessHash != 4 {
		t.Fatalf("renamed folder = %+v", f.renamedFolder)
	}
	if f.renamedNewName != "new" {
		t.Fatalf("new name = %q, want new", f.renamedNewName)
	}
}

func TestDeleteFolder_Returns204WithArgs(t *testing.T) {
	f := &fakeDrive{}
	rec := doJSON(t, mountDrive(f), http.MethodDelete, "/folders",
		`{"channel_id":5,"access_hash":6,"title":"[TC] x"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204 (body %q)", rec.Code, rec.Body.String())
	}
	if f.deletedFolder.ChannelID != 5 || f.deletedFolder.AccessHash != 6 {
		t.Fatalf("deleted folder = %+v", f.deletedFolder)
	}
}

func TestListFiles_ViaQuery(t *testing.T) {
	f := &fakeDrive{files: []store.File{{MessageID: 99, Name: "a.txt", Size: 10, MIME: "text/plain"}}}
	rec := doJSON(t, mountDrive(f), http.MethodGet, "/files?channel_id=1&access_hash=2", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if f.listedFolder.ChannelID != 1 || f.listedFolder.AccessHash != 2 {
		t.Fatalf("listed folder = %+v", f.listedFolder)
	}
	var out []store.File
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(out) != 1 || out[0].MessageID != 99 || out[0].Name != "a.txt" {
		t.Fatalf("unexpected files %+v", out)
	}
}

func TestListFiles_ViaBody(t *testing.T) {
	f := &fakeDrive{}
	rec := doJSON(t, mountDrive(f), http.MethodGet, "/files", `{"channel_id":42,"access_hash":43}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if f.listedFolder.ChannelID != 42 || f.listedFolder.AccessHash != 43 {
		t.Fatalf("listed folder = %+v", f.listedFolder)
	}
}

func newUploadRequest(t *testing.T, folder dialogs.Folder, name, mime string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("channel_id", strconv.FormatInt(folder.ChannelID, 10))
	_ = mw.WriteField("access_hash", strconv.FormatInt(folder.AccessHash, 10))
	_ = mw.WriteField("title", folder.Title)
	_ = mw.WriteField("name", name)
	_ = mw.WriteField("mime", mime)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestUploadFile_Multipart(t *testing.T) {
	content := []byte("hello telegram drive")
	f := &fakeDrive{file: store.File{MessageID: 500, Name: "note.txt"}}
	req := newUploadRequest(t, dialogs.Folder{ChannelID: 1, AccessHash: 2, Title: "[TC] x"}, "note.txt", "text/plain", content)
	rec := httptest.NewRecorder()
	mountDrive(f).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if f.uploadName != "note.txt" {
		t.Fatalf("upload name = %q, want note.txt", f.uploadName)
	}
	if f.uploadMIME != "text/plain" {
		t.Fatalf("upload mime = %q, want text/plain", f.uploadMIME)
	}
	if string(f.uploadBytes) != string(content) {
		t.Fatalf("upload bytes = %q, want %q", f.uploadBytes, content)
	}
	if f.uploadSize != int64(len(content)) {
		t.Fatalf("upload size = %d, want %d", f.uploadSize, len(content))
	}
	if f.uploadFolder.ChannelID != 1 || f.uploadFolder.AccessHash != 2 {
		t.Fatalf("upload folder = %+v", f.uploadFolder)
	}
	var out store.File
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.MessageID != 500 {
		t.Fatalf("message id = %d, want 500", out.MessageID)
	}
}

func TestUploadFile_MissingFilePartReturns400(t *testing.T) {
	f := &fakeDrive{}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("channel_id", "1")
	_ = mw.WriteField("access_hash", "2")
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	mountDrive(f).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 (body %q)", rec.Code, rec.Body.String())
	}
	if f.uploadName != "" {
		t.Fatalf("UploadFile should not be called, got name %q", f.uploadName)
	}
}

func TestDownloadFile_StreamsWithContentDisposition(t *testing.T) {
	f := &fakeDrive{dlBytes: []byte("file-contents")}
	rec := doJSON(t, mountDrive(f), http.MethodGet, "/files/download?channel_id=1&access_hash=2&msg_id=77&name=report.pdf", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if f.downloadMsgID != 77 {
		t.Fatalf("download msg id = %d, want 77", f.downloadMsgID)
	}
	if f.downloadFolder.ChannelID != 1 || f.downloadFolder.AccessHash != 2 {
		t.Fatalf("download folder = %+v", f.downloadFolder)
	}
	if rec.Body.String() != "file-contents" {
		t.Fatalf("body = %q, want file-contents", rec.Body.String())
	}
	cd := rec.Header().Get("Content-Disposition")
	if cd != `attachment; filename="report.pdf"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}
}

func TestDownloadFile_SanitizesFilename(t *testing.T) {
	f := &fakeDrive{dlBytes: []byte("x")}
	// name contains a quote, a CRLF injection attempt and a backslash.
	rec := doJSON(t, mountDrive(f), http.MethodGet,
		`/files/download?channel_id=1&access_hash=2&msg_id=1&name=`+`a%22b%0d%0aX%3A%20y%5Cc.txt`, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	cd := rec.Header().Get("Content-Disposition")
	if strings.ContainsAny(cd, "\r\n") {
		t.Fatalf("Content-Disposition contains CR/LF: %q", cd)
	}
	if cd != `attachment; filename="a\"bX: y\\c.txt"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}
}

func TestDownloadFile_BusinessErrorReturns500(t *testing.T) {
	f := &fakeDrive{err: context.DeadlineExceeded}
	rec := doJSON(t, mountDrive(f), http.MethodGet, "/files/download?channel_id=1&access_hash=2&msg_id=1", "")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500 (body %q)", rec.Code, rec.Body.String())
	}
}

func TestRenameFile_Returns204WithArgs(t *testing.T) {
	f := &fakeDrive{}
	rec := doJSON(t, mountDrive(f), http.MethodPatch, "/files",
		`{"channel_id":1,"access_hash":2,"msg_id":9,"new_name":"renamed.txt"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204 (body %q)", rec.Code, rec.Body.String())
	}
	if f.fileRenameMsgID != 9 || f.fileRenameNewName != "renamed.txt" {
		t.Fatalf("rename args msgID=%d name=%q", f.fileRenameMsgID, f.fileRenameNewName)
	}
	if f.fileRenameFolder.ChannelID != 1 {
		t.Fatalf("rename folder = %+v", f.fileRenameFolder)
	}
}

func TestMoveFile_ReturnsNewMsgID(t *testing.T) {
	f := &fakeDrive{newMsgID: 1234}
	rec := doJSON(t, mountDrive(f), http.MethodPost, "/files/move",
		`{"src":{"channel_id":1,"access_hash":2},"msg_id":5,"dst":{"channel_id":3,"access_hash":4}}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if f.moveSrc.ChannelID != 1 || f.moveDst.ChannelID != 3 || f.moveMsgID != 5 {
		t.Fatalf("move args src=%+v dst=%+v msgID=%d", f.moveSrc, f.moveDst, f.moveMsgID)
	}
	var out struct {
		NewMsgID int `json:"new_msg_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.NewMsgID != 1234 {
		t.Fatalf("new_msg_id = %d, want 1234", out.NewMsgID)
	}
}

func TestDeleteFile_Returns204WithArgs(t *testing.T) {
	f := &fakeDrive{}
	rec := doJSON(t, mountDrive(f), http.MethodDelete, "/files",
		`{"channel_id":1,"access_hash":2,"msg_id":8}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204 (body %q)", rec.Code, rec.Body.String())
	}
	if f.deleteFileMsgID != 8 || f.deleteFileFolder.ChannelID != 1 {
		t.Fatalf("delete args msgID=%d folder=%+v", f.deleteFileMsgID, f.deleteFileFolder)
	}
}

func TestCreateFolder_MalformedBodyReturns400(t *testing.T) {
	f := &fakeDrive{}
	rec := doJSON(t, mountDrive(f), http.MethodPost, "/folders", `{"name":`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 (body %q)", rec.Code, rec.Body.String())
	}
	if f.createdName != "" {
		t.Fatalf("CreateFolder should not be called, got %q", f.createdName)
	}
}

func TestCreateFolder_BusinessErrorReturnsErrorEnvelope(t *testing.T) {
	f := &fakeDrive{err: context.DeadlineExceeded}
	rec := doJSON(t, mountDrive(f), http.MethodPost, "/folders", `{"name":"x"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500 (body %q)", rec.Code, rec.Body.String())
	}
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	if out.Error.Code == "" || out.Error.Message == "" {
		t.Fatalf("error envelope incomplete: %q", rec.Body.String())
	}
}

// TestDriveRoutes_WiredIntoRouter verifies the routes are reachable through the
// full router behind the API key middleware.
func TestDriveRoutes_WiredIntoRouter(t *testing.T) {
	f := &fakeDrive{folders: []dialogs.Folder{{ChannelID: 1, AccessHash: 2, Title: "[TC] x"}}}
	r := NewRouter(Deps{APIKeyHashHex: hashOf("k"), Drive: f})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/folders", nil)
	req.Header.Set("X-API-Key", "k")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("wired /folders = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
}
