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
// A FIFO read blocks forever with no writer, and the reference emits NO FRAME at
// all — probe-measured, the request simply never replies, which a frame-diffing
// comparison cannot even see. claustrum refuses the read instead, converting an
// unbounded hang into an immediate error.
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
