// Package transfer is the byte-transfer engine that sits under the file
// operations of later phases. It streams uploads and downloads through Telegram
// while reporting progress, honouring context cancellation and retrying
// whole-operation transient failures via internal/telegram/retry.
//
// The offline-testable core is the progressReader/progressWriter pair: they
// wrap an io.Reader/io.Writer, count the bytes flowing through, forward them
// untouched and invoke an onProgress callback (with a guaranteed final report
// where Done == Total). The network entry points UploadBytes and DownloadTo
// build on top of gotd's uploader/downloader and require an authenticated
// *tg.Client, so their happy path is exercised end-to-end (guarded by the
// TELECOL_TEST_TG env var), while validation and the progress/cancellation
// plumbing are covered offline.
//
// No secret material is logged. Errors are wrapped with %w.
package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"

	"github.com/telecollection/telecollection/internal/telegram/retry"
)

// Progress reports how many bytes have been transferred (Done) out of the total
// expected (Total). Total may be 0 when the size is unknown; callers should not
// divide by it without checking.
type Progress struct {
	Done  int64
	Total int64
}

// transferPolicy governs the whole-operation retry loop for uploads and
// downloads. Part-level resilience (FLOOD_WAIT, CDN redirects, per-chunk
// retries) already lives inside gotd's uploader/downloader and the client
// transport middleware; this policy only re-drives the operation for transient
// failures that surface at the RPC boundary.
var transferPolicy = retry.Policy{
	MaxAttempts: 3,
	Base:        time.Second,
	Max:         30 * time.Second,
}

// UploadBytes uploads exactly size bytes read from r and returns the resulting
// gotd InputFile, ready to be attached to a message. Progress is reported as
// bytes are read, with a final report where Done == Total. Cancellation is
// honoured through ctx, and whole-operation transient failures are retried via
// retry.Do.
//
// Caveat: r is consumed as it is read. A retry re-invokes the RPC, but a
// non-seekable reader cannot be rewound, so a retry after r has been partially
// consumed will not re-send earlier bytes. The dominant transient case
// (FLOOD_WAIT and per-part failures) is handled inside gotd before r is
// exhausted; the outer retry guards failures raised before the stream is read.
func UploadBytes(ctx context.Context, api *tg.Client, name string, r io.Reader, size int64, onProgress func(Progress)) (tg.InputFileClass, error) {
	if api == nil {
		return nil, errors.New("transfer: api client is required")
	}
	if r == nil {
		return nil, errors.New("transfer: reader is required")
	}
	if size < 0 {
		return nil, fmt.Errorf("transfer: size must be non-negative, got %d", size)
	}

	pr := newProgressReader(ctx, r, size, onProgress)
	up := uploader.NewUploader(api)

	var result tg.InputFileClass
	err := retry.Do(ctx, transferPolicy, func() error {
		pr.reset()
		f, upErr := up.Upload(ctx, uploader.NewUpload(name, pr, size))
		if upErr != nil {
			return upErr
		}
		result = f
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("transfer: uploading %q: %w", name, err)
	}
	return result, nil
}

// DownloadTo streams the file at loc into w, reporting progress as bytes are
// written and finishing with a report where Done == Total. size is the expected
// length, used only for progress reporting. Cancellation is honoured through
// ctx, and whole-operation transient failures are retried via retry.Do.
//
// Caveat: w is written to as bytes arrive and cannot be rewound. A retry
// restarts the stream from the beginning; if the failure happened after some
// bytes were already written, those bytes remain in w. Per-part retries are
// handled inside gotd's downloader, so the outer retry is a thin safety net for
// failures raised before any data is written.
func DownloadTo(ctx context.Context, api *tg.Client, loc tg.InputFileLocationClass, size int64, w io.Writer, onProgress func(Progress)) error {
	if api == nil {
		return errors.New("transfer: api client is required")
	}
	if loc == nil {
		return errors.New("transfer: file location is required")
	}
	if w == nil {
		return errors.New("transfer: writer is required")
	}
	if size < 0 {
		return fmt.Errorf("transfer: size must be non-negative, got %d", size)
	}

	pw := newProgressWriter(ctx, w, size, onProgress)
	d := downloader.NewDownloader()

	err := retry.Do(ctx, transferPolicy, func() error {
		pw.reset()
		_, dlErr := d.Download(api, loc).Stream(ctx, pw)
		return dlErr
	})
	if err != nil {
		return fmt.Errorf("transfer: downloading: %w", err)
	}

	// Stream carries no EOF signal to the writer, so emit the final report
	// explicitly once the download has completed successfully.
	pw.finish()
	return nil
}

// progressReader wraps an io.Reader, counting the bytes read and forwarding
// them untouched while invoking onProgress. It is context-aware: a cancelled
// ctx short-circuits Read with ctx.Err(), so a cancelled transfer unwinds
// instead of reading further. A final report (Done == Total) is emitted when
// the underlying reader reports io.EOF.
type progressReader struct {
	ctx        context.Context
	r          io.Reader
	total      int64
	done       int64
	onProgress func(Progress)
	finalSent  bool
}

func newProgressReader(ctx context.Context, r io.Reader, total int64, onProgress func(Progress)) *progressReader {
	return &progressReader{ctx: ctx, r: r, total: total, onProgress: onProgress}
}

// reset zeroes the byte counter so a retried attempt reports fresh progress.
func (pr *progressReader) reset() {
	pr.done = 0
	pr.finalSent = false
}

func (pr *progressReader) Read(p []byte) (int, error) {
	if pr.ctx != nil {
		if err := pr.ctx.Err(); err != nil {
			return 0, err
		}
	}
	n, err := pr.r.Read(p)
	if n > 0 {
		pr.done += int64(n)
		emitProgress(pr.onProgress, pr.done, pr.total, false, &pr.finalSent)
	}
	if errors.Is(err, io.EOF) {
		emitProgress(pr.onProgress, pr.done, pr.total, true, &pr.finalSent)
	}
	return n, err
}

// progressWriter wraps an io.Writer, counting the bytes written and forwarding
// them untouched while invoking onProgress. It is context-aware in the same way
// as progressReader. The final report is emitted by finish, since writes carry
// no natural end-of-stream signal.
type progressWriter struct {
	ctx        context.Context
	w          io.Writer
	total      int64
	done       int64
	onProgress func(Progress)
	finalSent  bool
}

func newProgressWriter(ctx context.Context, w io.Writer, total int64, onProgress func(Progress)) *progressWriter {
	return &progressWriter{ctx: ctx, w: w, total: total, onProgress: onProgress}
}

// reset zeroes the byte counter so a retried attempt reports fresh progress.
func (pw *progressWriter) reset() {
	pw.done = 0
	pw.finalSent = false
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	if pw.ctx != nil {
		if err := pw.ctx.Err(); err != nil {
			return 0, err
		}
	}
	n, err := pw.w.Write(p)
	if n > 0 {
		pw.done += int64(n)
		emitProgress(pw.onProgress, pw.done, pw.total, false, &pw.finalSent)
	}
	return n, err
}

// finish emits the final progress report (Done == Total) exactly once.
func (pw *progressWriter) finish() {
	emitProgress(pw.onProgress, pw.done, pw.total, true, &pw.finalSent)
}

// emitProgress invokes onProgress (when non-nil) with the current counters. On
// the final report it clamps Done up to Total so callers always observe a clean
// Done == Total completion, and it guards against sending the final report more
// than once via the finalSent flag.
func emitProgress(onProgress func(Progress), done, total int64, final bool, finalSent *bool) {
	if onProgress == nil {
		return
	}
	if final {
		if *finalSent {
			return
		}
		*finalSent = true
		if total >= 0 && done < total {
			done = total
		}
	}
	onProgress(Progress{Done: done, Total: total})
}
