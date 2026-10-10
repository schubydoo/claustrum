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

// TestSocketFilesReadNonRegular pins the rule of files.read for a path that is
// not a regular file, as 5fd08069 answers it (Linux and macOS VMs). The open
// comes first and the kind test follows it.
//
//	row 2  a FIFO with no writer is refused, and the open does not wait
//	row 3  a symlink to that FIFO is refused
//	row 4  the null device reads as empty content
//	row 5  a symlink to the null device reads as empty content
//	row 6  a symlink loop keeps the text of the open
//	row 7  a regular file with a trailing slash keeps the text of the open
//
// A device node needs root, so no row here makes one. The identity test of the
// null device is in TestFilesReadNullDeviceIsOneFile.
//
// The file is unix-tagged rather than runtime-skipped: syscall.Mkfifo does not
// exist on Windows, so a GOOS check inside the test would still fail to compile
// there.
func TestSocketFilesReadNonRegular(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	fifo := filepath.Join(root, "fifo")
	mustMkfifo(t, fifo)
	regular := filepath.Join(root, "regular.txt")
	writeFile(t, regular, "regular content\n", 0o644)
	for link, target := range map[string]string{
		"fifo_link": fifo, "null_link": os.DevNull, "loop_a": "loop_b", "loop_b": "loop_a",
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	// If the open waits for a writer, the daemon parks a goroutine in it and the
	// call below fails at the 5 s limit of the harness. This writer open then
	// lets that goroutine go. It fails at once when no reader waits.
	defer releaseFifoReader(fifo)

	sock := startSocketServer(t)
	cl := dial(t, sock)
	got := []json.RawMessage{
		normPath(cl.call(req(1, "files.read", map[string]any{"path": regular})), root),
		normPath(cl.call(req(2, "files.read", map[string]any{"path": fifo})), root),
		normPath(cl.call(req(3, "files.read", map[string]any{"path": filepath.Join(root, "fifo_link")})), root),
		normPath(cl.call(req(4, "files.read", map[string]any{"path": os.DevNull})), root),
		normPath(cl.call(req(5, "files.read", map[string]any{"path": filepath.Join(root, "null_link")})), root),
		normPath(cl.call(req(6, "files.read", map[string]any{"path": filepath.Join(root, "loop_a")})), root),
		normPath(cl.call(req(7, "files.read", map[string]any{"path": regular + "/"})), root),
	}
	assertGolden(t, "socket_files_read_nonregular.golden.json", encodeGolden(t, got))
}

// releaseFifoReader opens the FIFO for writing without a wait and closes it. A
// reader that waits in its open returns then.
func releaseFifoReader(path string) {
	if f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
		_ = f.Close()
	}
}

// TestSocketFilesReadFifoWithWriter pins the order of the open and the kind
// test. A writer waits in its open of the FIFO. files.read refuses the FIFO,
// and the open of the writer returns: the daemon opened the FIFO before it
// refused. 5fd08069 does the same (Linux and macOS VMs).
func TestSocketFilesReadFifoWithWriter(t *testing.T) {
	root := resolveTestRoot(t, t.TempDir())
	fifo := filepath.Join(root, "fifo")
	mustMkfifo(t, fifo)
	// The failure path leaves the writer in its open. A read-side open without
	// a wait lets it go. Registered before the writer starts.
	defer func() {
		if f, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
	}()
	opened := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err == nil {
			_ = f.Close()
		}
		opened <- err
	}()

	sock := startSocketServer(t)
	cl := dial(t, sock)
	// The request follows the start of the writer by the time of a server start
	// and a dial. If the writer is not in its open yet, the daemon still refuses,
	// and the open of the writer then fails or waits: the test fails loudly.
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := string(cl.call(req(1, "files.read", map[string]any{"path": fifo})))
		if !strings.Contains(got, `"code":-32602,"message":"files.read: not a regular file"`) {
			t.Fatalf("files.read of a FIFO with a writer = %s, want the refusal", got)
		}
		select {
		case err := <-opened:
			if err != nil {
				t.Fatalf("the open of the writer: %v", err)
			}
			return
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("the open of the writer did not return: files.read refused the FIFO without an open")
		}
	}
}

// TestFilesReadNullDeviceIsOneFile pins that the null device test is the
// identity of the file. 5fd08069 refuses a second device node with the numbers
// of /dev/null (Linux and macOS VMs). A device node needs root, so this test
// uses the nullDevicePath seam and two FIFOs. Both have the same kind and the
// same device numbers. The one that the seam names reads as empty content. The
// other is refused.
func TestFilesReadNullDeviceIsOneFile(t *testing.T) {
	root := t.TempDir()
	named, other := filepath.Join(root, "named"), filepath.Join(root, "other")
	mustMkfifo(t, named)
	mustMkfifo(t, other)
	old := nullDevicePath
	t.Cleanup(func() { nullDevicePath = old })
	nullDevicePath = named
	defer releaseFifoReader(named)
	defer releaseFifoReader(other)

	s := newTestServer(t)
	read := func(path string) string {
		t.Helper()
		done := make(chan string, 1)
		go func() { done <- dispatchRaw(t, s, rpcLine(t, "files.read", map[string]any{"path": path})) }()
		select {
		case got := <-done:
			return got
		case <-time.After(10 * time.Second):
			t.Fatalf("files.read of %s did not answer in 10 s", path)
			return ""
		}
	}
	if got := read(named); !strings.Contains(got, `"result":{"content":"","exists":true}`) {
		t.Errorf("files.read of the file that is the null device = %s, want empty content", got)
	}
	if got := read(other); !strings.Contains(got, "files.read: not a regular file") {
		t.Errorf("files.read of a second file of the same kind = %s, want the refusal", got)
	}
}

// mustMkfifo creates a FIFO, and FAILS rather than skips on the two platforms CI
// runs this file on. A bare t.Skipf would delete the FIFO rows of files.read
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
			"would silently drop the FIFO rows of files.read", path, err)
	}
	t.Skipf("mkfifo unavailable: %v", err)
}

// TestSocketFilesReadSocketErrorText pins a bound AF_UNIX socket: the open fails,
// so the answer is -32603 with the text of the open and not the -32602 of the
// kind test. 89cb6289 and 5fd08069 answer the same (Linux VM).
//
// Linux-only, for the same reason TestSocketListNonDirErrorText is: open() on a
// socket returns a different errno per kernel — ENXIO "no such device or address"
// on linux, EOPNOTSUPP "operation not supported on socket" on Darwin (measured on
// macOS 26.5). What is pinned is that the open comes before the kind test.
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
