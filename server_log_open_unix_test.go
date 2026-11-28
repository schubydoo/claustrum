//go:build unix

package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// notKeptLine is the launcher line of an entry that keepOldLog does not rename. 13047a51
// writes the same text after its timestamp (measured on Linux and macOS, rows R1 to R3,
// R5 and O2). claustrum puts its level tag before it.
const notKeptLine = "INFO  [daemon] the last run's log is not kept as .old: it is no regular file, or has a second name"

// logOpenDir is a short folder with the socket and the log path in it.
func logOpenDir(t *testing.T) (dir, sock, logPath string) {
	t.Helper()
	dir = shortTempDir(t)
	return dir, filepath.Join(dir, "rpc.sock"), filepath.Join(dir, daemonLogName)
}

// notKeptLines returns the lines of buf about the .old log that name logPath. A shared log
// capture can hold lines of other tests, so only the lines of this path count.
func notKeptLines(buf *syncBuffer, logPath string) []string {
	var got []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "not kept as .old") && strings.Contains(l, logPath) {
			got = append(got, l)
		}
	}
	return got
}

// wantNotKeptLine asserts that buf holds the line of an entry that is not renamed, and no
// line that names logPath. That line names no path, so a line of another test can match
// it too. The test asserts that it is there, not its count.
func wantNotKeptLine(t *testing.T, buf *syncBuffer, logPath string) {
	t.Helper()
	if !strings.Contains(buf.String()+"\n", notKeptLine+"\n") {
		t.Errorf("log = %q, want the line %q", buf.String(), notKeptLine)
	}
	if got := notKeptLines(buf, logPath); len(got) != 0 {
		t.Errorf("log lines = %q, want none that name the log path", got)
	}
}

// openLogWithLimit runs openDaemonLog with a time limit, so an open that blocks on a FIFO
// fails the test and does not hang it.
func openLogWithLimit(t *testing.T, sock, logPath string) (*os.File, error) {
	t.Helper()
	type result struct {
		f   *os.File
		err error
	}
	done := make(chan result, 1)
	go func() {
		f, err := openDaemonLog(sock)
		done <- result{f, err}
	}()
	select {
	case got := <-done:
		return got.f, got.err
	case <-time.After(10 * time.Second):
		// Unblock the open with a reader, so the goroutine ends.
		if r, err := os.OpenFile(logPath, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			defer r.Close()
		}
		if got := <-done; got.f != nil {
			_ = got.f.Close()
		}
		t.Fatal("openDaemonLog blocked on the entry at the log path")
		return nil, nil
	}
}

// wantFreshLog asserts that the log path holds a regular file of mode 0600 with one link
// that is not the file of before (nil for no file before). A file system can give a new
// file the inode of a removed entry at once, as ext4 does. So a regular file of before
// must still be held, by an open handle or a second name, and it is compared by
// identity. An entry of another kind is told apart by its kind. A new file of a test
// with a before entry is empty.
func wantFreshLog(t *testing.T, logPath string, before os.FileInfo) {
	t.Helper()
	fi, err := os.Lstat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || st == nil || st.Nlink != 1 {
		t.Errorf("log entry = %v with %v links, want a regular file of mode 0600 with one link", fi.Mode(), st)
	}
	if before == nil {
		return
	}
	if before.Mode().IsRegular() && os.SameFile(before, fi) {
		t.Error("the log is the file of before, want a fresh file")
	}
	if fi.Size() != 0 {
		t.Errorf("log size = %d, want an empty fresh file", fi.Size())
	}
}

// wantFileText asserts the bytes of path.
func wantFileText(t *testing.T, path, want string) {
	t.Helper()
	if b, err := os.ReadFile(path); err != nil || string(b) != want {
		t.Errorf("%s = %q, %v, want %q unchanged", path, b, err, want)
	}
}

// Measured on 13047a51 (Linux and macOS, rows R1, R1b, R2, R2b, R3 and O2): an entry that
// is no regular file, or a regular file with a second name, is not renamed. The launcher
// logs one line, one remove deletes the entry, and a fresh 0600 log takes its place. A
// link target and the other name keep their bytes, and no FIFO is opened. The fixes are
// the kind and link count test of keepOldLog and the remove of openDaemonLogAt.
func TestDaemonLogReplacesAnEntryThatIsNotKept(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, dir, logPath string) (keep, body string)
	}{
		{"link to a file", func(t *testing.T, dir, logPath string) (string, string) {
			target := filepath.Join(dir, "sentinel")
			if err := os.WriteFile(target, []byte("SENTINEL\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, logPath); err != nil {
				t.Fatal(err)
			}
			return target, "SENTINEL\n"
		}},
		{"dangling link", func(t *testing.T, dir, logPath string) (string, string) {
			if err := os.Symlink(filepath.Join(dir, "missing"), logPath); err != nil {
				t.Fatal(err)
			}
			return "", ""
		}},
		{"FIFO", func(t *testing.T, _, logPath string) (string, string) {
			if err := syscall.Mkfifo(logPath, 0o600); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
			return "", ""
		}},
		{"socket", func(t *testing.T, _, logPath string) (string, string) {
			l, err := net.ListenUnix("unix", &net.UnixAddr{Name: logPath, Net: "unix"})
			if err != nil {
				t.Skipf("listen: %v", err)
			}
			l.SetUnlinkOnClose(false)
			_ = l.Close()
			return "", ""
		}},
		{"file with a second name", func(t *testing.T, dir, logPath string) (string, string) {
			if err := os.WriteFile(logPath, []byte("OLD-BODY\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(dir, "other.name")
			if err := os.Link(logPath, other); err != nil {
				t.Skipf("link: %v", err)
			}
			return other, "OLD-BODY\n"
		}},
		{"empty folder", func(t *testing.T, _, logPath string) (string, string) {
			if err := os.Mkdir(logPath, 0o755); err != nil {
				t.Fatal(err)
			}
			return "", ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, sock, logPath := logOpenDir(t)
			keep, body := tc.plant(t, dir, logPath)
			before, err := os.Lstat(logPath)
			if err != nil {
				t.Fatal(err)
			}
			buf := captureLogBuf(t)
			f, err := openLogWithLimit(t, sock, logPath)
			if f == nil || err != nil {
				t.Fatalf("openDaemonLog = %v, %v, want a fresh log", f, err)
			}
			_ = f.Close()
			wantFreshLog(t, logPath, before)
			if _, err := os.Lstat(logPath + ".old"); !os.IsNotExist(err) {
				t.Errorf("remote-server.log.old exists (err=%v), want none", err)
			}
			wantNotKeptLine(t, buf, logPath)
			if keep != "" {
				wantFileText(t, keep, body)
			}
			if tc.name == "file with a second name" {
				if fi, err := os.Stat(keep); err != nil || fi.Sys().(*syscall.Stat_t).Nlink != 1 {
					t.Errorf("the other name has not one link (err=%v)", err)
				}
			}
		})
	}
}

// Measured on 13047a51 (Linux and macOS, rows R4 and ctl_regular): a regular file with one
// link goes to remote-server.log.old with its bytes, a fresh 0600 log takes its place, and
// no line is logged. This is the control of the test above.
func TestDaemonLogKeepsAOneLinkFileAsOld(t *testing.T) {
	_, sock, logPath := logOpenDir(t)
	if err := os.WriteFile(logPath, []byte("OWN-BODY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buf := captureLogBuf(t)
	f, err := openDaemonLog(sock)
	if f == nil || err != nil {
		t.Fatalf("openDaemonLog = %v, %v, want a fresh log", f, err)
	}
	_ = f.Close()
	wantFreshLog(t, logPath, nil)
	wantFileText(t, logPath+".old", "OWN-BODY\n")
	if got := notKeptLines(buf, logPath); len(got) != 0 || strings.Contains(buf.String(), notKeptLine) {
		t.Errorf("log lines = %q, want none", got)
	}
}

// Measured on 13047a51 (Linux and macOS, row O5): a folder at remote-server.log.old stops
// the rename of a one-link file. The launcher logs the error of the rename, the file goes
// by the remove, and a fresh 0600 log takes its place. The folder at .old stays. The fix
// is the line after a failed rename in keepOldLog.
func TestDaemonLogReplacesAFileThatCannotRotate(t *testing.T) {
	_, sock, logPath := logOpenDir(t)
	if err := os.WriteFile(logPath, []byte("OWN-BODY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(logPath+".old", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logPath+".old", "keep"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	// The open handle holds the inode, so the new file cannot reuse it.
	held, err := os.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	buf := captureLogBuf(t)
	f, err := openDaemonLog(sock)
	if f == nil || err != nil {
		t.Fatalf("openDaemonLog = %v, %v, want a fresh log", f, err)
	}
	_ = f.Close()
	wantFreshLog(t, logPath, before)
	wantFileText(t, filepath.Join(logPath+".old", "keep"), "keep\n")
	want := "INFO  [daemon] the last run's log is not kept as .old: rename " + logPath + " " + logPath + ".old: file exists"
	if got := notKeptLines(buf, logPath); len(got) != 1 || got[0] != want {
		t.Errorf("log lines = %q, want [%q]", got, want)
	}
}

// A failed remove fails the open with its error, and the entry stays. Measured on
// 13047a51 (Linux and macOS, row R5): a folder with an entry gives "directory not empty".
// The second case is a file in a folder that the user cannot write. It stands for the
// sticky rows S1 and O4b, which a test cannot stage without root, and its error text is
// claustrum's own case, not measured. The fix is the error return of the remove in
// openDaemonLogAt.
func TestDaemonLogEntryThatStaysFailsTheOpen(t *testing.T) {
	t.Run("folder with an entry", func(t *testing.T) {
		_, sock, logPath := logOpenDir(t)
		if err := os.Mkdir(logPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(logPath, "in"), []byte("in\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		buf := captureLogBuf(t)
		f, err := openDaemonLog(sock)
		if want := "remove " + logPath + ": directory not empty"; f != nil || err == nil || err.Error() != want {
			t.Fatalf("openDaemonLog = %v, %v, want nil and the error %q", f, err, want)
		}
		wantFileText(t, filepath.Join(logPath, "in"), "in\n")
		wantNotKeptLine(t, buf, logPath)
	})
	t.Run("file in a folder that cannot be written", func(t *testing.T) {
		skipIfRoot(t)
		dir, sock, logPath := logOpenDir(t)
		if err := os.WriteFile(logPath, []byte("kept\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		buf := captureLogBuf(t)
		f, err := openDaemonLog(sock)
		if want := "remove " + logPath + ": permission denied"; f != nil || err == nil || err.Error() != want {
			t.Fatalf("openDaemonLog = %v, %v, want nil and the error %q", f, err, want)
		}
		wantFileText(t, logPath, "kept\n")
		want := "INFO  [daemon] the last run's log is not kept as .old: rename " + logPath + " " + logPath + ".old: permission denied"
		if got := notKeptLines(buf, logPath); len(got) != 1 || got[0] != want {
			t.Errorf("log lines = %q, want [%q]", got, want)
		}
	})
}

// Measured on 13047a51 (Linux and macOS, row R5): at the launcher a log entry that stays
// prints the refusal as its last stderr line and exits 1. No child starts, so no daemon,
// socket, daemon.lock or daemon.token comes (from the code). The fix is the error arm of
// daemonizeWithToken.
func TestDaemonizeFailsWhenTheLogEntryStays(t *testing.T) {
	stubOsExit(t)
	oldStart := startDaemonChild
	started := false
	startDaemonChild = func(*exec.Cmd) error { started = true; return errStartRefused }
	t.Cleanup(func() { startDaemonChild = oldStart })

	dir, sock, logPath := logOpenDir(t)
	if err := os.Mkdir(logPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logPath, "in"), []byte("in\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	captureLogBuf(t)
	var code int
	var exited bool
	stderr := captureStderr(t, func() {
		code, exited = catchExit(func() { daemonizeWithToken(sock, "") })
	})
	if !exited || code != 1 || started {
		t.Errorf("exited=%v code=%d started=%v, want exit 1 before any child start", exited, code, started)
	}
	if want := "claustrum: open log: remove " + logPath + ": directory not empty\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	for _, name := range []string{"rpc.sock", "daemon.lock", "daemon.token"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s exists (err=%v), want none", name, err)
		}
	}
}

// Measured on 87375cc7 and 13047a51 (macOS, rows L4 to L6): a folder above the socket folder
// that cannot be searched fails the start at the mkdir, before the log open. The launcher
// prints the error of the mkdir and exits 1. Linux is not measured. The fix
// is the error arm of the MkdirAll in daemonizeWithToken.
func TestDaemonizeFailsAtTheMkdirOfTheSocketFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root searches a folder of mode 000")
	}
	for _, tc := range []struct {
		name string
		mode os.FileMode
		sock string // the socket path below the closed folder
		want string // the folder whose mkdir fails, below the closed folder
	}{
		{"L4 run mode 000", 0o000, "c1/rpc.sock", "c1"},
		{"L5 run mode 600", 0o600, "c1/rpc.sock", "c1"},
		{"L6 remote mode 000", 0o000, "run/c1/rpc.sock", "run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubOsExit(t)
			oldStart := startDaemonChild
			started := false
			startDaemonChild = func(*exec.Cmd) error { started = true; return errStartRefused }
			t.Cleanup(func() { startDaemonChild = oldStart })

			closed := filepath.Join(shortTempDir(t), "closed")
			sock := filepath.Join(closed, tc.sock)
			if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(closed, tc.mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(closed, 0o700) })

			captureLogBuf(t)
			var code int
			var exited bool
			stderr := captureStderr(t, func() {
				code, exited = catchExit(func() { daemonizeWithToken(sock, "") })
			})
			if !exited || code != 1 || started {
				t.Errorf("exited=%v code=%d started=%v, want exit 1 before any child start", exited, code, started)
			}
			if want := "claustrum: mkdir " + filepath.Join(closed, tc.want) + ": permission denied\n"; stderr != want {
				t.Errorf("stderr = %q, want %q", stderr, want)
			}
		})
	}
}

// stubAfterLogRemove plants an entry at the path between the remove and the create of
// each of the first n tries.
func stubAfterLogRemove(t *testing.T, n int, plant func(path string)) *int {
	t.Helper()
	calls := 0
	old := afterLogRemove
	afterLogRemove = func(path string) {
		calls++
		if calls <= n {
			plant(path)
		}
	}
	t.Cleanup(func() { afterLogRemove = old })
	return &calls
}

// A link planted between the remove and the create (not measured) is never opened. The
// exclusive create fails, the next try removes the link, and a fresh log takes the path.
// After 3 such tries the open fails with the error of the create. The fixes are O_EXCL
// and the retry of openDaemonLogAt.
func TestDaemonLogCreateNeverOpensAPlantedEntry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		planted int
	}{{"once", 1}, {"at two tries", 2}, {"at all 3 tries", 3}} {
		t.Run(tc.name, func(t *testing.T) {
			dir, sock, logPath := logOpenDir(t)
			target := filepath.Join(dir, "sentinel")
			if err := os.WriteFile(target, []byte("SENTINEL\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			calls := stubAfterLogRemove(t, tc.planted, func(path string) {
				if err := os.Symlink(target, path); err != nil {
					t.Error(err)
				}
			})
			f, err := openDaemonLog(sock)
			if f != nil {
				_, _ = f.WriteString("daemon line\n")
				_ = f.Close()
			}
			if tc.planted < 3 {
				if f == nil || err != nil {
					t.Fatalf("openDaemonLog = %v, %v, want a fresh log", f, err)
				}
				wantFreshLog(t, logPath, nil)
				if *calls != tc.planted+1 {
					t.Errorf("tries = %d, want %d", *calls, tc.planted+1)
				}
			} else if want := "open " + logPath + ": file exists"; f != nil || err == nil || err.Error() != want {
				t.Errorf("openDaemonLog = %v, %v, want nil and the error %q", f, err, want)
			}
			wantFileText(t, target, "SENTINEL\n")
		})
	}
}
