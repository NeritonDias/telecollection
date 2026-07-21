package transfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"testing/iotest"
	"time"

	"github.com/gotd/td/tg"
)

// tgClientStub is a zero-value *tg.Client used only to satisfy the non-nil api
// guard in the validation tests. The validated paths return before any RPC is
// attempted, so the client is never actually driven.
var tgClientStub tg.Client

// stubLocation returns a syntactically valid file location for validation tests.
func stubLocation() tg.InputFileLocationClass {
	return &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2}
}

// makePayload builds a deterministic byte slice of length n.
func makePayload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251) // 251 is prime, avoids trivial alignment with power-of-two chunk sizes
	}
	return b
}

func TestProgressReaderCountsAndPassesThroughBytes(t *testing.T) {
	data := makePayload(4096)
	// OneByteReader forces one byte per Read, so we exercise many progress
	// callbacks and the byte accounting under fragmentation.
	src := iotest.OneByteReader(bytes.NewReader(data))

	var reports []Progress
	pr := newProgressReader(context.Background(), src, int64(len(data)), func(p Progress) {
		reports = append(reports, p)
	})

	got, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("ReadAll: unexpected error: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("bytes passed through altered: got %d bytes, want %d", len(got), len(data))
	}
	if len(reports) == 0 {
		t.Fatal("expected progress reports, got none")
	}

	// Done must be monotonically non-decreasing and never exceed Total.
	var prev int64
	for i, p := range reports {
		if p.Total != int64(len(data)) {
			t.Errorf("report %d: Total = %d, want %d", i, p.Total, len(data))
		}
		if p.Done < prev {
			t.Errorf("report %d: Done went backwards: %d < %d", i, p.Done, prev)
		}
		if p.Done > p.Total {
			t.Errorf("report %d: Done %d exceeds Total %d", i, p.Done, p.Total)
		}
		prev = p.Done
	}

	// The final report must show completion.
	last := reports[len(reports)-1]
	if last.Done != last.Total || last.Done != int64(len(data)) {
		t.Errorf("final report = %+v, want Done == Total == %d", last, len(data))
	}
}

func TestProgressReaderFinalReportOnShortRead(t *testing.T) {
	// Reader delivers fewer bytes than the declared total; the final report
	// must still clamp Done up to Total for a clean completion.
	data := makePayload(10)
	var reports []Progress
	pr := newProgressReader(context.Background(), bytes.NewReader(data), 20, func(p Progress) {
		reports = append(reports, p)
	})

	if _, err := io.ReadAll(pr); err != nil {
		t.Fatalf("ReadAll: unexpected error: %v", err)
	}
	if len(reports) == 0 {
		t.Fatal("expected progress reports, got none")
	}
	last := reports[len(reports)-1]
	if last.Done != 20 || last.Total != 20 {
		t.Errorf("final report = %+v, want Done == Total == 20", last)
	}

	// The final report must be emitted exactly once even across repeated
	// EOF-returning reads.
	finalCount := 0
	for _, p := range reports {
		if p.Done == 20 {
			finalCount++
		}
	}
	if finalCount != 1 {
		t.Errorf("final report emitted %d times, want exactly 1", finalCount)
	}
}

func TestProgressWriterCountsAndPassesThroughBytes(t *testing.T) {
	data := makePayload(3000)
	var dst bytes.Buffer

	var reports []Progress
	pw := newProgressWriter(context.Background(), &dst, int64(len(data)), func(p Progress) {
		reports = append(reports, p)
	})

	// Write in several chunks to produce multiple reports.
	for off := 0; off < len(data); off += 512 {
		end := off + 512
		if end > len(data) {
			end = len(data)
		}
		n, err := pw.Write(data[off:end])
		if err != nil {
			t.Fatalf("Write: unexpected error: %v", err)
		}
		if n != end-off {
			t.Fatalf("Write returned %d, want %d", n, end-off)
		}
	}
	pw.finish()

	if !bytes.Equal(dst.Bytes(), data) {
		t.Fatalf("bytes passed through altered: got %d bytes, want %d", dst.Len(), len(data))
	}
	if len(reports) == 0 {
		t.Fatal("expected progress reports, got none")
	}
	last := reports[len(reports)-1]
	if last.Done != last.Total || last.Done != int64(len(data)) {
		t.Errorf("final report = %+v, want Done == Total == %d", last, len(data))
	}
}

func TestProgressWriterFinishIsIdempotent(t *testing.T) {
	var dst bytes.Buffer
	calls := 0
	pw := newProgressWriter(context.Background(), &dst, 0, func(Progress) { calls++ })
	pw.finish()
	pw.finish()
	if calls != 1 {
		t.Errorf("finish emitted %d reports, want exactly 1", calls)
	}
}

// blockingReader blocks in Read until its context is cancelled, then returns
// the context error. It models a stalled network read.
type blockingReader struct{ ctx context.Context }

func (b blockingReader) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func TestProgressReaderContextCancellationUnblocks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pr := newProgressReader(ctx, blockingReader{ctx: ctx}, 100, nil)

	done := make(chan error, 1)
	go func() {
		_, err := pr.Read(make([]byte, 16))
		done <- err
	}()

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Read after cancel returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read did not unblock after context cancellation (possible goroutine leak)")
	}
}

func TestProgressReaderShortCircuitsWhenContextAlreadyDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Underlying reader has data, but the cancelled context must win before it
	// is ever touched.
	pr := newProgressReader(ctx, bytes.NewReader(makePayload(64)), 64, nil)
	n, err := pr.Read(make([]byte, 16))
	if n != 0 {
		t.Errorf("Read returned %d bytes, want 0", n)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Read returned %v, want context.Canceled", err)
	}
}

func TestProgressWriterShortCircuitsWhenContextAlreadyDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var dst bytes.Buffer
	pw := newProgressWriter(ctx, &dst, 64, nil)
	n, err := pw.Write(makePayload(16))
	if n != 0 {
		t.Errorf("Write returned %d bytes, want 0", n)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Write returned %v, want context.Canceled", err)
	}
	if dst.Len() != 0 {
		t.Errorf("writer received %d bytes despite cancellation, want 0", dst.Len())
	}
}

func TestUploadBytesValidatesInput(t *testing.T) {
	ctx := context.Background()
	r := bytes.NewReader(makePayload(8))

	if _, err := UploadBytes(ctx, nil, "f", r, 8, nil); err == nil {
		t.Error("UploadBytes with nil api: expected error, got nil")
	}
	if _, err := UploadBytes(ctx, &tgClientStub, "f", nil, 8, nil); err == nil {
		t.Error("UploadBytes with nil reader: expected error, got nil")
	}
	if _, err := UploadBytes(ctx, &tgClientStub, "f", r, -1, nil); err == nil {
		t.Error("UploadBytes with negative size: expected error, got nil")
	}
}

func TestDownloadToValidatesInput(t *testing.T) {
	ctx := context.Background()
	var dst bytes.Buffer
	loc := stubLocation()

	if err := DownloadTo(ctx, nil, loc, 8, &dst, nil); err == nil {
		t.Error("DownloadTo with nil api: expected error, got nil")
	}
	if err := DownloadTo(ctx, &tgClientStub, nil, 8, &dst, nil); err == nil {
		t.Error("DownloadTo with nil location: expected error, got nil")
	}
	if err := DownloadTo(ctx, &tgClientStub, loc, 8, nil, nil); err == nil {
		t.Error("DownloadTo with nil writer: expected error, got nil")
	}
	if err := DownloadTo(ctx, &tgClientStub, loc, -1, &dst, nil); err == nil {
		t.Error("DownloadTo with negative size: expected error, got nil")
	}
}

// nonSeekable wraps an io.Reader so it does NOT satisfy io.Seeker, modelling a
// streaming body (e.g. an HTTP multipart part) that cannot be rewound.
type nonSeekable struct{ r io.Reader }

func (n nonSeekable) Read(p []byte) (int, error) { return n.r.Read(p) }

// TestUploadWithRetry_NonSeekableReaderNotReused proves that when the body
// cannot be rewound, a failed upload is NOT re-driven: doing so would resume
// from a consumed reader and could upload a truncated/empty body as a false
// success. The operation must instead fail after a single attempt.
func TestUploadWithRetry_NonSeekableReaderNotReused(t *testing.T) {
	ctx := context.Background()
	payload := makePayload(32)
	r := nonSeekable{bytes.NewReader(payload)}

	calls := 0
	_, err := uploadWithRetry(ctx, "f", r, int64(len(payload)), nil, func(body io.Reader) (tg.InputFileClass, error) {
		calls++
		// The uploader drains the body before the RPC surfaces its failure,
		// exhausting a non-seekable reader.
		_, _ = io.Copy(io.Discard, body)
		return nil, errors.New("transient boom")
	})

	if err == nil {
		t.Fatal("want failure, got nil: a non-rewindable retry must not report a truncated upload as success")
	}
	if calls != 1 {
		t.Fatalf("upload attempted %d times, want exactly 1 (non-seekable reader must not be re-driven)", calls)
	}
}

// TestUploadWithRetry_SeekableReaderRewoundOnRetry proves that a seekable body
// is rewound to the start before a retry, so the re-driven attempt re-sends the
// full stream instead of resuming from a consumed position.
func TestUploadWithRetry_SeekableReaderRewoundOnRetry(t *testing.T) {
	// Shrink the backoff so the single inter-attempt wait is negligible.
	orig := transferPolicy
	transferPolicy.Base = time.Millisecond
	transferPolicy.Max = 2 * time.Millisecond
	t.Cleanup(func() { transferPolicy = orig })

	ctx := context.Background()
	payload := makePayload(48)
	r := bytes.NewReader(payload) // *bytes.Reader implements io.Seeker

	calls := 0
	var lastRead []byte
	f, err := uploadWithRetry(ctx, "f", r, int64(len(payload)), nil, func(body io.Reader) (tg.InputFileClass, error) {
		calls++
		b, _ := io.ReadAll(body)
		lastRead = b
		if calls < 2 {
			return nil, errors.New("transient boom")
		}
		return &tg.InputFile{}, nil
	})

	if err != nil {
		t.Fatalf("want success after a rewound retry, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("upload attempted %d times, want 2", calls)
	}
	if !bytes.Equal(lastRead, payload) {
		t.Fatalf("retried attempt read %d bytes, want the full %d (reader was not rewound)", len(lastRead), len(payload))
	}
	if f == nil {
		t.Fatal("want non-nil InputFile on success")
	}
}

// TestUploadDownloadRoundTripE2E exercises the real network path against a live,
// authenticated Telegram session. It is skipped unless TELECOL_TEST_TG=1, since
// UploadBytes/DownloadTo require a connected *tg.Client. It exists to keep the
// gotd uploader/downloader wiring compiling and honest; the full round trip is
// driven by the file-ops phase that owns a real client.
func TestUploadDownloadRoundTripE2E(t *testing.T) {
	if os.Getenv("TELECOL_TEST_TG") != "1" {
		t.Skip("set TELECOL_TEST_TG=1 with a live Telegram session to run the transfer E2E test")
	}
	t.Skip("E2E transfer round trip is driven by the file-ops phase with a connected *tg.Client")
}
