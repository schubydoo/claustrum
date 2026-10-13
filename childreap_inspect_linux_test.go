//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// The cells of tier 0 round 2 (`5fd08069`, Linux VM) on a fake /proc tree. The reader is
// the real one, and the signal is a seam, so no real process gets a signal.

const (
	inspChild  = 800000501 // the recorded child C
	inspDaemon = 800000502 // the recorded daemon D
)

// inspectStage is one staged start: a fake /proc with this daemon's namespace link and
// a child C that passes every test of the reap, and one record of C by the daemon D. D
// has no /proc folder, so it is gone. That staging alone is the control: C gets SIGTERM.
type inspectStage struct {
	root, runDir, name string
	sim                *procSim
	mk                 func(pid int, name, content string)
	link               func(pid int, name, target string)
}

func stageInspect(t *testing.T) *inspectStage {
	t.Helper()
	if node, _ := ownReapIdentity(); node == "" {
		t.Skip("no node identity on this host")
	}
	st := &inspectStage{runDir: t.TempDir()}
	st.root, st.mk, st.link = fakeProc(t)
	if err := os.MkdirAll(filepath.Join(st.root, "self", "ns"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("pid:[1]", filepath.Join(st.root, "self", "ns", "pid")); err != nil {
		t.Fatal(err)
	}
	st.mk(inspChild, "stat", statLine(inspChild, "S", "4194304"))
	st.mk(inspChild, "cmdline", "/n\x00")
	st.mk(inspChild, "environ", "CLAUDE_SSH_RUN_DIR="+st.runDir+"\x00CLAUDE_SSH_CHILD="+strconv.Itoa(inspChild)+":9000\x00")
	st.link(inspChild, "ns/pid", "pid:[1]")
	rec := orphanRecord(inspChild, "/n", "")
	rec.DaemonPid = inspDaemon
	st.name = writeRec(t, st.runDir, rec)
	// The sim gives the signal seam and the clock. The reader is the real one again.
	st.sim = newProcSim(t)
	readLiveProc = realReadLiveProc
	return st
}

// path is the path of one entry of a fake /proc folder.
func (st *inspectStage) path(pid int, name string) string {
	return filepath.Join(st.root, strconv.Itoa(pid), name)
}

// run runs the reap and returns its log.
func (st *inspectStage) run(t *testing.T) string {
	t.Helper()
	return captureLog(t, func() { reapOrphans(st.runDir, "inst-self") })
}

// wantUntouched fails when C got a signal or its record is gone.
func (st *inspectStage) wantUntouched(t *testing.T) {
	t.Helper()
	if len(st.sim.kills) != 0 {
		t.Errorf("kills = %v, want no signal", st.sim.kills)
	}
	if !recordExists(st.runDir, st.name) {
		t.Error("the record is gone, want it kept")
	}
}

// noReadAsRoot skips a case that needs a read to fail for its mode.
func noReadAsRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode 000 entry")
	}
}

// TestReapInspectControl is the control of every test below: with nothing changed in
// the staging, C gets its SIGTERM.
func TestReapInspectControl(t *testing.T) {
	st := stageInspect(t)
	out := st.run(t)
	if st.sim.sigCount(syscall.SIGTERM) != 1 || !st.sim.signaled(inspChild) {
		t.Errorf("kills = %v, want a SIGTERM to %d\nlog = %q", st.sim.kills, inspChild, out)
	}
}

// TestReapKeepsChildOfDaemonItCannotInspect is cells H2a to H2c: the stat of the
// recorded daemon is there and cannot be read or parsed. claustrum counts the daemon
// as alive. `5fd08069` keeps the record of the child and sends no signal there.
func TestReapKeepsChildOfDaemonItCannotInspect(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage func(t *testing.T, st *inspectStage)
	}{
		{"H2a the stat is a folder", func(t *testing.T, st *inspectStage) {
			if err := os.MkdirAll(st.path(inspDaemon, "stat"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"H2b the stat has mode 000", func(t *testing.T, st *inspectStage) {
			noReadAsRoot(t)
			st.mk(inspDaemon, "stat", statLine(inspDaemon, "S", "4194304"))
			if err := os.Chmod(st.path(inspDaemon, "stat"), 0); err != nil {
				t.Fatal(err)
			}
		}},
		{"H2c the stat does not parse", func(t *testing.T, st *inspectStage) {
			st.mk(inspDaemon, "stat", "x")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := stageInspect(t)
			tc.stage(t, st)
			out := st.run(t)
			st.wantUntouched(t)
			if want := "0 orphaned group(s): 0 ended on SIGTERM, 0 on SIGKILL, 0 survived; 0 stale record(s) dropped, 1 kept"; !strings.Contains(out, want) {
				t.Errorf("log = %q\nwant the summary %q", out, want)
			}
		})
	}
}

// TestReapKeepsChildWithUnparseableGroup is cell H2d: the group field of the stat of
// the child is not an integer.
func TestReapKeepsChildWithUnparseableGroup(t *testing.T) {
	st := stageInspect(t)
	st.mk(inspChild, "stat", strings.Replace(statLine(inspChild, "S", "4194304"), " 1 "+strconv.Itoa(inspChild)+" ", " 1 abc ", 1))
	out := st.run(t)
	st.wantUntouched(t)
	want := "[process.Registry] record " + st.name + ": pid " + strconv.Itoa(inspChild) + " cannot be inspected now (unparseable stat); keeping the record, signalling nothing\n"
	if !strings.Contains(out, want) || !strings.Contains(out, "0 stale record(s) dropped, 1 kept") {
		t.Errorf("log = %q\nwant %q and one kept record", out, want)
	}
}

// TestReapDropsChildWithUnreadableStat is cell P7 of tier 0 round 3 (`5fd08069`, Linux
// VM): the read of the stat of the child answers "permission denied". The record goes
// with the line, no signal goes out, and there is no summary line.
func TestReapDropsChildWithUnreadableStat(t *testing.T) {
	noReadAsRoot(t)
	st := stageInspect(t)
	stat := st.path(inspChild, "stat")
	if err := os.Chmod(stat, 0); err != nil {
		t.Fatal(err)
	}
	out := st.run(t)
	if len(st.sim.kills) != 0 || recordExists(st.runDir, st.name) {
		t.Errorf("kills = %v, record there = %v, want no signal and no record", st.sim.kills, recordExists(st.runDir, st.name))
	}
	want := "[process.Registry] record " + st.name + ": pid " + strconv.Itoa(inspChild) +
		" is not a child to end (stat unreadable: open " + stat + ": permission denied); dropping the record, signalling nothing\n"
	if !strings.Contains(out, want) || strings.Contains(out, "predecessor's children") {
		t.Errorf("log = %q\nwant %q and no summary line", out, want)
	}
}

// TestReapKeepsChildWithStatThatIsAFolder: the read of the stat of the child fails with
// another error than "permission denied" and "does not exist". claustrum keeps the
// record and sends nothing (claustrum's own, not measured).
func TestReapKeepsChildWithStatThatIsAFolder(t *testing.T) {
	st := stageInspect(t)
	stat := st.path(inspChild, "stat")
	if err := os.Remove(stat); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stat, 0o755); err != nil {
		t.Fatal(err)
	}
	st.run(t)
	st.wantUntouched(t)
}

// TestReapDropsChildWithNoNamespaceLink is cell H3: the pid namespace link of the child
// does not exist. The record goes, with no signal and no line.
func TestReapDropsChildWithNoNamespaceLink(t *testing.T) {
	st := stageInspect(t)
	if err := os.Remove(st.path(inspChild, "ns/pid")); err != nil {
		t.Fatal(err)
	}
	out := st.run(t)
	if len(st.sim.kills) != 0 || recordExists(st.runDir, st.name) {
		t.Errorf("kills = %v, record there = %v, want no signal and no record", st.sim.kills, recordExists(st.runDir, st.name))
	}
	if strings.Contains(out, "[process.Registry]") {
		t.Errorf("log = %q, want no registry line", out)
	}
}

// TestReapDropsChildWithUnreadableNamespaceLink is cell H3v: the read of the pid
// namespace link of the child answers "permission denied".
func TestReapDropsChildWithUnreadableNamespaceLink(t *testing.T) {
	noReadAsRoot(t)
	st := stageInspect(t)
	ns := st.path(inspChild, "ns")
	if err := os.Chmod(ns, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ns, 0o755) })
	out := st.run(t)
	if len(st.sim.kills) != 0 || recordExists(st.runDir, st.name) {
		t.Errorf("kills = %v, record there = %v, want no signal and no record", st.sim.kills, recordExists(st.runDir, st.name))
	}
	want := "[process.Registry] record " + st.name + ": pid " + strconv.Itoa(inspChild) +
		" is not a child to end (pid namespace unreadable: readlink " + ns + "/pid: permission denied); dropping the record, signalling nothing\n"
	if !strings.Contains(out, want) {
		t.Errorf("log = %q\nwant %q", out, want)
	}
}

// TestNamespaceTestOnEveryRead: the read without the environment carries the result of
// the namespace test too, and the test right before a signal applies it. The read of
// a daemon does not apply it: its state stays as the stat gives it. Both are
// claustrum's own, not measured.
func TestNamespaceTestOnEveryRead(t *testing.T) {
	st := stageInspect(t)
	if v, _ := sameLeader(inspChild, "9000"); v != verdictReap {
		t.Fatalf("control: sameLeader = %d, want verdictReap", v)
	}
	if err := os.Remove(st.path(inspChild, "ns/pid")); err != nil {
		t.Fatal(err)
	}
	if v, _ := sameLeader(inspChild, "9000"); v != verdictGone {
		t.Errorf("no link: sameLeader = %d, want verdictGone", v)
	}
	if !daemonAlive(inspChild, "9000", 42) {
		t.Error("no link: daemonAlive = false, want the stat alone to decide for a daemon")
	}
	st.link(inspChild, "ns/pid", "pid:[2]")
	if v, _ := sameLeader(inspChild, "9000"); v != verdictUnknown {
		t.Errorf("another namespace: sameLeader = %d, want verdictUnknown", v)
	}
}

// TestReapTargetsNamespaceLinkGoesAfterSIGTERM: a target verifies and gets SIGTERM.
// Then its pid namespace link goes, while its stat still reads as alive and its group
// still answers. No SIGKILL goes out: the settle read stays on the stat, so the gone
// link never reaches reapGoneLeader. The group is given up after the grace, its record
// goes, and it counts as dropped. That result is claustrum's own, not measured.
func TestReapTargetsNamespaceLinkGoesAfterSIGTERM(t *testing.T) {
	st := stageInspect(t)
	st.sim.group[inspChild] = true
	st.sim.onKill[inspChild] = func(sig syscall.Signal) error {
		if sig == syscall.SIGTERM {
			if err := os.Remove(st.path(inspChild, "ns/pid")); err != nil {
				t.Errorf("remove the namespace link: %v", err)
			}
		}
		return nil
	}
	tg := reapTarget{pid: inspChild, start: "9000", name: st.name, rec: orphanRecord(inspChild, "/n", "")}
	var counts reapCounts
	reapTargets(simCtx(t, st.runDir), []reapTarget{tg}, &counts)

	if len(st.sim.kills) != 1 || st.sim.kills[0].sig != syscall.SIGTERM {
		t.Errorf("kills = %v, want exactly one SIGTERM and no SIGKILL", st.sim.kills)
	}
	if recordExists(st.runDir, st.name) {
		t.Error("the record is still there, want it removed")
	}
	if want := (reapCounts{signalled: 1, forgotten: 1}); counts != want {
		t.Errorf("counts = %+v, want %+v", counts, want)
	}
}
