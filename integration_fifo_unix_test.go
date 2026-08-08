//go:build unix

package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSocketFilesReadNonRegularOptedIn pins sweep gap N3: the documented,
// intentional divergence on files.read (D4), as an operator opts INTO it with
// -files-read-regular-only or the matching claustrum.conf key.
//
// A FIFO read blocks in open while the FIFO has no writer, so the reference
// emits no frame for as long as that holds and a frame-diffing comparison cannot
// see the request at all. The opted-in guard refuses the read instead, converting
// an indefinite wait into an immediate error.
//
// It is NOT a permanent hang, and this comment said it was until 2026-08-02:
// the reference replies normally the instant a writer opens. The "never replies"
// reading came from a probe that wrapped the read in `timeout 8` and never
// opened one — a harness deadline shorter than the subject's own blocking
// behaviour records "no reply" by construction. See docs/PROTOCOL.md.
//
// /dev/null is the cost of that guard rather than a separate decision: the check
// is Mode().IsRegular(), so it also rejects character devices the reference reads
// happily ({"content":"","exists":true}). Both rows are asserted so neither can
// drift unnoticed — see docs/PROTOCOL.md → files.read.
//
// The file is unix-tagged rather than runtime-skipped: syscall.Mkfifo does not
// exist on Windows, so a GOOS check inside the test would still fail to compile
// there.
func TestSocketFilesReadNonRegularOptedIn(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	fifo := filepath.Join(root, "fifo")
	mustMkfifo(t, fifo)
	writeFile(t, filepath.Join(root, "regular.txt"), "regular content\n", 0o644)
	setRegularOnly(t, true)

	sock := startSocketServer(t)
	cl := dial(t, sock)
	got := []json.RawMessage{
		// The control: a regular file still reads normally.
		normPath(cl.call(req(1, "files.read", map[string]any{"path": filepath.Join(root, "regular.txt")})), root),
		// Would block on the reference until a writer opens; must return promptly here.
		//
		// ⚠️ No writer, by design — the guard has to refuse before open(2) can
		// block. So the regression this row catches is the mirror of the one the
		// default test catches, and it surfaces differently: if the guard stops
		// arming, the daemon parks in open forever and the failure is cl.wait's
		// generic 5s "timeout waiting for expected responses/frames" (harness_test.go),
		// not a golden mismatch. That message on THIS test means D4. It also leaves a
		// request goroutine and its descriptor parked for the life of the package
		// binary; t.TempDir's unlink does not release them. Deliberately no rescue
		// here — the test already fails, and the default test is where the bounded
		// wait earns its keep.
		normPath(cl.call(req(2, "files.read", map[string]any{"path": fifo})), root),
		// The reference answers {"content":"","exists":true} for this one.
		normPath(cl.call(req(3, "files.read", map[string]any{"path": "/dev/null"})), root),
	}
	assertGolden(t, "socket_files_read_nonregular.golden.json", encodeGolden(t, got))
}

// TestSocketFilesReadNonRegularDefault pins the SHIPPED default, where the guard
// is off and every row must match what the reference was measured to do. This is
// the golden D4's flip exists to produce; the opted-in one above is now the
// divergence.
//
// Row 4 exists because the guard USED to hide it: while every non-regular path
// died at the mode check, an over-cap FIFO could not reach os.ReadFile. Turning
// the guard off makes it reachable, so it was measured against 5db5e4a before
// shipping and is locked here.
//
//	row 4  a FIFO carrying more than maxBytes reads IN FULL, because the cap keys
//	       off the stat size and that is 0 for a FIFO. The measured reference rows
//	       were 100 bytes at maxBytes:4 and 300000 bytes at the 256 KiB default —
//	       the second is the one that proves the cap is INERT rather than merely
//	       unhit, since it exceeds a pipe buffer and forces the reader to drain
//	       while the writer streams. ⚠️ Only the 40-byte size is committed
//	       anywhere. The 300000-byte measurement is one-off: it is recorded in
//	       the maxBytes paragraph BELOW docs/PROTOCOL.md's D4 table, not in the
//	       table itself, which calls this shape "measured but not tabled". The
//	       battery has no FIFO case at all — its one D4 case is /dev/null
//	       (battery.js id 70). So CI locks the 40-byte case for want of a
//	       committed home for the larger one, NOT because a larger payload would
//	       deadlock here: the writers are goroutines and the in-process daemon
//	       drains concurrently, so 300000 bytes completes fine.
//
//	       ⚠️ THREE earlier versions of this sentence were wrong — deadlock as
//	       the reason; then "the battery owns the load-bearing row"; then a
//	       pointer at PROTOCOL's table plus a claim about which other shapes are
//	       committed, which was false. Each was caught in review, and the third
//	       was caught twice: the correction for it was itself off by one,
//	       because it counted shapes. The claim that survives is narrow on
//	       purpose — where the 300000-byte row lives, and nothing about any
//	       other shape, with no tally to get wrong. Keep it that way.
//
//	       It is parity, not a claustrum property. Row 1 is its control: the same
//	       maxBytes on a regular file still errors.
//
// Deliberately NOT here, two kinds:
//
//   - a bound AF_UNIX socket — the frame text is platform-specific, so it has its
//     own linux-gated test below (see TestSocketFilesReadSocketErrorText).
//   - an unreadable device (/dev/console, a block device). Both were measured
//     identical on the reference, but the frame depends on the daemon's uid, and
//     a root runner does not merely get a different error: /dev/console blocks on
//     the tty (this test would hang) and a block device streams the disk into
//     memory. They live in docs/PROTOCOL.md's table rather than in a golden.
//
// ⚠️ The FIFO rows need a writer and the opted-in ones do not — that asymmetry IS
// the divergence, not test scaffolding. With the guard off, the read blocks in
// open exactly as the reference's does, so an unpaired FIFO would hang this test
// rather than fail it. The writer supplies the half the reference waits for, which
// is also the shape that proves "not a permanent hang".
func TestSocketFilesReadNonRegularDefault(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	fifo := filepath.Join(root, "fifo")
	bigFifo := filepath.Join(root, "fifo_over_cap")
	for _, p := range []string{fifo, bigFifo} {
		mustMkfifo(t, p)
	}
	writeFile(t, filepath.Join(root, "regular.txt"), "regular content\n", 0o644)
	setRegularOnly(t, false)

	// Rescue any writer still parked in open(O_WRONLY) — see the wait below for why
	// one might be. O_RDONLY|O_NONBLOCK on a FIFO returns immediately whether or not
	// a writer is present, so the rescue itself cannot hang. Registered BEFORE the
	// writers start and before any t.Fatal path, because cl.wait's own t.Fatal is a
	// runtime.Goexit: it skips the rest of the function, and only a defer still runs.
	defer func() {
		for _, p := range []string{fifo, bigFifo} {
			if f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = f.Close()
			}
		}
	}()

	sock := startSocketServer(t)
	cl := dial(t, sock)

	// Opening a FIFO for writing blocks until a reader opens, so this rendezvous is
	// order-independent: whichever side arrives first waits for the other. Confirmed
	// on macOS 26.5 as well as linux — Darwin ignores WriteFile's O_TRUNC on a FIFO
	// (POSIX leaves it undefined) and blocks in open just as linux does.
	wrote := make(chan error, 2)
	go func() { wrote <- os.WriteFile(fifo, []byte("fifo content\n"), 0o600) }()
	// 40 bytes against maxBytes:4 — over the cap, and small enough to fit a pipe
	// buffer, so the writer cannot deadlock waiting for the reader to drain.
	go func() { wrote <- os.WriteFile(bigFifo, []byte(strings.Repeat("Z", 40)), 0o600) }()

	got := []json.RawMessage{
		// Control: the cap DOES fire on a regular file at the same maxBytes.
		normPath(cl.call(req(1, "files.read", map[string]any{"path": filepath.Join(root, "regular.txt"), "maxBytes": 4})), root),
		normPath(cl.call(req(2, "files.read", map[string]any{"path": fifo})), root),
		normPath(cl.call(req(3, "files.read", map[string]any{"path": "/dev/null"})), root),
		normPath(cl.call(req(4, "files.read", map[string]any{"path": bigFifo, "maxBytes": 4})), root),
	}
	// The golden goes FIRST, so a real mismatch is reported as a mismatch rather
	// than being pre-empted by the writer wait below.
	assertGolden(t, "socket_files_read_nonregular_default.golden.json", encodeGolden(t, got))

	// Bounded receive, NOT a bare <-wrote. If a regression re-arms the guard, the
	// daemon refuses the FIFO rows instantly, nothing ever opens them for reading,
	// and the writers stay parked in open(O_WRONLY) forever — so a bare receive
	// turns an assertion failure into `panic: test timed out`, which takes the whole
	// package binary down and discards every other test's result. Measured against
	// that exact mutant, twice. CI passes no -timeout, so the default is 10 minutes,
	// on each of the linux and macOS legs.
	for range 2 {
		select {
		case err := <-wrote:
			if err != nil {
				t.Fatalf("fifo writer: %v", err)
			}
		// 10s, not 30: on a healthy run both sends have almost always already
		// landed in the buffered channel by the time the reads above returned.
		// Not "never waits" — the daemon's os.ReadFile returns on EOF, i.e. once
		// the writer closes, and the goroutine sends only after os.WriteFile
		// RETURNS, so the send races the reply by a scheduling window. Harmless:
		// the channel is buffered to its exact producer count, so no producer can
		// park, and the wait is bounded either way.
		// The bound only pays out on the failure path, where assertGolden has already
		// reported the real diagnostic — measured, that mutant now says "golden
		// mismatch" instead of "panic: test timed out".
		case <-time.After(10 * time.Second):
			t.Fatal("a fifo writer never completed: files.read answered a FIFO row without " +
				"opening it — the D4 guard is armed when it must be off by default")
		}
	}
}

// mustMkfifo creates a FIFO, and FAILS rather than skips on the two platforms CI
// runs this file on. A bare t.Skipf would delete D4's only default-parity golden
// with a green run — the silent-coverage-hole shape. Elsewhere (a temp filesystem
// that genuinely refuses FIFOs) the skip is still the right answer.
func mustMkfifo(t *testing.T, path string) {
	t.Helper()
	err := syscall.Mkfifo(path, 0o600)
	if err == nil {
		return
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		t.Fatalf("mkfifo %s: %v — this platform must support FIFOs, so a skip here "+
			"would silently drop the D4 default-parity coverage", path, err)
	}
	t.Skipf("mkfifo unavailable: %v", err)
}

// TestSocketFilesReadSocketErrorText pins the remaining shape D4's flip exposes: a
// bound AF_UNIX socket now reaches os.ReadFile and takes its ERROR arm, answering
// -32603 with Go's open text instead of the -32602 the guard used to give.
//
// Linux-only, for the same reason TestSocketListNonDirErrorText is: open() on a
// socket returns a different errno per kernel — ENXIO "no such device or address"
// on linux, EOPNOTSUPP "operation not supported on socket" on Darwin (measured on
// macOS 26.5). That is NOT a claustrum-vs-reference divergence; the reference is
// also Go and takes the same stdlib path, so it says the same thing on each OS.
// What is pinned is that the read reaches open() at all, which is exactly what the
// old guard prevented.
//
// ⚠️ The socket binds under os.MkdirTemp, NOT t.TempDir. Measured on macOS:
// t.TempDir() for a test of this name is 99 bytes, resolveTestRoot's /private
// prefix takes it to 107, and "/a.sock" to 114 — against Darwin's 104-byte
// sun_path. Binding under t.TempDir() made net.Listen fail there, and the t.Skipf
// that followed dropped EVERY row of the default golden on the macOS leg while the
// run stayed green — the socket row was one of them, which is why it lives here
// now. (That arrangement was never committed, so no golden in this repo's history
// shows it; the count is deliberately left unstated.) Same reasoning as
// harness_test.go's own socket dir, and the same silent-skip shape mustMkfifo
// exists to prevent — which is why the bind failure is now fatal.
//
// ⚠️ BOTH guards in this test are unprotected, measured by mutation: reverting the
// bind t.Fatalf to a t.Skipf, or flipping the GOOS gate to `== "linux"`, deletes
// this test with a fully green suite and no red run anywhere. That is inherent —
// no assertion can observe a test that never ran — and it is the same exposure
// mustMkfifo and TestSocketListNonDirErrorText carry, not new debt here. The only
// real mitigation is a CI check on the skip LIST (the macOS leg runs 4 skips and
// origin/main runs 3; a change in that count is the signal). Recorded so the next
// person to touch these two lines knows nothing will stop them.
func TestSocketFilesReadSocketErrorText(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("open() errno on a socket is platform-specific (linux ENXIO / darwin EOPNOTSUPP)")
	}
	sdir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sdir) })
	root := resolveTestRoot(t, sdir)
	setRegularOnly(t, false)

	sockPath := filepath.Join(root, "a.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("bind unix socket fixture at %s (%d bytes): %v", sockPath, len(sockPath), err)
	}
	defer ln.Close()

	sock := startSocketServer(t)
	cl := dial(t, sock)
	got := []json.RawMessage{
		normPath(cl.call(req(1, "files.read", map[string]any{"path": sockPath})), root),
	}
	assertGolden(t, "socket_files_read_socket_linux.golden.json", encodeGolden(t, got))
}

// setRegularOnly flips the D4 guard for one test and restores it afterwards. The
// server runs in-process, so filesRead reads this package var directly.
func setRegularOnly(t *testing.T, on bool) {
	t.Helper()
	old := filesReadRegularOnly
	t.Cleanup(func() { filesReadRegularOnly = old })
	filesReadRegularOnly = on
}
