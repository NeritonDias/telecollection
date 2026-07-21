package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/telecollection/telecollection/internal/drive"
	"github.com/telecollection/telecollection/internal/telegram/dialogs"
)

// Upload ceilings. maxUploadBytes caps the whole multipart request body as a
// defence against memory/disk exhaustion (the original project had no such
// limit); uploadMemoryBudget is how much of the file part is buffered in memory
// before the rest spills to a temp file during parsing.
const (
	maxUploadBytes     = int64(4) << 30 // 4 GiB
	uploadMemoryBudget = 16 << 20       // 16 MiB
)

// folderJSON is the wire representation of dialogs.Folder using snake_case keys.
type folderJSON struct {
	ChannelID  int64  `json:"channel_id"`
	AccessHash int64  `json:"access_hash"`
	Title      string `json:"title"`
}

func (f folderJSON) toFolder() dialogs.Folder {
	return dialogs.Folder{ChannelID: f.ChannelID, AccessHash: f.AccessHash, Title: f.Title}
}

func folderToJSON(f dialogs.Folder) folderJSON {
	return folderJSON{ChannelID: f.ChannelID, AccessHash: f.AccessHash, Title: f.Title}
}

// RegisterDriveRoutes mounts the drive endpoints onto r. It depends only on the
// drive.Service contract, so it can be wired ahead of the concrete
// orchestration. Progress reporting (SSE) is added in a later phase, so every
// handler passes a nil onProgress callback.
//
// Routes (relative to the mount point):
//
//	GET    /folders                    -> 200 [{channel_id, access_hash, title}]
//	POST   /folders   {name}           -> 200 {channel_id, access_hash, title}
//	PATCH  /folders   {folder,new_name}-> 204
//	DELETE /folders   {folder}         -> 204
//	GET    /files     ?folder | {folder}        -> 200 [store.File]
//	POST   /files     multipart(file,folder,name,mime) -> 200 store.File
//	GET    /files/download ?folder,msg_id,name  -> stream + Content-Disposition
//	PATCH  /files     {folder,msg_id,new_name}  -> 204
//	POST   /files/move {src,msg_id,dst}         -> 200 {new_msg_id}
//	DELETE /files     {folder,msg_id}           -> 204
func RegisterDriveRoutes(r chi.Router, svc drive.Service) {
	r.Get("/folders", func(w http.ResponseWriter, r *http.Request) {
		folders, err := svc.ListFolders(r.Context())
		if err != nil {
			writeDriveError(w, err)
			return
		}
		out := make([]folderJSON, 0, len(folders))
		for _, f := range folders {
			out = append(out, folderToJSON(f))
		}
		writeJSON(w, http.StatusOK, out)
	})

	r.Post("/folders", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		folder, err := svc.CreateFolder(r.Context(), body.Name)
		if err != nil {
			writeDriveError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, folderToJSON(folder))
	})

	r.Patch("/folders", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			folderJSON
			NewName string `json:"new_name"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if err := svc.RenameFolder(r.Context(), body.toFolder(), body.NewName); err != nil {
			writeDriveError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	r.Delete("/folders", func(w http.ResponseWriter, r *http.Request) {
		var body folderJSON
		if !decodeBody(w, r, &body) {
			return
		}
		if err := svc.DeleteFolder(r.Context(), body.toFolder()); err != nil {
			writeDriveError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	r.Get("/files", func(w http.ResponseWriter, r *http.Request) {
		folder, ok := folderFromRequest(w, r)
		if !ok {
			return
		}
		files, err := svc.ListFiles(r.Context(), folder)
		if err != nil {
			writeDriveError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, files)
	})

	r.Post("/files", func(w http.ResponseWriter, r *http.Request) {
		// Cap the whole request body before parsing so an oversized upload is
		// rejected instead of exhausting memory/disk.
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
		// The body is bounded by MaxBytesReader above, so form parsing cannot
		// exhaust memory/disk; gosec's G120 cannot see that wrap.
		if err := r.ParseMultipartForm(uploadMemoryBudget); err != nil { //nolint:gosec // body capped by MaxBytesReader above
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid multipart body")
			return
		}
		channelID, err1 := strconv.ParseInt(r.FormValue("channel_id"), 10, 64)
		accessHash, err2 := strconv.ParseInt(r.FormValue("access_hash"), 10, 64)
		if err1 != nil || err2 != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid folder identifiers")
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "missing file part")
			return
		}
		defer func() { _ = file.Close() }()

		name := r.FormValue("name")
		if name == "" {
			name = header.Filename
		}
		folder := dialogs.Folder{ChannelID: channelID, AccessHash: accessHash, Title: r.FormValue("title")}
		// file is streamed to the service as an io.Reader; size comes from the
		// multipart part header.
		out, err := svc.UploadFile(r.Context(), folder, name, file, header.Size, r.FormValue("mime"), nil)
		if err != nil {
			writeDriveError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})

	r.Get("/files/download", func(w http.ResponseWriter, r *http.Request) {
		folder, msgID, name, ok := fileRefFromRequest(w, r)
		if !ok {
			return
		}
		if name == "" {
			name = fmt.Sprintf("file-%d", msgID)
		}
		// Header values are staged here but not committed until the first write.
		// If the service fails before writing anything, the staged attachment
		// header must be dropped so the 500 JSON envelope does not go out with a
		// Content-Disposition attachment (writeJSON overrides Content-Type, but
		// Content-Disposition would otherwise linger).
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", contentDisposition(name))
		sw := &streamWriter{w: w}
		if err := svc.DownloadFile(r.Context(), folder, msgID, sw, nil); err != nil {
			if !sw.wrote {
				w.Header().Del("Content-Disposition")
				writeDriveError(w, err)
			}
			return
		}
	})

	r.Patch("/files", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			folderJSON
			MsgID   int    `json:"msg_id"`
			NewName string `json:"new_name"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if err := svc.RenameFile(r.Context(), body.toFolder(), body.MsgID, body.NewName); err != nil {
			writeDriveError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	r.Post("/files/move", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Src   folderJSON `json:"src"`
			MsgID int        `json:"msg_id"`
			Dst   folderJSON `json:"dst"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		newMsgID, err := svc.MoveFile(r.Context(), body.Src.toFolder(), body.MsgID, body.Dst.toFolder())
		if err != nil {
			writeDriveError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"new_msg_id": newMsgID})
	})

	r.Delete("/files", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			folderJSON
			MsgID int `json:"msg_id"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if err := svc.DeleteFile(r.Context(), body.toFolder(), body.MsgID); err != nil {
			writeDriveError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// folderFromRequest reads a folder from the query string (channel_id,
// access_hash, title) when present, otherwise from a JSON body. On failure it
// writes a 400 and returns false.
func folderFromRequest(w http.ResponseWriter, r *http.Request) (dialogs.Folder, bool) {
	q := r.URL.Query()
	if q.Get("channel_id") != "" || q.Get("access_hash") != "" {
		channelID, err1 := strconv.ParseInt(q.Get("channel_id"), 10, 64)
		accessHash, err2 := strconv.ParseInt(q.Get("access_hash"), 10, 64)
		if err1 != nil || err2 != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid folder identifiers")
			return dialogs.Folder{}, false
		}
		return dialogs.Folder{ChannelID: channelID, AccessHash: accessHash, Title: q.Get("title")}, true
	}
	var body folderJSON
	if !decodeBody(w, r, &body) {
		return dialogs.Folder{}, false
	}
	return body.toFolder(), true
}

// fileRefFromRequest reads folder + msg_id + name from the query string when a
// folder is present there, otherwise from a JSON body. On failure it writes a
// 400 and returns false.
func fileRefFromRequest(w http.ResponseWriter, r *http.Request) (dialogs.Folder, int, string, bool) {
	q := r.URL.Query()
	if q.Get("channel_id") != "" || q.Get("access_hash") != "" {
		channelID, err1 := strconv.ParseInt(q.Get("channel_id"), 10, 64)
		accessHash, err2 := strconv.ParseInt(q.Get("access_hash"), 10, 64)
		msgID, err3 := strconv.Atoi(q.Get("msg_id"))
		if err1 != nil || err2 != nil || err3 != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid file reference")
			return dialogs.Folder{}, 0, "", false
		}
		folder := dialogs.Folder{ChannelID: channelID, AccessHash: accessHash, Title: q.Get("title")}
		return folder, msgID, q.Get("name"), true
	}
	var body struct {
		folderJSON
		MsgID int    `json:"msg_id"`
		Name  string `json:"name"`
	}
	if !decodeBody(w, r, &body) {
		return dialogs.Folder{}, 0, "", false
	}
	return body.toFolder(), body.MsgID, body.Name, true
}

// contentDisposition builds an attachment Content-Disposition header value with
// the filename sanitized: CR/LF are stripped (header-injection defence) and
// backslashes/quotes are escaped so they cannot terminate the quoted-string.
func contentDisposition(name string) string {
	replacer := strings.NewReplacer(
		"\\", `\\`,
		`"`, `\"`,
		"\r", "",
		"\n", "",
	)
	return fmt.Sprintf(`attachment; filename="%s"`, replacer.Replace(name))
}

// streamWriter records whether any bytes have been written, so the download
// handler can tell whether it is still safe to replace the response with an
// error envelope after a service failure.
type streamWriter struct {
	w     http.ResponseWriter
	wrote bool
}

func (s *streamWriter) Write(p []byte) (int, error) {
	s.wrote = true
	return s.w.Write(p)
}

// writeDriveError maps a business error to a 500 JSON envelope. The wire message
// is generic so internal error details are not leaked. It reuses the shared
// error format from auth_routes.go.
func writeDriveError(w http.ResponseWriter, _ error) {
	writeError(w, http.StatusInternalServerError, "DRIVE_ERROR", "drive request failed")
}
