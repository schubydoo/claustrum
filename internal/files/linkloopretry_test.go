package files

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// loopFirstCalls makes the first n calls of statPath and openListDir answer
// ELOOP for path, and counts every call for path. The op of each error holds
// the number of its try. It records the pauses too.
func loopFirstCalls(t *testing.T, path string, n int) (calls *int, pauses *[]time.Duration) {
	t.Helper()
	calls, pauses = new(int), new([]time.Duration)
	fail := func(op string) error {
		*calls++
		if *calls <= n {
			return &fs.PathError{Op: op + strconv.Itoa(*calls), Path: path, Err: syscall.ELOOP}
		}
		return nil
	}
	oldStat, oldOpen, oldSleep := statPath, openListDir, sleepLinkLoop
	statPath = func(name string) (fs.FileInfo, error) {
		if name == path {
			if err := fail("stat"); err != nil {
				return nil, err
			}
		}
		return oldStat(name)
	}
	openListDir = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		if name == path {
			if err := fail("open"); err != nil {
				return nil, err
			}
		}
		return oldOpen(name, flag, perm)
	}
	sleepLinkLoop = func(d time.Duration) { *pauses = append(*pauses, d) }
	t.Cleanup(func() { statPath, openListDir, sleepLinkLoop = oldStat, oldOpen, oldSleep })
	return calls, pauses
}

// An ELOOP that a later try does not repeat gives the normal answer, on Linux
// within linkLoopTries tries. Off Linux the first ELOOP is the answer.
func TestLinkLoopRetryOfStatListValidate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t)
	cases := []struct{ method, ok, op string }{
		{"files.stat", `"exists":true,"isDir":true`, "stat"},
		{"files.list", `"name":"x"`, "open"},
		{"files.validate", `"valid":true,"isDir":true`, "stat"},
	}
	for _, c := range cases {
		for _, fails := range []int{1, linkLoopTries - 1, linkLoopTries} {
			if fails < 1 {
				continue
			}
			calls, pauses := loopFirstCalls(t, dir, fails)
			got := dispatchRaw(t, s, rpcLine(t, c.method, map[string]any{"path": dir}))
			// The reply holds the error of the last try.
			quoted, _ := json.Marshal(c.op + strconv.Itoa(linkLoopTries) + " " + dir + ": " + syscall.ELOOP.Error())
			loopText := strings.Trim(string(quoted), `"`)
			if fails < linkLoopTries {
				if !strings.Contains(got, c.ok) {
					t.Errorf("%s, %d ELOOP: %s, want %s", c.method, fails, got, c.ok)
				}
				if *calls != fails+1 {
					t.Errorf("%s, %d ELOOP: %d tries, want %d", c.method, fails, *calls, fails+1)
				}
			} else {
				if !strings.Contains(got, loopText) {
					t.Errorf("%s, %d ELOOP: %s, want %q", c.method, fails, got, loopText)
				}
				if *calls != linkLoopTries {
					t.Errorf("%s, %d ELOOP: %d tries, want %d", c.method, fails, *calls, linkLoopTries)
				}
			}
			if len(*pauses) != *calls-1 {
				t.Errorf("%s, %d ELOOP: %d pauses for %d tries", c.method, fails, len(*pauses), *calls)
			}
			for _, d := range *pauses {
				if d != linkLoopPause {
					t.Errorf("%s: pause %v, want %v", c.method, d, linkLoopPause)
				}
			}
		}
	}
}

// Linux makes 4 tries 10 ms apart (b3081b51, measured on Linux).
func TestLinkLoopTriesOnLinux(t *testing.T) {
	want := 1
	if runtime.GOOS == "linux" {
		want = 4
	}
	if linkLoopTries != want || linkLoopPause != 10*time.Millisecond {
		t.Errorf("linkLoopTries %d, pause %v, want %d and 10ms", linkLoopTries, linkLoopPause, want)
	}
}

// Any error but ELOOP is the answer of the first try.
func TestLinkLoopRetryOnlyForELOOP(t *testing.T) {
	calls := 0
	_, err := retryLinkLoop(func() (int, error) {
		calls++
		return 0, &fs.PathError{Op: "stat", Path: "p", Err: syscall.ENOTDIR}
	})
	if calls != 1 || err == nil {
		t.Errorf("ENOTDIR: %d tries, err %v, want 1 try and the error", calls, err)
	}
}
