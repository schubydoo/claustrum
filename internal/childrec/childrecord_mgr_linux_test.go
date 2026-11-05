//go:build linux

package childrec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/schubydoo/claustrum/internal/procinfo"
)

// TestRecordChild drives RecordChild for a live pid (this process) and asserts the
// written record's fields, then confirms the three gates (no run dir, pid < 2, and an
// unreadable start-time) all skip the write.
func TestRecordChild(t *testing.T) {
	runDir := t.TempDir()
	m := &Records{}
	m.RunDir = runDir
	m.InstanceID = "inst-32hex-abc"

	m.RecordChild(os.Getpid(), "/bin/sleep", "")
	data, err := os.ReadFile(filepath.Join(runDir, "children", strconv.Itoa(os.Getpid())+".json"))
	if err != nil {
		t.Fatalf("record not written: %v", err)
	}
	var rec childRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("record not valid JSON: %v", err)
	}
	if rec.Pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", rec.Pid, os.Getpid())
	}
	if rec.Instance != "inst-32hex-abc" {
		t.Errorf("instance = %q, want the daemon instanceID", rec.Instance)
	}
	if rec.DaemonPid != os.Getpid() {
		t.Errorf("daemonPid = %d, want %d", rec.DaemonPid, os.Getpid())
	}
	if want := strconv.FormatInt(OwnStartTicks(), 10); rec.DaemonStart != want {
		t.Errorf("daemonStart = %q, want %q", rec.DaemonStart, want)
	}
	if want := strconv.FormatInt(procStartTicks(os.Getpid()), 10); rec.Start != want {
		t.Errorf("start = %q, want the child's /proc start-ticks %q", rec.Start, want)
	}
	if rec.Argv0 != "/bin/sleep" {
		t.Errorf("argv0 = %q, want /bin/sleep", rec.Argv0)
	}
	if rec.At <= 0 {
		t.Errorf("at = %d, want an epoch-ms timestamp > 0", rec.At)
	}
	if rec.Node == "" {
		t.Errorf("node is empty; want nodeID()'s boot-id/pidns")
	}
	if !strings.HasPrefix(rec.Host, "machine-id:") {
		t.Errorf("host = %q, want a machine-id: prefix", rec.Host)
	}

	// Gate: no run dir -> no write (and no panic).
	(&Records{}).RecordChild(os.Getpid(), "/bin/sleep", "")

	// Gate: pid < 2 -> skip.
	m.RecordChild(1, "/sbin/init", "")
	if _, err := os.Stat(filepath.Join(runDir, "children", "1.json")); !os.IsNotExist(err) {
		t.Errorf("pid 1 was recorded; the pid<2 gate must skip it")
	}

	// Gate: unreadable start-time (a dead/bogus pid) -> skip.
	bogus := 1 << 30
	m.RecordChild(bogus, "/x", "")
	if _, err := os.Stat(filepath.Join(runDir, "children", strconv.Itoa(bogus)+".json")); !os.IsNotExist(err) {
		t.Errorf("a pid with no readable start-time was recorded; the start==0 gate must skip it")
	}

	// A write failure is logged, not fatal: with the children path a file,
	// writeChildRecord fails and RecordChild takes its error-log branch (no panic, no
	// record written).
	badDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(badDir, "children"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mBad := &Records{}
	mBad.RunDir = badDir
	mBad.InstanceID = "x"
	mBad.RecordChild(os.Getpid(), "/bin/sleep", "")
	if _, err := os.Stat(filepath.Join(badDir, "children", strconv.Itoa(os.Getpid())+".json")); err == nil {
		t.Error("a record was written despite the write failing")
	}
}

// TestRecordChildNoProcLogsAndWritesNothing pins the spawn with no usable /proc: a
// stand-in /proc that holds only self/exe gives one WARN start-time line for a live
// pid, and no children folder. With the real /proc a pid that cannot be read logs nothing, and
// a readable pid gets its record and no line.
func TestRecordChildNoProcLogsAndWritesNothing(t *testing.T) {
	standIn := t.TempDir()
	if err := os.MkdirAll(filepath.Join(standIn, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(standIn, "self", "exe"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	self, bogus := os.Getpid(), 1<<30
	line := "WARN  [process.Manager] cannot read the start time of pid " + strconv.Itoa(self) +
		" (no /proc and no ps on this platform): a successor daemon will not be able to reap it\n"

	runDir := t.TempDir()
	m := &Records{}
	m.RunDir = runDir
	m.InstanceID = "x"

	oldRoot := procinfo.Root()
	t.Cleanup(func() { procinfo.SetRootForTest(oldRoot) })
	procinfo.SetRootForTest(standIn)
	out := captureLog(t, func() { m.RecordChild(self, "/bin/sleep", "") })
	if !strings.Contains(out, line) {
		t.Errorf("log = %q, want the line %q", out, line)
	}
	if _, err := os.Lstat(filepath.Join(runDir, "children")); !os.IsNotExist(err) {
		t.Errorf("children folder: err = %v, want none made", err)
	}

	procinfo.SetRootForTest(oldRoot)
	out = captureLog(t, func() { m.RecordChild(bogus, "/bin/sleep", "") })
	if strings.Contains(out, "start time of pid "+strconv.Itoa(bogus)+" ") {
		t.Errorf("log = %q, want no start-time line while /proc works", out)
	}
	out = captureLog(t, func() { m.RecordChild(self, "/bin/sleep", "") })
	if strings.Contains(out, "start time of pid "+strconv.Itoa(self)+" ") {
		t.Errorf("log = %q, want no start-time line for a readable pid", out)
	}
	if _, err := os.Stat(filepath.Join(runDir, "children", strconv.Itoa(self)+".json")); err != nil {
		t.Errorf("record of a readable pid: %v", err)
	}
}
