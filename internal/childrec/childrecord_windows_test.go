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

// TestRemoveChildRecordWindowsKeepsPlantedRecord pins the end of a child on windows: a
// planted record for its pid stays (c9125eb8 and b3081b51, measured on Windows, row
// C12). The measured record held literal backslash-quotes, so it was not JSON.
// A remove that runs with no record of this daemon fails here.
func TestRemoveChildRecordWindowsKeepsPlantedRecord(t *testing.T) {
	m := &Records{RunDir: t.TempDir()}
	path := filepath.Join(m.RunDir, "children", "4242.json")
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{\"pid\":4242,\"start\":\"REWRITTEN-BY-OTHER-PROCESS\"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m.RemoveChildRecord(4242)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the planted record is gone on windows: %v", err)
	}
}
