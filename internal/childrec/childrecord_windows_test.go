//go:build windows

package childrec

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestRecordChildWindowsNoop pins the verified windows contract: the daemon writes no child
// record on windows. Measured on Windows, the 19f30c46 reference daemon reports it is
// not the run-dir lock holder on windows and records nothing (a spawned child left the
// children dir empty). RecordChild is a no-op even with a run dir set and a live pid. A
// mutant that ported the linux/darwin record writer to windows would fail here.
func TestRecordChildWindowsNoop(t *testing.T) {
	runDir := t.TempDir()
	m := &Records{}
	m.RunDir = runDir
	m.InstanceID = "inst-32hex-abc"

	m.RecordChild(os.Getpid(), `C:\Windows\System32\cmd.exe`, "")

	if _, err := os.Stat(filepath.Join(runDir, "children", strconv.Itoa(os.Getpid())+".json")); !os.IsNotExist(err) {
		t.Errorf("RecordChild wrote a record on windows; it must be a no-op (never the run-dir lock holder)")
	}
	if _, err := os.Stat(filepath.Join(runDir, "children")); !os.IsNotExist(err) {
		t.Errorf("RecordChild created the children dir on windows; it must write nothing")
	}
}
