//go:build unix

package main

import (
	"encoding/json"
	"path/filepath"
	"syscall"
	"testing"
)

// TestSocketFilesReadNonRegular pins sweep gap N3: the documented, intentional
// divergence on files.read.
//
// A FIFO read blocks in open while the FIFO has no writer, so the reference
// emits no frame for as long as that holds and a frame-diffing comparison cannot
// see the request at all. claustrum refuses the read instead, converting an
// indefinite wait into an immediate error.
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
func TestSocketFilesReadNonRegular(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	writeFile(t, filepath.Join(root, "regular.txt"), "regular content\n", 0o644)

	sock := startSocketServer(t)
	cl := dial(t, sock)
	got := []json.RawMessage{
		// The control: a regular file still reads normally.
		normPath(cl.call(req(1, "files.read", map[string]any{"path": filepath.Join(root, "regular.txt")})), root),
		// Would hang forever on the reference; must return promptly here.
		normPath(cl.call(req(2, "files.read", map[string]any{"path": fifo})), root),
		// The reference answers {"content":"","exists":true} for this one.
		normPath(cl.call(req(3, "files.read", map[string]any{"path": "/dev/null"})), root),
	}
	assertGolden(t, "socket_files_read_nonregular.golden.json", encodeGolden(t, got))
}
