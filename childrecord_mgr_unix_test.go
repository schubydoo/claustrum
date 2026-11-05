//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRecordCallsWithNoUsableRunDir covers the record calls of a manager whose run dir
// cannot be opened, and the two gates of the removal. A write that cannot open the
// run dir logs the cannot-record line. A removal there, a removal with no run dir,
// and a removal for a pid below 2 do nothing and log nothing.
func TestRecordCallsWithNoUsableRunDir(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	m.runDir = filepath.Join(t.TempDir(), "gone")
	m.writeRecord(childRecord{Pid: 4242, Start: "1", Argv0: "a"})
	if want := "[process.Registry] cannot record child 4242: "; !strings.Contains(logs.String(), want) {
		t.Errorf("log = %q\nwant it to hold %q", logs.String(), want)
	}
	logs.Reset()
	m.removeChildRecord(4242)
	newTestProcManager(t).removeChildRecord(4242)

	held := newTestProcManager(t)
	held.runDir = t.TempDir()
	if err := writeChildRecord(held.runDir, childRecord{Pid: 1, Start: "1", Argv0: "a"}); err != nil {
		t.Fatal(err)
	}
	held.removeChildRecord(1)
	if _, err := os.Stat(filepath.Join(held.runDir, "children", "1.json")); err != nil {
		t.Errorf("the removal acted for a pid below 2: %v", err)
	}
	// A record that is already gone is not a failed removal.
	held.removeChildRecord(4242)
	// Only record lines count. A late line of another test can land in this capture.
	if out := logs.String(); strings.Contains(out, "[process.Registry]") {
		t.Errorf("these removals logged a record line, want none:\n%s", out)
	}
}

// TestRemoveRecordThroughChildrenSymlinkInsideRunDir pins the removal at the end of a
// child when children is a relative symlink to a folder inside the run dir. The
// removal goes through the link, as on 89cb6289. A file <pid>.json in the target is
// removed (Linux row SLb, macOS row KDb). With no such file, nothing is removed and no
// line is logged (Linux row SLa, macOS row KDa). A symlink that leads out of the run
// dir stays refused: TestChildrenNotADirectory pins that (measured).
func TestRemoveRecordThroughChildrenSymlinkInsideRunDir(t *testing.T) {
	logs := captureLogBuf(t)
	m := newTestProcManager(t)
	m.runDir = t.TempDir()
	placed := filepath.Join(m.runDir, "kids", "4242.json")
	if err := os.Mkdir(filepath.Dir(placed), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(placed, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("kids", filepath.Join(m.runDir, "children")); err != nil {
		t.Fatal(err)
	}

	m.removeChildRecord(4242) // the file is there
	if _, err := os.Stat(placed); !os.IsNotExist(err) {
		t.Errorf("the placed file is still there (stat error: %v), want it removed through the link", err)
	}
	m.removeChildRecord(4243) // no file for this pid

	if out := logs.String(); strings.Contains(out, "[process.Registry]") {
		t.Errorf("the removals logged a record line, want none:\n%s", out)
	}
	if fi, err := os.Lstat(filepath.Join(m.runDir, "children")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the children symlink changed: %v, %v", fi, err)
	}
}

// TestAwaitRecordsRemovedIsBounded pins the bound of the shutdown wait: a child whose
// exit goroutine never reports does not hold the daemon past shutdownRecordWait.
func TestAwaitRecordsRemovedIsBounded(t *testing.T) {
	old := shutdownRecordWait
	shutdownRecordWait = 50 * time.Millisecond
	t.Cleanup(func() { shutdownRecordWait = old })
	m := newTestProcManager(t)
	m.runDir = t.TempDir()
	m.procs["stuck"] = &managedProc{id: "stuck", recordDone: make(chan struct{})}
	m.procs["bare"] = &managedProc{id: "bare"} // no channel: nothing to wait for
	done := make(chan struct{})
	go func() { defer close(done); m.awaitRecordsRemoved() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the shutdown wait did not end at its bound")
	}
}
