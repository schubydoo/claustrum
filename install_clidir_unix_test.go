//go:build !windows

package main

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The symlink, FIFO and permission cells of the cli-dir rule (E12x-d, e, f, i
// and k, Linux and macOS VMs, f6010b97 and 89cb6289). See install_clidir_test.go.

// Cell d. A symlink to a regular file at the cli-dir path is removed as a link.
// Its target stays with its content. The version file beside the link goes.
func TestInstallReplacesASymlinkToAFileAtTheCLIDir(t *testing.T) {
	c := newCLIDirCell(t)
	target := c.file(t, "realfile", "the target\n")
	dir := filepath.Join(c.parent, "cli")
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	c.file(t, cliVersionMarker, "beside\n")
	cli := filepath.Join(dir, "9.9.9")
	out := captureInstallOutput(t, installOpts{cliDir: dir, cliVersion: "9.9.9", cliZst: c.stubBlob(t)})
	if want := factsHead(t, cli) + "false}\n"; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	wantFolder0700(t, dir)
	if !isRegularFile(cli) {
		t.Error("the CLI was not installed in the new cli folder")
	}
	wantContent(t, target, "the target\n")
	c.wantParent(t, "cli", "realfile")
}

// Cell e. A symlink to a folder triggers nothing: the install goes through the
// link, and the link, the folder and the version file stay.
func TestInstallKeepsASymlinkToAFolderAtTheCLIDir(t *testing.T) {
	c := newCLIDirCell(t)
	real := filepath.Join(c.parent, "realdir")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(c.parent, "cli")
	if err := os.Symlink(real, dir); err != nil {
		t.Fatal(err)
	}
	marker := c.file(t, cliVersionMarker, "beside\n")
	out := captureInstallOutput(t, installOpts{cliDir: dir, cliVersion: "9.9.9", cliZst: c.stubBlob(t)})
	if want := factsHead(t, filepath.Join(dir, "9.9.9")) + "false}\n"; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the cli-dir entry: err %v, want the symlink kept", err)
	}
	if !isRegularFile(filepath.Join(real, "9.9.9")) {
		t.Error("the CLI was not installed through the symlink")
	}
	wantContent(t, marker, "beside\n")
}

// Cell f. A dangling symlink triggers nothing and nothing is removed. The answer
// is the mkdir error with an empty cliPath, and the blob stays.
func TestInstallKeepsADanglingSymlinkAtTheCLIDir(t *testing.T) {
	c := newCLIDirCell(t)
	dir := filepath.Join(c.parent, "cli")
	if err := os.Symlink(filepath.Join(c.parent, "absent"), dir); err != nil {
		t.Fatal(err)
	}
	marker := c.file(t, cliVersionMarker, "beside\n")
	blob := c.stubBlob(t)
	out := captureInstallOutput(t, installOpts{cliDir: dir, cliVersion: "9.9.9", cliZst: blob})
	want := factsHead(t, "") + `false,"cliError":"mkdir cli dir: mkdir ` + dir + `: file exists"}` + "\n"
	if out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the cli-dir entry: err %v, want the dangling symlink kept", err)
	}
	wantContent(t, marker, "beside\n")
	if !isRegularFile(blob) {
		t.Error("the blob must stay")
	}
}

// Cell i. With a parent folder that is not writable nothing is removed. The
// answer is the mkdir error with an empty cliPath.
func TestInstallRemovesNothingBelowAReadOnlyParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a 0555 folder is still writable")
	}
	c := newCLIDirCell(t)
	dir := c.file(t, "cli", "a regular file\n")
	marker := c.file(t, cliVersionMarker, "beside\n")
	if err := os.Chmod(dir, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(c.parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(c.parent, 0o755) })
	blob := c.stubBlob(t)
	out := captureInstallOutput(t, installOpts{cliDir: dir, cliVersion: "9.9.9", cliZst: blob})
	want := factsHead(t, "") + `false,"cliError":"mkdir cli dir: mkdir ` + dir + `: not a directory"}` + "\n"
	if out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	wantContent(t, dir, "a regular file\n")
	wantContent(t, marker, "beside\n")
	if !isRegularFile(blob) {
		t.Error("the blob must stay")
	}
}

// installOutputBeside runs -install with a FIFO at or behind the cli-dir path
// and gives the run 20 s. A run that blocks on an open of the FIFO is released
// by an open for write. Then the helper waits for the capture goroutine, so
// that it gives os.Stdout back before the next test, and fails the test.
func installOutputBeside(t *testing.T, fifo string, o installOpts) string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- captureInstallOutput(t, o) }()
	return waitBesideFIFO(t, fifo, done)
}

// waitBesideFIFO is the bounded wait of installOutputBeside for any call.
func waitBesideFIFO[T any](t *testing.T, fifo string, done <-chan T) T {
	t.Helper()
	select {
	case v := <-done:
		return v
	case <-time.After(20 * time.Second):
		if fd, err := syscall.Open(fifo, syscall.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = syscall.Close(fd)
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the call blocked for 20s on the FIFO, and the reader did not return after the release")
		}
		t.Fatal("the call blocked for 20s: the FIFO was opened")
	}
	panic("unreachable")
}

// Cell k. A FIFO at the cli-dir path is removed without being opened, so the run
// does not block. The reference answered in 0.12 s. The test gives it 20 s.
func TestInstallReplacesAFIFOAtTheCLIDirWithoutBlocking(t *testing.T) {
	c := newCLIDirCell(t)
	dir := filepath.Join(c.parent, "cli")
	if err := syscall.Mkfifo(dir, 0o644); err != nil {
		t.Fatal(err)
	}
	c.file(t, cliVersionMarker, "beside\n")
	blob := c.stubBlob(t)
	cli := filepath.Join(dir, "9.9.9")
	out := installOutputBeside(t, dir, installOpts{cliDir: dir, cliVersion: "9.9.9", cliZst: blob})
	if want := factsHead(t, cli) + "false}\n"; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	wantFolder0700(t, dir)
	if !isRegularFile(cli) {
		t.Error("the CLI was not installed in the new cli folder")
	}
	c.wantParent(t, "cli")
}

// The sweep reads the cli-dir path after a mkdir error, and a FIFO can be at or
// behind that path. The read does not block. os.ReadDir opens the path with
// O_DIRECTORY, and that open answers "not a directory" for a FIFO at once
// (measured on Linux with strace, Go 1.26.8). An open without that flag waits
// for a writer.
func TestSweepDoesNotBlockOnAFIFO(t *testing.T) {
	for _, kind := range []string{"a FIFO", "a symlink to a FIFO"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			fifo := filepath.Join(parent, "realfifo")
			if err := syscall.Mkfifo(fifo, 0o644); err != nil {
				t.Fatal(err)
			}
			dir := fifo
			if kind != "a FIFO" {
				dir = filepath.Join(parent, "cli")
				if err := os.Symlink(fifo, dir); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan struct{}, 1)
			go func() {
				sweepFetchTemps(dir, time.Now())
				done <- struct{}{}
			}()
			waitBesideFIFO(t, fifo, done)
		})
	}
}

// Not measured on the reference: a ccd-cli-version that is a symlink. claustrum
// leaves the link and its target alone.
func TestInstallLeavesAVersionMarkerThatIsASymlink(t *testing.T) {
	c := newCLIDirCell(t)
	dir := c.file(t, "cli", "a regular file\n")
	target := c.file(t, "marker-target", "the target\n")
	marker := filepath.Join(c.parent, cliVersionMarker)
	if err := os.Symlink(target, marker); err != nil {
		t.Fatal(err)
	}
	out := captureInstallOutput(t, installOpts{cliDir: dir, cliVersion: "9.9.9", cliZst: c.stubBlob(t)})
	if want := factsHead(t, filepath.Join(dir, "9.9.9")) + "false}\n"; out != want {
		t.Errorf("stdout\n got %q\nwant %q", out, want)
	}
	if fi, err := os.Lstat(marker); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the version marker: err %v, want the symlink kept", err)
	}
	wantContent(t, target, "the target\n")
}

// Not measured on the reference: a symlink to a FIFO and a unix socket at the
// cli-dir path. claustrum leaves both alone. The entry keeps its type, the
// version file beside it keeps its content, and the answer is the mkdir error
// with an empty cliPath. After that error the sweep opens the path once, for its
// directory read. That open does not block: see TestSweepDoesNotBlockOnAFIFO.
//
// The cell is a short temp folder, not t.TempDir: macOS limits the path of a
// unix socket to 104 bytes.
func TestInstallLeavesOtherEntryTypesAtTheCLIDir(t *testing.T) {
	for _, kind := range []string{"a symlink to a FIFO", "a unix socket"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv(managedLauncherGateEnv, "")
			parent, err := os.MkdirTemp("", "cd")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(parent) })
			dir := filepath.Join(parent, "cli")
			var wantType os.FileMode
			if kind == "a unix socket" {
				ln, err := net.Listen("unix", dir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = ln.Close() })
				wantType = os.ModeSocket
			} else {
				fifo := filepath.Join(parent, "realfifo")
				if err := syscall.Mkfifo(fifo, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(fifo, dir); err != nil {
					t.Fatal(err)
				}
				wantType = os.ModeSymlink
			}
			marker := filepath.Join(parent, cliVersionMarker)
			if err := os.WriteFile(marker, []byte("beside\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// For the socket cell the release open of the helper fails and does
			// nothing.
			out := installOutputBeside(t, dir, installOpts{cliDir: dir, cliVersion: "9.9.9"})
			want := factsHead(t, "") + `false,"cliError":"mkdir cli dir: mkdir ` + dir + `: not a directory"}` + "\n"
			if out != want {
				t.Errorf("stdout\n got %q\nwant %q", out, want)
			}
			if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeType != wantType {
				t.Errorf("the cli-dir entry: err %v, info %v, want it kept as %s", err, fi, kind)
			}
			wantContent(t, marker, "beside\n")
		})
	}
}
