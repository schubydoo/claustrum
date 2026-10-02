package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// installProgressInterval is the tick of the __INSTALL_PROGRESS__ lines on the
// -install download (4534d86). A tick prints only a changed byte count (see
// emitProgress). var so tests can shrink it.
var installProgressInterval = time.Second

// installIdleTimeout aborts the -cli-url download after this long with no bytes —
// the reference's always-on read-idle stall abort (4534d86, VM-measured at 60s).
// It is a READ-IDLE bound (reset on every byte), NOT a total-download deadline, so
// a slow-but-progressing download is never aborted. This is parity (the reference
// does it), not a divergence, and it is separate from the opt-in total-download
// bound D12 (cliDownloadTimeout), which no measurement of the reference has shown
// within the window measured. var so tests can shrink it.
var installIdleTimeout = 60 * time.Second

// installHeaderTimeout is how long the -cli-url download waits for the response
// headers. It is reference behavior and always on. Measured on f6010b97 and
// 89cb6289 (row G12, Linux and Windows): a server that reads the request and
// sends nothing gets a closed connection after 60.0 s, and the cliError is
// `download failed: Get "<url>": net/http: timeout awaiting response headers`.
// That is the text of the Go transport's ResponseHeaderTimeout. Not measured: the
// exact start point of the limit, macOS, and this limit together with an opted-in
// -cli-download-timeout below 60 s. var so tests can shrink it.
var installHeaderTimeout = 60 * time.Second

// lastInstallFetch holds the -cli-url download's fetch stats for the current
// -install run. -install is a one-shot, single-threaded process (the -serve daemon
// never installs), so a package var is the simplest way to carry the stats from
// fetchToFile up to runInstall without threading a param through ensureCLI's many
// callers. runInstall resets it to nil and reads it after ensureCLI. It is set only
// on the -cli-url path (fetchToFile), so it stays nil for -cli-zst and cache-hit
// runs, where the reference emits no `fetch` object.
var lastInstallFetch *fetchStats

// lastInstallFinal is the final __INSTALL_PROGRESS__ line of a complete -cli-url
// download. fetchToFile sets it, and ensureCLI prints it only after the checksum
// passes (emitFinalProgress). runInstall resets it with lastInstallFetch.
var lastInstallFinal *progressLine

// fetchStats is the `fetch` object 4534d86 adds to __INSTALL_RESULT__ whenever a
// -cli-url download was attempted, even a 0-byte 404. installFacts has its place.
// Field order bytes, ms, longestPauseMs, all always present. bytes is the total read, ms the
// download duration, longestPauseMs the largest gap between reads (the ~60s stall
// gap on an idle abort, and 0 on a clean single-read download). The wait from the
// last byte to a read error counts as a gap too (row E07).
type fetchStats struct {
	Bytes          int64 `json:"bytes"`
	Ms             int64 `json:"ms"`
	LongestPauseMs int64 `json:"longestPauseMs"`
}

// progressLine is one __INSTALL_PROGRESS__ frame. total carries the Content-Length
// and is omitempty: the reference drops it when the server sends none (chunked).
type progressLine struct {
	Phase string `json:"phase"`
	Bytes int64  `json:"bytes"`
	Total int64  `json:"total,omitempty"`
}

// watchedBody wraps the -cli-url response body to reproduce 4534d86's download
// instrumentation: a ~1s __INSTALL_PROGRESS__ ticker on -install stdout, a read-idle
// stall abort, and the fetch stats. The idle timer resets on every byte, so only a
// true stall (no data for installIdleTimeout) trips it.
type watchedBody struct {
	inner io.ReadCloser
	total int64

	mu      sync.Mutex
	bytes   int64
	printed int64 // the byte count of the last progress line
	longest time.Duration
	lastAt  time.Time
	hadRead bool
	stalled bool

	start      time.Time
	idle       *time.Timer
	stop       chan struct{}
	tickerDone chan struct{}
	once       sync.Once
}

// newWatchedBody wraps inner. start is the fetch's start time (before the request),
// so fetchStats.ms covers the whole exchange; lastAt is set to now (wrap time) so the
// read-idle clock and the first pause measure from when the body became readable.
func newWatchedBody(inner io.ReadCloser, total int64, start time.Time) *watchedBody {
	w := &watchedBody{inner: inner, total: total, lastAt: time.Now(), start: start, stop: make(chan struct{}), tickerDone: make(chan struct{})}
	// The leading bytes:0 line is printed HERE, before any Read, so it always
	// says 0. Printed from the ticker goroutine, it raced the first Read and
	// once said bytes:57 with no bytes:0 line. The reference printed bytes:0
	// first in every measured run.
	w.emitProgress(false)
	w.idle = time.AfterFunc(installIdleTimeout, w.onIdle)
	go w.ticker()
	return w
}

// onIdle fires when no byte arrived for installIdleTimeout: it records the stall
// gap (so longestPauseMs reports ~60000) and closes the body so a blocked Read
// returns at once.
func (w *watchedBody) onIdle() {
	w.mu.Lock()
	w.stalled = true
	if p := time.Since(w.lastAt); p > w.longest {
		w.longest = p
	}
	w.mu.Unlock()
	_ = w.inner.Close()
}

func (w *watchedBody) Read(p []byte) (int, error) {
	n, err := w.inner.Read(p)
	if n > 0 {
		now := time.Now()
		w.mu.Lock()
		// The start-to-first-read gap is not a pause between reads, so it is not
		// counted (the reference reports longestPauseMs 0 on a clean single-read
		// download).
		if w.hadRead {
			if pause := now.Sub(w.lastAt); pause > w.longest {
				w.longest = pause
			}
		}
		w.hadRead = true
		w.lastAt = now
		w.bytes += int64(n)
		w.mu.Unlock()
		w.idle.Reset(installIdleTimeout)
	}
	if err != nil {
		w.mu.Lock()
		stalled, got, total := w.stalled, w.bytes, w.total
		interrupted := !stalled && err != io.EOF && !isDownloadDeadline(err)
		if interrupted {
			if pause := time.Since(w.lastAt); pause > w.longest {
				w.longest = pause
			}
		}
		w.mu.Unlock()
		if stalled {
			return n, &stalledError{secs: int(installIdleTimeout / time.Second), got: got, total: total}
		}
		if interrupted {
			return n, &interruptedError{got: got, total: total, err: err}
		}
	}
	return n, err
}

// interruptedError is a body read that ended with a transport error. Measured on
// f6010b97 and 89cb6289 (row E07, Linux), with a connection reset after half the
// body. The answer is `download interrupted after 381089/762179 bytes: read tcp
// <a>-><b>: read: connection reset by peer`. It has no "download failed: "
// prefix. The wait from the last byte to the error is the longestPauseMs.
// ensureCLI returns it as-is.
//
// Not measured: a read error other than a reset, for example a body that ends
// before its Content-Length. Not measured: an error before the first body byte.
// Not measured: a body with no length. claustrum prints total 0 for it, as
// stalledError does.
type interruptedError struct {
	got, total int64
	err        error
}

func (e *interruptedError) Error() string {
	return fmt.Sprintf("download interrupted after %d/%d bytes: %v", e.got, e.total, e.err)
}

// isDownloadDeadline reports whether a body read error is the opted-in D12
// deadline (cliDownloadTimeout). That error keeps its documented text,
// `download failed: context deadline exceeded (…)`. It is claustrum-only, so no
// row measures it.
func isDownloadDeadline(err error) bool {
	var ne net.Error
	return cliDownloadTimeout > 0 && errors.As(err, &ne) && ne.Timeout()
}

// stalledError is a read-idle abort of a -cli-url download (4534d86 aborts a body
// that goes silent for installIdleTimeout). It is a distinct type so ensureCLI can
// return it as-is instead of under the "download failed: " prefix: the reference's
// cliError for a stall is the bare "download stalled: …" string, exactly as for a
// non-200 (httpStatusError). Measured against 4534d86
// (scratch/probe/install-frames-4534d86.md).
type stalledError struct {
	secs       int
	got, total int64
}

func (e *stalledError) Error() string {
	return fmt.Sprintf("download stalled: no data for %ds after %d/%d bytes", e.secs, e.got, e.total)
}

// Close stops the ticker and idle timer and closes the body. It WAITS for the
// ticker goroutine to exit before returning, so no stray __INSTALL_PROGRESS__ line
// can be emitted after the caller moves on to print __INSTALL_RESULT__. Idempotent.
func (w *watchedBody) Close() error {
	w.once.Do(func() { close(w.stop) })
	<-w.tickerDone
	w.idle.Stop()
	return w.inner.Close()
}

// stats returns the fetch object for __INSTALL_RESULT__.
func (w *watchedBody) stats() fetchStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return fetchStats{
		Bytes:          w.bytes,
		Ms:             time.Since(w.start).Milliseconds(),
		LongestPauseMs: w.longest.Milliseconds(),
	}
}

func (w *watchedBody) ticker() {
	defer close(w.tickerDone)
	t := time.NewTicker(installProgressInterval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			w.emitProgress(true)
		}
	}
}

// emitProgress prints one progress line. On a tick it prints nothing when the
// byte count equals the count of the last printed line. Measured on 89cb6289
// (rows G02 to G11). A body of 6 parts 20 s apart gives 7 lines, not one per
// second. A body with no byte gives the leading line only.
func (w *watchedBody) emitProgress(tick bool) {
	w.mu.Lock()
	if tick && w.bytes == w.printed {
		w.mu.Unlock()
		return
	}
	w.printed = w.bytes
	pl := progressLine{Phase: "download", Bytes: w.bytes}
	if w.total > 0 {
		pl.Total = w.total
	}
	w.mu.Unlock()
	printProgress(pl)
}

// emitFinalProgress prints lastInstallFinal, if a download completed. Not
// measured: whether the reference prints this last line when a tick printed the
// full count already. claustrum always prints it.
func emitFinalProgress() {
	if lastInstallFinal != nil {
		printProgress(*lastInstallFinal)
	}
}

func printProgress(pl progressLine) {
	b, _ := json.Marshal(pl)
	fmt.Printf("__INSTALL_PROGRESS__%s\n", b)
}
