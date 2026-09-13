//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestRecordChildWindowsNoop pins the verified windows contract: the daemon writes no child
// record on windows. Measured on a windows VM, the 19f30c46 reference daemon reports it is
// not the run-dir lock holder on windows and records nothing (a spawned child left the
// children dir empty). recordChild is a no-op even with a run dir set and a live pid. A
// mutant that ported the linux/darwin record writer to windows would fail here.
func TestRecordChildWindowsNoop(t *testing.T) {
	runDir := t.TempDir()
	m := newTestProcManager(t)
	m.runDir = runDir
	m.instanceID = "inst-32hex-abc"

	m.recordChild(os.Getpid(), `C:\Windows\System32\cmd.exe`)

	if _, err := os.Stat(filepath.Join(runDir, "children", strconv.Itoa(os.Getpid())+".json")); !os.IsNotExist(err) {
		t.Errorf("recordChild wrote a record on windows; it must be a no-op (never the run-dir lock holder)")
	}
	if _, err := os.Stat(filepath.Join(runDir, "children")); !os.IsNotExist(err) {
		t.Errorf("recordChild created the children dir on windows; it must write nothing")
	}
}
