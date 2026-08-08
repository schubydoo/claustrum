//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
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
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	writeFile(t, filepath.Join(root, "regular.txt"), "regular content\n", 0o644)
	setRegularOnly(t, true)

	sock := startSocketServer(t)
	cl := dial(t, sock)
	got := []json.RawMessage{
		// The control: a regular file still reads normally.
		normPath(cl.call(req(1, "files.read", map[string]any{"path": filepath.Join(root, "regular.txt")})), root),
		// Would block on the reference until a writer opens; must return promptly here.
		normPath(cl.call(req(2, "files.read", map[string]any{"path": fifo})), root),
		// The reference answers {"content":"","exists":true} for this one.
		normPath(cl.call(req(3, "files.read", map[string]any{"path": "/dev/null"})), root),
	}
	assertGolden(t, "socket_files_read_nonregular.golden.json", encodeGolden(t, got))
}

// TestSocketFilesReadNonRegularDefault is the same three inputs at the SHIPPED
// default, where the guard is off and every row must match what the reference was
// measured to do. This is the golden D4's flip exists to produce; the opted-in one
// above is now the divergence.
//
// ⚠️ The FIFO row needs a writer and the opted-in one does not — that asymmetry IS
// the divergence, not test scaffolding. With the guard off, the read blocks in
// open exactly as the reference's does, so an unpaired FIFO would hang this test
// until the go test panic timeout rather than fail it. The writer supplies the
// half the reference waits for, which is also the shape that proves "not a
// permanent hang".
func TestSocketFilesReadNonRegularDefault(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	writeFile(t, filepath.Join(root, "regular.txt"), "regular content\n", 0o644)
	setRegularOnly(t, false)

	sock := startSocketServer(t)
	cl := dial(t, sock)

	// Opening a FIFO for writing blocks until a reader opens, so this rendezvous is
	// order-independent: whichever side arrives first waits for the other.
	wrote := make(chan error, 1)
	go func() { wrote <- os.WriteFile(fifo, []byte("fifo content\n"), 0o600) }()

	got := []json.RawMessage{
		normPath(cl.call(req(1, "files.read", map[string]any{"path": filepath.Join(root, "regular.txt")})), root),
		normPath(cl.call(req(2, "files.read", map[string]any{"path": fifo})), root),
		normPath(cl.call(req(3, "files.read", map[string]any{"path": "/dev/null"})), root),
	}
	if err := <-wrote; err != nil {
		t.Fatalf("fifo writer: %v", err)
	}
	assertGolden(t, "socket_files_read_nonregular_default.golden.json", encodeGolden(t, got))
}

// setRegularOnly flips the D4 guard for one test and restores it afterwards. The
// server runs in-process, so filesRead reads this package var directly.
func setRegularOnly(t *testing.T, on bool) {
	t.Helper()
	old := filesReadRegularOnly
	t.Cleanup(func() { filesReadRegularOnly = old })
	filesReadRegularOnly = on
}
