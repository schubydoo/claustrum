package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestWatchedBodyEmitsProgressLine checks the actual stdout emission (prefix,
// exact leading line, newline) — the marshaling test pins only the JSON shape, so
// without this a broken __INSTALL_PROGRESS__ prefix, a dropped newline, or a
// silenced ticker would ship green.
func TestWatchedBodyEmitsProgressLine(t *testing.T) {
	oldProg := installProgressInterval
	installProgressInterval = 5 * time.Millisecond
	oldIdle := installIdleTimeout
	installIdleTimeout = time.Hour
	t.Cleanup(func() { installProgressInterval = oldProg; installIdleTimeout = oldIdle })

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	// Drain the read end CONCURRENTLY so a full pipe buffer can never wedge the ticker
	// (and thus Close's join). Collect the bytes after w closes.
	captured := make(chan string, 1)
	go func() { b, _ := io.ReadAll(r); captured <- string(b) }()

	// An io.Pipe body: Read blocks until the writer runs, so the download streams
	// through the watched body and at least one progress line is emitted.
	pr, pw := io.Pipe()
	wb := newWatchedBody(pr, 5, time.Now())
	go func() { _, _ = pw.Write([]byte("hello")); _ = pw.Close() }()
	_, _ = io.ReadAll(wb)
	_ = wb.Close() // joins the ticker; no os.Stdout write can follow
	_ = w.Close()
	os.Stdout = old

	out := <-captured
	// Assert an actual __INSTALL_PROGRESS__ line reached stdout with the right prefix,
	// shape, total, and trailing newline. The bytes value races the ticker vs the read,
	// so it is not pinned here; the marshaling test pins the exact JSON.
	if !strings.Contains(out, "__INSTALL_PROGRESS__{\"phase\":\"download\",\"bytes\":") ||
		!strings.Contains(out, ",\"total\":5}\n") {
		t.Errorf("stdout missing a well-formed __INSTALL_PROGRESS__ line, got:\n%s", out)
	}
}

// The 4534d86 install-download frames are byte-exact: fetch comes LAST in
// __INSTALL_RESULT__ (after cliError), the fetch object is bytes/ms/longestPauseMs,
// and a progress line is phase/bytes/total with total omitempty.
func TestInstallFetchMarshaling(t *testing.T) {
	cases := []struct {
		name string
		val  any
		want string
	}{
		{"facts with fetch after cliError",
			installFacts{ServerVersion: "v", OS: "linux", Arch: "amd64", Libc: "glibc", CliPath: "/p", CliError: "download stalled: x", Fetch: &fetchStats{Bytes: 4, Ms: 1000, LongestPauseMs: 1000}},
			`{"serverVersion":"v","os":"linux","arch":"amd64","libc":"glibc","cliPath":"/p","cliWasPresent":false,"cliError":"download stalled: x","fetch":{"bytes":4,"ms":1000,"longestPauseMs":1000}}`},
		{"facts without fetch (nil) drop the field",
			installFacts{ServerVersion: "v", OS: "linux", Arch: "amd64", Libc: "glibc", CliPath: "/p"},
			`{"serverVersion":"v","os":"linux","arch":"amd64","libc":"glibc","cliPath":"/p","cliWasPresent":false}`},
		{"fetch object field order", fetchStats{Bytes: 30, Ms: 4, LongestPauseMs: 0},
			`{"bytes":30,"ms":4,"longestPauseMs":0}`},
		{"progress with total", progressLine{Phase: "download", Bytes: 5, Total: 10},
			`{"phase":"download","bytes":5,"total":10}`},
		{"progress without total (chunked) drops total", progressLine{Phase: "download", Bytes: 5},
			`{"phase":"download","bytes":5}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.val)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if got := string(b); got != tc.want {
				t.Errorf("\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// fetchToFile aborts a stalled -cli-url download at the always-on read-idle
// timeout (4534d86 parity) and records the fetch stats. A 1s timeout keeps the
// message ("no data for 1s after 4/...") realistic while staying fast.
func TestFetchToFileAbortsOnReadIdleStall(t *testing.T) {
	oldIdle := installIdleTimeout
	installIdleTimeout = time.Second
	oldProg := installProgressInterval
	installProgressInterval = time.Hour // only the leading bytes:0 line, quiet output
	t.Cleanup(func() { installIdleTimeout = oldIdle; installProgressInterval = oldProg })

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("abcd")) // 4 bytes, then go silent
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-block // stall: never send the rest
	}))
	// srv.Close blocks until the handler returns, so unblock it FIRST (defers run
	// LIFO — this runs before srv.Close).
	defer srv.Close()
	defer close(block)

	lastInstallFetch = nil
	_, _, err := fetchToFile(srv.URL, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "download stalled: no data for 1s after 4/") {
		t.Fatalf("fetchToFile on a stall = %v, want a 'download stalled: no data for 1s after 4/...' error", err)
	}
	if lastInstallFetch == nil || lastInstallFetch.Bytes != 4 {
		t.Fatalf("fetch stats = %+v, want bytes=4", lastInstallFetch)
	}
	if lastInstallFetch.LongestPauseMs < 1000 {
		t.Errorf("longestPauseMs = %d, want >= 1000 (the stall gap)", lastInstallFetch.LongestPauseMs)
	}
}

// A completed -cli-url download records fetch stats (bytes = body length,
// longestPauseMs 0 for a single fast read), and a slow-but-progressing body is
// NOT aborted by the read-idle timeout (it is read-idle, not a total deadline).
func TestFetchToFileRecordsStatsAndSurvivesSlowProgress(t *testing.T) {
	oldIdle := installIdleTimeout
	installIdleTimeout = time.Second
	oldProg := installProgressInterval
	installProgressInterval = time.Hour // only the leading bytes:0 line, quiet output
	t.Cleanup(func() { installIdleTimeout = oldIdle; installProgressInterval = oldProg })

	// Five chunks 250ms apart (total ~1.25s > the 1s idle timeout): each gap is well
	// under idle (4:1 margin, robust on a loaded CI runner), so every read resets the
	// clock and the download completes — proving the abort is read-idle, not a total
	// deadline.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i < 5; i++ {
			_, _ = w.Write([]byte("xx"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(250 * time.Millisecond)
		}
	}))
	defer srv.Close()

	lastInstallFetch = nil
	_, _, err := fetchToFile(srv.URL, t.TempDir())
	if err != nil {
		t.Fatalf("slow-but-progressing download must not abort, got: %v", err)
	}
	if lastInstallFetch == nil || lastInstallFetch.Bytes != 10 {
		t.Fatalf("fetch stats = %+v, want bytes=10", lastInstallFetch)
	}
}

// TestWatchedBodyTickerEmitsPeriodically holds the body open past several ticker
// intervals so the periodic `case <-t.C` branch fires: with a body that streams no
// bytes, only the ticker produces __INSTALL_PROGRESS__ lines, so more than the one
// leading bytes:0 line proves the tick branch (not just the leading emit) ran.
func TestWatchedBodyTickerEmitsPeriodically(t *testing.T) {
	oldProg := installProgressInterval
	installProgressInterval = 5 * time.Millisecond
	oldIdle := installIdleTimeout
	installIdleTimeout = time.Hour
	t.Cleanup(func() { installProgressInterval = oldProg; installIdleTimeout = oldIdle })

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	// Drain the read end CONCURRENTLY so a full pipe buffer can never wedge the ticker
	// (and thus Close's join). Collect the bytes after w closes.
	captured := make(chan string, 1)
	go func() { b, _ := io.ReadAll(r); captured <- string(b) }()

	// A body that never yields a byte: the read blocks until wb.Close closes it, so
	// every progress line on stdout comes from the ticker, not from a read.
	pr, _ := io.Pipe()
	wb := newWatchedBody(pr, 0, time.Now())
	time.Sleep(60 * time.Millisecond) // ~12 intervals: at least one <-t.C fires
	_ = wb.Close()                    // joins the ticker; no os.Stdout write can follow
	_ = w.Close()
	os.Stdout = old

	out := <-captured
	// The leading emit is one line; any additional line can only be a ticker tick.
	if n := strings.Count(out, "__INSTALL_PROGRESS__"); n < 2 {
		t.Errorf("want >= 2 __INSTALL_PROGRESS__ lines (leading + at least one tick), got %d:\n%s", n, out)
	}
}
