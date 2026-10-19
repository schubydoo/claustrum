//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// logOpenFixture is a folder with an entry at remote-server.log that the rotate cannot
// move. A folder at .old stops the rename of a file, a link or a FIFO. A file at .old
// stops the rename of a folder. Round task015-03 stages the same two states (cells E1
// and E3) and a sticky folder of another user (cells S1 to S4). A test cannot make that
// sticky folder without root, so the S tests use the .old folder to stop the rename.
func logOpenFixture(t *testing.T, oldIsFolder bool) (dir, sock, logPath string) {
	t.Helper()
	dir = shortTempDir(t)
	sock = filepath.Join(dir, "rpc.sock")
	logPath = filepath.Join(dir, daemonLogName)
	if oldIsFolder {
		if err := os.Mkdir(logPath+".old", 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(logPath+".old", "inner.txt"), []byte("inner\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(logPath+".old", []byte("old-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, sock, logPath
}

// wantLogRefused asserts that openDaemonLog failed with "open <path>: <text>".
func wantLogRefused(t *testing.T, sock, logPath, text string) {
	t.Helper()
	f, err := openDaemonLog(sock)
	if f != nil {
		_ = f.Close()
	}
	if want := "open " + logPath + ": " + text; f != nil || err == nil || err.Error() != want {
		t.Fatalf("openDaemonLog = %v, %v, want nil and the error %q", f, err, want)
	}
}

// wantFileText asserts the bytes of path.
func wantFileText(t *testing.T, path, want string) {
	t.Helper()
	if b, err := os.ReadFile(path); err != nil || string(b) != want {
		t.Errorf("%s = %q, %v, want %q unchanged", path, b, err, want)
	}
}

// Cell E1: the rename fails and an own regular file is at the log path. The file is
// truncated and gets the lines, with its inode and its mode kept, as on 5fd08069. The
// second case is an own file in a folder that the user cannot write (not measured). The
// fix is the call of openHeldDaemonLog: a nil file there sends the lines elsewhere.
func TestDaemonLogTruncatesAnOwnFileThatCannotRotate(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		dir, sock, logPath := logOpenFixture(t, true)
		if err := os.WriteFile(logPath, []byte("old line 0\nold line 1\nold line 2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(logPath, 0o644); err != nil {
			t.Fatal(err)
		}
		before, err := os.Stat(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if readOnly {
			skipIfRoot(t)
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Fatal(err)
			}
		}
		f, err := openDaemonLog(sock)
		if readOnly {
			_ = os.Chmod(dir, 0o700)
		}
		if f == nil || err != nil {
			t.Fatalf("readOnly=%v: openDaemonLog = %v, %v, want the existing log", readOnly, f, err)
		}
		_, werr := f.WriteString("daemon line\n")
		_ = f.Close()
		if werr != nil {
			t.Fatal(werr)
		}
		after, err := os.Stat(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(before, after) || after.Mode().Perm() != 0o644 {
			t.Errorf("readOnly=%v: log is a new file or mode %o, want the same file at mode 644", readOnly, after.Mode().Perm())
		}
		wantFileText(t, logPath, "daemon line\n")
		wantFileText(t, filepath.Join(logPath+".old", "inner.txt"), "inner\n")
	}
}

// Cell E3: a folder is at the log path and .old is a file. openDaemonLog fails with
// "is a directory" and leaves both entries as they are. The fix is the folder case of
// openDaemonLog.
func TestDaemonLogFolderFailsTheOpen(t *testing.T) {
	_, sock, logPath := logOpenFixture(t, false)
	if err := os.Mkdir(logPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logPath, "inner.txt"), []byte("inner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantLogRefused(t, sock, logPath, "is a directory")
	wantFileText(t, filepath.Join(logPath, "inner.txt"), "inner\n")
	wantFileText(t, logPath+".old", "old-file\n")
}

// stubLinksOfAnotherUser makes every link read as an entry of another user. A test
// cannot own a link as another user without root.
func stubLinksOfAnotherUser(t *testing.T) {
	t.Helper()
	old := daemonLogOwned
	daemonLogOwned = func(fi os.FileInfo) bool { return fi.Mode().Type() != os.ModeSymlink && old(fi) }
	t.Cleanup(func() { daemonLogOwned = old })
}

// Cell E5: an own link at the log path to an own regular file. The file is truncated
// and gets the lines, with its inode kept, and the link stays. 5fd08069 does so (round
// task015-04, Linux VM). The fix is the link branch of openHeldDaemonLog.
func TestDaemonLogOpensAnOwnLinkToAnOwnFile(t *testing.T) {
	dir, sock, logPath := logOpenFixture(t, true)
	target := filepath.Join(dir, "V")
	if err := os.WriteFile(target, []byte("V-original"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, logPath); err != nil {
		t.Fatal(err)
	}
	f, err := openDaemonLog(sock)
	if f == nil || err != nil {
		t.Fatalf("openDaemonLog = %v, %v, want the target of the own link", f, err)
	}
	_, werr := f.WriteString("daemon line\n")
	_ = f.Close()
	if werr != nil {
		t.Fatal(werr)
	}
	if after, err := os.Stat(target); err != nil || !os.SameFile(before, after) {
		t.Errorf("the target is a new file (err=%v), want the same file", err)
	}
	wantFileText(t, target, "daemon line\n")
	if fi, err := os.Lstat(logPath); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link at the log path is gone or changed (err=%v)", err)
	}
}

// Cells S1 and S2, and the target tests of an own link: each link here fails the open
// with "permission denied". The target keeps its bytes, a missing name is not made, and
// the link stays. S1 and S2 are a link of another user, which the owner seam stages. The
// target tests (a missing name, a folder, a file with two links) are claustrum's own.
// The fixes are the owner test of the link and the target tests in openHeldDaemonLog.
func TestDaemonLogRefusesALink(t *testing.T) {
	for _, tc := range []struct {
		name    string
		foreign bool
		target  string // "file", "missing", "folder" or "two links"
	}{
		{"S1 link of another user to an own file", true, "file"},
		{"S2 link of another user to a missing name", true, "missing"},
		{"own link to a missing name", false, "missing"},
		{"own link to a folder", false, "folder"},
		{"own link to a file with two links", false, "two links"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.foreign {
				stubLinksOfAnotherUser(t)
			}
			dir, sock, logPath := logOpenFixture(t, true)
			target := filepath.Join(dir, "V")
			switch tc.target {
			case "file", "two links":
				if err := os.WriteFile(target, []byte("V-original"), 0o600); err != nil {
					t.Fatal(err)
				}
				if tc.target == "two links" {
					if err := os.Link(target, filepath.Join(dir, "V2")); err != nil {
						t.Skipf("link: %v", err)
					}
				}
			case "folder":
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, logPath); err != nil {
				t.Fatal(err)
			}
			wantLogRefused(t, sock, logPath, "permission denied")
			switch tc.target {
			case "missing":
				if _, err := os.Lstat(target); !os.IsNotExist(err) {
					t.Errorf("the missing target exists now (err=%v)", err)
				}
			case "file", "two links":
				wantFileText(t, target, "V-original")
			}
			if fi, err := os.Lstat(logPath); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Errorf("the link at the log path is gone or changed (err=%v)", err)
			}
		})
	}
}

// Cell S3: a FIFO at the log path. The open fails with "permission denied" at
// once and never opens the FIFO. 5fd08069 blocks in that open with the fs.protected
// sysctls off and on macOS. The fixes are the Lstat before any open and the case of an
// entry that is not a regular file. The wait has a limit, so a blocked open fails the
// test and does not hang it.
func TestDaemonLogRefusesAFIFOWithoutBlocking(t *testing.T) {
	_, sock, logPath := logOpenFixture(t, true)
	if err := syscall.Mkfifo(logPath, 0o666); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	type result struct {
		f   *os.File
		err error
	}
	done := make(chan result, 1)
	go func() {
		f, err := openDaemonLog(sock)
		done <- result{f, err}
	}()
	var got result
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		// Unblock the open with a reader, so the goroutine ends.
		if r, err := os.OpenFile(logPath, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			defer r.Close()
		}
		if got = <-done; got.f != nil {
			_ = got.f.Close()
		}
		t.Fatal("openDaemonLog blocked on the FIFO at the log path")
	}
	if got.f != nil {
		_ = got.f.Close()
	}
	if want := "open " + logPath + ": permission denied"; got.f != nil || got.err == nil || got.err.Error() != want {
		t.Errorf("openDaemonLog = %v, %v, want nil and the error %q", got.f, got.err, want)
	}
	if fi, err := os.Lstat(logPath); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the FIFO at the log path is gone or changed (err=%v)", err)
	}
}

// Cell S4: a regular file of another user at the log path. The open fails with
// "permission denied" and the file keeps its bytes. A test cannot own a file as another
// user without root, so the daemonLogOwned seam stages it. The fix is the owner test of
// openHeldDaemonLog.
func TestDaemonLogRefusesTheFileOfAnotherUser(t *testing.T) {
	_, sock, logPath := logOpenFixture(t, true)
	if err := os.WriteFile(logPath, []byte("eve-line-1\neve-line-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := daemonLogOwned
	daemonLogOwned = func(os.FileInfo) bool { return false }
	t.Cleanup(func() { daemonLogOwned = old })
	wantLogRefused(t, sock, logPath, "permission denied")
	wantFileText(t, logPath, "eve-line-1\neve-line-2\n")
}

// A hard link at the log path (not measured, claustrum's own): an own file with a second
// name is refused like a file of another user, so the other name is never truncated. The
// fix is the link count test of openHeldDaemonLog.
func TestDaemonLogRefusesAHardLink(t *testing.T) {
	dir, sock, logPath := logOpenFixture(t, true)
	other := filepath.Join(dir, "other-name")
	if err := os.WriteFile(other, []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(other, logPath); err != nil {
		t.Skipf("link: %v", err)
	}
	wantLogRefused(t, sock, logPath, "permission denied")
	wantFileText(t, other, "other\n")
}

// An own file that the user cannot write (not measured, claustrum's own): the error of
// the open fails the start, and the file keeps its bytes. The fix is the error return
// after syscall.Open in openHeldDaemonLog.
func TestDaemonLogOpenErrorFailsTheOpen(t *testing.T) {
	skipIfRoot(t)
	_, sock, logPath := logOpenFixture(t, true)
	if err := os.WriteFile(logPath, []byte("kept\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	wantLogRefused(t, sock, logPath, "permission denied")
	wantFileText(t, logPath, "kept\n")
}

// Cells E3 and S2 at the launcher: a refused log open prints one line on stderr
// and exits 1 before any child starts. So no daemon, socket, daemon.lock or daemon.token
// comes. The fix is the error arm of daemonizeWithToken.
func TestDaemonizeFailsWhenTheLogIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, dir, logPath string)
		text  string
	}{
		{"E3 folder", func(t *testing.T, _, logPath string) {
			if err := os.Mkdir(logPath, 0o700); err != nil {
				t.Fatal(err)
			}
		}, "is a directory"},
		{"link to a missing name", func(t *testing.T, dir, logPath string) {
			if err := os.Symlink(filepath.Join(dir, "V"), logPath); err != nil {
				t.Fatal(err)
			}
		}, "permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubOsExit(t)
			oldStart := startDaemonChild
			started := false
			startDaemonChild = func(*exec.Cmd) error { started = true; return errStartRefused }
			t.Cleanup(func() { startDaemonChild = oldStart })

			dir, sock, logPath := logOpenFixture(t, tc.name != "E3 folder")
			tc.plant(t, dir, logPath)
			var code int
			var exited bool
			stderr := captureStderr(t, func() {
				code, exited = catchExit(func() { daemonizeWithToken(sock, "") })
			})
			if !exited || code != 1 || started {
				t.Errorf("exited=%v code=%d started=%v, want exit 1 before any child start", exited, code, started)
			}
			if want := "claustrum: open log: open " + logPath + ": " + tc.text + "\n"; stderr != want {
				t.Errorf("stderr = %q, want %q", stderr, want)
			}
			for _, name := range []string{"rpc.sock", "daemon.lock", "daemon.token"} {
				if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Errorf("%s exists (err=%v), want none", name, err)
				}
			}
		})
	}
}
