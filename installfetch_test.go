package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
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

// The 4534d86 install-download frames are byte-exact: fetch comes after cliError,
// the fetch object is bytes/ms/longestPauseMs,
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

// captureStdout runs f with os.Stdout on a pipe and returns what f printed. The
// read end is drained concurrently, so a full pipe never blocks a writer.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	captured := make(chan string, 1)
	go func() { b, _ := io.ReadAll(r); captured <- string(b) }()
	os.Stdout = w
	f()
	_ = w.Close()
	os.Stdout = old
	out := <-captured
	_ = r.Close()
	return out
}

// progressCounts is the byte count of each __INSTALL_PROGRESS__ line of out, in
// order. withTotal reports whether every line carries a "total" key.
func progressCounts(t *testing.T, out string) (counts []int64, withTotal bool) {
	t.Helper()
	withTotal = true
	for _, l := range strings.Split(out, "\n") {
		body, ok := strings.CutPrefix(l, "__INSTALL_PROGRESS__")
		if !ok {
			continue
		}
		var pl struct {
			Bytes int64  `json:"bytes"`
			Total *int64 `json:"total"`
		}
		if err := json.Unmarshal([]byte(body), &pl); err != nil {
			t.Fatalf("progress line %q: %v", l, err)
		}
		counts = append(counts, pl.Bytes)
		if pl.Total == nil {
			withTotal = false
		}
	}
	return counts, withTotal
}

// smallBlob is a zstd blob of a few kilobytes, the same on every system. Its
// content is not a CLI, so an install of it ends as "not runnable" after the
// download. The progress tests need only the download.
func smallBlob(t *testing.T) []byte {
	t.Helper()
	raw := make([]byte, 4096)
	x := uint32(1)
	for i := range raw {
		x = x*1664525 + 1013904223
		raw[i] = byte(x >> 24)
	}
	return zstdOf(t, raw)
}

// Row G11. A body that never yields a byte prints the leading bytes:0
// line only. A tick prints nothing when the byte count equals the last printed
// count. The body stays open for about 12 ticks.
func TestWatchedBodyTickPrintsNothingWithoutNewBytes(t *testing.T) {
	oldProg := installProgressInterval
	installProgressInterval = 5 * time.Millisecond
	oldIdle := installIdleTimeout
	installIdleTimeout = time.Hour
	t.Cleanup(func() { installProgressInterval = oldProg; installIdleTimeout = oldIdle })

	out := captureStdout(t, func() {
		pr, _ := io.Pipe()
		wb := newWatchedBody(pr, 0, time.Now())
		time.Sleep(60 * time.Millisecond)
		_ = wb.Close() // joins the ticker, so no os.Stdout write follows
	})
	if want := "__INSTALL_PROGRESS__{\"phase\":\"download\",\"bytes\":0}\n"; out != want {
		t.Errorf("stdout %q, want only the leading line %q", out, want)
	}
}

// Rows G02, G06, G07. A body of two parts with a gap of many
// ticks prints three lines. They are the leading 0, the first part on the next
// tick, and the full count at the end. It does not print one line per tick. With no
// Content-Length the lines carry no total key, and the count rule is the same.
func TestInstallProgressPrintsOnlyChangedCounts(t *testing.T) {
	oldProg := installProgressInterval
	installProgressInterval = 40 * time.Millisecond
	t.Cleanup(func() { installProgressInterval = oldProg })

	body := smallBlob(t)
	half := len(body) / 2
	sum := sha256.Sum256(body)
	for _, tc := range []struct {
		name      string
		withTotal bool
	}{{"with a length (G02)", true}, {"chunked (G06)", false}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.withTotal {
					w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				}
				_, _ = w.Write(body[:half])
				w.(http.Flusher).Flush()
				time.Sleep(600 * time.Millisecond) // about 15 ticks
				_, _ = w.Write(body[half:])
			}))
			defer srv.Close()
			out := captureInstallOutput(t, installOpts{cliDir: filepath.Join(t.TempDir(), "cli"), cliVersion: "1.0.0",
				cliURL: srv.URL + "/cli.zst", cliChecksum: hex.EncodeToString(sum[:])})
			counts, withTotal := progressCounts(t, out)
			if withTotal != tc.withTotal {
				t.Errorf("the lines carry a total key: %v, want %v\n%s", withTotal, tc.withTotal, out)
			}
			// Not measured on the reference: a tick that prints the full count just
			// before the last line. claustrum then prints the full count twice.
			if n := len(counts); n >= 2 && counts[n-1] == counts[n-2] {
				counts = counts[:n-1]
			}
			want := []int64{0, int64(half), int64(len(body))}
			if !slices.Equal(counts, want) {
				t.Errorf("progress byte counts = %v, want %v (one line per changed count, not one per tick)", counts, want)
			}
		})
	}
}

// Rows G04, G05. With parts that arrive faster than the tick, each tick
// line shows a new count: no count is printed twice.
func TestInstallProgressTicksShowNewCounts(t *testing.T) {
	oldProg := installProgressInterval
	installProgressInterval = 60 * time.Millisecond
	t.Cleanup(func() { installProgressInterval = oldProg })

	body := smallBlob(t)
	sum := sha256.Sum256(body)
	const parts = 30
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		// The first 30 bytes go out one each 20 ms, then the rest.
		for i := 0; i < parts; i++ {
			_, _ = w.Write(body[i : i+1])
			w.(http.Flusher).Flush()
			time.Sleep(20 * time.Millisecond)
		}
		_, _ = w.Write(body[parts:])
	}))
	defer srv.Close()
	out := captureInstallOutput(t, installOpts{cliDir: filepath.Join(t.TempDir(), "cli"), cliVersion: "1.0.0",
		cliURL: srv.URL + "/cli.zst", cliChecksum: hex.EncodeToString(sum[:])})
	counts, _ := progressCounts(t, out)
	if n := len(counts); n >= 2 && counts[n-1] == counts[n-2] {
		counts = counts[:n-1] // see TestInstallProgressPrintsOnlyChangedCounts
	}
	if len(counts) < 4 || counts[0] != 0 || counts[len(counts)-1] != int64(len(body)) {
		t.Fatalf("progress byte counts = %v, want 0 first, %d last and tick lines between", counts, len(body))
	}
	for i := 1; i < len(counts); i++ {
		if counts[i] <= counts[i-1] {
			t.Errorf("progress byte counts = %v: line %d repeats or lowers the count", counts, i)
		}
	}
}

// Row G12. A server that reads the request and sends nothing is given up
// after the response-header limit. The cliError is the transport's text behind
// "download failed: ", no progress line is printed, and fetch has bytes 0 and
// longestPauseMs 0.
func TestInstallGivesUpWaitingForResponseHeaders(t *testing.T) {
	old := installHeaderTimeout
	installHeaderTimeout = 300 * time.Millisecond
	t.Cleanup(func() { installHeaderTimeout = old })

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Without the limit the run waits for these headers. The handler then
		// answers after 5 s, so the test fails with another text and does not hang.
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	defer close(release)

	url := srv.URL + "/nohdr"
	cliDir := filepath.Join(t.TempDir(), "cli")
	start := time.Now()
	out := captureInstallOutput(t, installOpts{cliDir: cliDir, cliVersion: "9.9.9", cliURL: url, cliChecksum: "x"})
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("the run took %v, want about the 300ms limit", d)
	}
	wantText := `download failed: Get "` + url + `": net/http: timeout awaiting response headers`
	b, _ := json.Marshal(wantText)
	head := factsHead(t, installCLIPath(cliDir, "9.9.9")) + `false,"cliError":` + string(b) + `,"fetch":{"bytes":0,"ms":`
	if !strings.HasPrefix(out, head) || !strings.HasSuffix(out, `,"longestPauseMs":0}}`+"\n") {
		t.Errorf("G12: stdout\n got %q\nwant the start %q and longestPauseMs 0", out, head)
	}
	if strings.Contains(out, "__INSTALL_PROGRESS__") {
		t.Errorf("G12: stdout %q holds a progress line, want none before the headers", out)
	}
}

// Row E07. A body read that ends with a transport error answers
// "download interrupted after <got>/<total> bytes: <error>", with no
// "download failed: " prefix. The wait from the last byte to the error counts for
// longestPauseMs. One progress line is printed, and the cli folder stays empty.
func TestInstallReportsAnInterruptedDownload(t *testing.T) {
	body := zstdOf(t, bytes.Repeat([]byte("claustrum"), 4096))
	half := len(body) / 2
	const pause = time.Second
	// The row had a pause of 0.3 s, under the 1 s tick. The test pause is 1 s, so
	// the tick is moved out of the way: a tick in the pause prints the half count.
	oldProg := installProgressInterval
	installProgressInterval = time.Hour
	t.Cleanup(func() { installProgressInterval = oldProg })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body[:half])
		w.(http.Flusher).Flush()
		time.Sleep(pause)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		// A linger of 0 makes the close send a reset.
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetLinger(0)
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	cliDir := filepath.Join(t.TempDir(), "cli")
	out := captureInstallOutput(t, installOpts{cliDir: cliDir, cliVersion: "9.9.9", cliURL: srv.URL + "/cli.zst", cliChecksum: "x"})
	line := out[strings.LastIndex(out, "__INSTALL_RESULT__"):]
	var f installFacts
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(line), "__INSTALL_RESULT__")), &f); err != nil {
		t.Fatalf("facts line %q: %v", line, err)
	}
	prefix := "download interrupted after " + strconv.Itoa(half) + "/" + strconv.Itoa(len(body)) + " bytes: read tcp "
	if !strings.HasPrefix(f.CliError, prefix) {
		t.Errorf("E07: cliError %q, want the start %q", f.CliError, prefix)
	}
	if runtime.GOOS == "linux" && !strings.HasSuffix(f.CliError, ": read: connection reset by peer") {
		t.Errorf("E07: cliError %q, want a connection reset", f.CliError)
	}
	if f.Fetch == nil || f.Fetch.Bytes != int64(half) {
		t.Fatalf("E07: fetch = %+v, want bytes %d", f.Fetch, half)
	}
	if got := f.Fetch.LongestPauseMs; got < (pause / 2).Milliseconds() {
		t.Errorf("E07: longestPauseMs = %d, want about %d: the wait for the error counts as a pause", got, pause.Milliseconds())
	}
	if counts, _ := progressCounts(t, out); !slices.Equal(counts, []int64{0}) {
		t.Errorf("E07: progress byte counts = %v, want the leading line only", counts)
	}
	if ents, err := os.ReadDir(cliDir); err != nil || len(ents) != 0 {
		t.Errorf("E07: cli folder entries %v, err %v, want it empty", ents, err)
	}
}

// An opted-in D12 deadline that fires in the body keeps its documented text. It
// is not an interrupted download.
func TestInstallDownloadDeadlineKeepsItsText(t *testing.T) {
	setCLIDownloadTimeout(t, 300*time.Millisecond)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("abcd"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	defer close(release)
	err := ensureCLI(installOpts{cliURL: srv.URL, cliChecksum: "x"}, filepath.Join(t.TempDir(), "v1"))
	// The stdlib adds a "(Client.Timeout …)" tail only when its clock is past the
	// deadline, which a coarse clock tick on Windows does not promise. So the
	// test pins the prefix, and that the text is not the interrupted one.
	const want = "download failed: context deadline exceeded"
	if err == nil || !strings.HasPrefix(err.Error(), want) || strings.Contains(err.Error(), "download interrupted") {
		t.Errorf("ensureCLI = %v, want the prefix %q and no interrupted text", err, want)
	}
}
